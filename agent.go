package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Agent runs the loop: ask the model, parse one action, approve it, run
// it, and send the result back as the next user turn.
type Agent struct {
	settings Settings
	chat     *ChatClient
	kb       *KnowledgeBase
	kbError  error
	log      *os.File
	tty      *os.File
	ttyIn    *bufio.Reader
	messages []Message
	usage    Usage
	runner   *ScriptRunner
	lastTask string
	out      Painter // stdout
	ttyPaint Painter // /dev/tty prompts
}

func newAgent(s Settings) (*Agent, error) {
	agent := &Agent{settings: s, chat: newChatClient(s),
		runner: newScriptRunner(s), out: newPainter(s.TerminalOutput),
		ttyPaint: newPainter(true)}
	agent.kb, agent.kbError = openKnowledgeBase(s)
	if s.Interactive {
		tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err == nil {
			agent.tty = tty
			agent.ttyIn = bufio.NewReader(tty)
		}
	}
	if err := agent.openLog(); err != nil {
		return nil, err
	}
	return agent, nil
}

func (a *Agent) close() {
	if a.kb != nil {
		a.kb.close()
	}
	if a.log != nil {
		a.log.Close()
	}
	if a.tty != nil {
		a.tty.Close()
	}
}

func (a *Agent) openLog() error {
	if err := os.MkdirAll(a.settings.LogFolder, 0o700); err != nil {
		return fmt.Errorf("cannot create %s; set PUFFY_LOG_FOLDER",
			a.settings.LogFolder)
	}
	name := time.Now().Format("20060102-150405") +
		fmt.Sprintf("-%d.puffylog", os.Getpid())
	file, err := os.OpenFile(filepath.Join(a.settings.LogFolder, name),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	a.log = file
	return nil
}

func (a *Agent) logf(format string, args ...any) {
	fmt.Fprintf(a.log, format, args...)
}

func (a *Agent) say(format string, args ...any) {
	if !a.settings.Quiet {
		fmt.Printf(format, args...)
	}
}

// note prints a puffy status line (not model text) in the notice color.
func (a *Agent) note(format string, args ...any) {
	a.say("%s\n", a.out.notice("puffy: "+fmt.Sprintf(format, args...)))
}

// run handles one task, then (on a terminal) keeps the conversation going
// with follow-up tasks until the user enters an empty line.
func (a *Agent) run(task string) int {
	if task == "" {
		if a.tty == nil {
			fmt.Fprintln(os.Stderr, "puffy: no task on the command line")
			return 1
		}
		task = a.prompt("puffy> ")
		if task == "" {
			return 0
		}
	}
	a.lastTask = task
	if a.kbError != nil {
		a.note("%v (search and man actions are off)", a.kbError)
	}
	restrictProcess(a.settings)

	// No key or PUFFY_OFFLINE: answer from the local docs until the user
	// asks to retry with a key in place.
	for reason := a.offlineReason(); reason != ""; reason = a.offlineReason() {
		if !a.offline(task, reason) {
			return 0
		}
		task = "" // already shown; a retry goes online with the same task
		a.settings = loadSettings()
		a.chat = newChatClient(a.settings)
	}
	if task == "" {
		task = a.lastTask
	}

	system := buildSystemPrompt(a.settings, "")
	a.messages = []Message{{Role: "system", Content: system}}
	a.logf("=== puffy %s model %s\n=== system\n%s\n", version,
		a.settings.Model, system)
	a.note("log %s", a.log.Name())

	first := "Task: " + task
	if !a.settings.TerminalStdin {
		input, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if strings.TrimSpace(string(input)) != "" {
			first += "\n\nContext from stdin:\n" + string(input)
		}
	}
	for {
		status := a.loop(first)
		for status == statusOffline {
			if !a.offline(a.lastTask, "cannot reach "+a.settings.APIBase) {
				a.finish()
				return 0
			}
			status = a.loop("") // resume: the conversation is intact
		}
		if status != 0 || a.tty == nil {
			a.finish()
			return status
		}
		next := a.prompt("\npuffy> (follow-up task, empty line to quit) ")
		if next == "" {
			a.finish()
			return 0
		}
		a.lastTask = next
		first = "Next task (same session): " + next
	}
}

func (a *Agent) finish() {
	a.logf("=== usage prompt=%d completion=%d\n", a.usage.PromptTokens,
		a.usage.CompletionTokens)
	a.note("%d prompt + %d completion tokens; log %s",
		a.usage.PromptTokens, a.usage.CompletionTokens, a.log.Name())
}

// statusOffline is loop's result when the model cannot be reached.
const statusOffline = -1

// offlineReason says why puffy cannot use the model, or "" when it can.
func (a *Agent) offlineReason() string {
	switch {
	case forcedOffline():
		return "PUFFY_OFFLINE is set"
	case a.settings.APIKey == "":
		return "no API key; run puffy --setup or set PUFFY_API_KEY"
	}
	return ""
}

// loop runs one task until the model replies # complete. An empty
// userText resumes after offline mode without adding a turn.
func (a *Agent) loop(userText string) int {
	for {
		if userText != "" {
			a.addMessage("user", userText)
		}
		reply, err := a.ask()
		if isNetworkError(err) {
			a.logf("=== model unreachable: %v\n", err)
			return statusOffline
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", a.out.bad(
				"puffy: model request failed: "+err.Error()))
			return 1
		}
		action, err := parseReply(reply)
		if err != nil {
			a.say("%s\n", a.out.bad("puffy: format error: "+err.Error()))
			a.logf("=== format error\n%v\n", err)
			userText = "Format error: " + err.Error() + ". Reply again " +
				"with exactly one action in a valid format, ending in # end."
			continue
		}
		result, done, quit := a.handle(action)
		if quit {
			return 1
		}
		if done {
			return 0
		}
		userText = result
	}
}

func (a *Agent) addMessage(role, content string) {
	a.messages = append(a.messages, Message{Role: role, Content: content})
	a.logf("=== %s\n%s\n", role, content)
	a.trimContext()
}

// trimContext drops the oldest turns (never the system prompt or the
// first task) when the conversation grows past PUFFY_CONTEXT_CHARS.
func (a *Agent) trimContext() {
	total := 0
	for _, message := range a.messages {
		total += len(message.Content)
	}
	for total > a.settings.ContextChars && len(a.messages) > 4 {
		dropped := a.messages[2]
		total -= len(dropped.Content)
		a.messages = append(a.messages[:2], a.messages[3:]...)
		a.logf("=== dropped an old %s turn (%d chars)\n", dropped.Role,
			len(dropped.Content))
	}
}

func (a *Agent) ask() (string, error) {
	a.say("\n")
	// Model text is shown line by line, control characters removed and
	// colored by role: actions bold cyan, # comments and # end dim.
	lines := &lineWriter{painter: a.out,
		write: func(text string) { fmt.Print(text) }}
	var onText func(string)
	if !a.settings.Quiet {
		onText = lines.add
	}
	started := time.Now()
	reply, usage, servedBy, err := a.chat.Complete(a.messages, onText,
		func(notice string) {
			a.note("%s", notice)
			a.logf("=== %s\n", notice)
		})
	if !a.settings.Quiet {
		lines.flush()
	}
	if err != nil {
		return "", err
	}
	a.usage.PromptTokens += usage.PromptTokens
	a.usage.CompletionTokens += usage.CompletionTokens
	reply = cutAtEndMarker(reply)
	a.messages = append(a.messages, Message{Role: "assistant", Content: reply})
	a.logf("=== assistant (%s, %s, %d+%d tokens)\n%s\n", servedBy,
		time.Since(started).Round(time.Millisecond), usage.PromptTokens,
		usage.CompletionTokens, reply)
	return reply, nil
}

func (a *Agent) prompt(text string) string {
	if a.tty == nil {
		return ""
	}
	fmt.Fprint(a.tty, "\a"+a.ttyPaint.prompt(text))
	line, err := a.ttyIn.ReadString('\n')
	if err != nil && line == "" {
		return ""
	}
	return strings.TrimSpace(line)
}

// Reply parsing -------------------------------------------------------------

// Action is one parsed reply.
type Action struct {
	Kind     string // script write edit search man question message complete
	Argument string // path, man page, or search filter
	Body     string
	Notes    []string // # comment lines before the action line
}

var actionLine = regexp.MustCompile(
	`^# ?(script|write file:|edit file:|search|man:|question|message|complete)\s*(.*)$`)

// gluedAction finds an action line stuck to the end of a sentence, as in
// "I will read the page.# man: pf.conf(5)" (reasoning leaking into the
// reply). Only sentence punctuation right before "#" counts, so shell
// comments like "echo hi # complete" are left alone.
var gluedAction = regexp.MustCompile(
	`([.!?])(# ?(?:script|write file:|edit file:|search|man:|question|message|complete))`)

// normalizeReply puts glued action lines and "# end#..." on lines of
// their own before parsing.
func normalizeReply(reply string) string {
	reply = gluedAction.ReplaceAllString(reply, "$1\n$2")
	return strings.ReplaceAll(reply, endMarker+"#", endMarker+"\n#")
}

// isEndLine is true for "# end", and for "# end#..." where the model kept
// going; everything after the marker is the model imagining the result.
func isEndLine(line string) bool {
	line = strings.TrimSpace(line)
	return line == endMarker || strings.HasPrefix(line, endMarker+"#")
}

func cutAtEndMarker(reply string) string {
	lines := strings.Split(normalizeReply(reply), "\n")
	for i, line := range lines {
		if isEndLine(line) {
			return strings.Join(lines[:i+1], "\n")
		}
	}
	return strings.TrimRight(reply, "\n")
}

func parseReply(reply string) (Action, error) {
	var action Action
	lines := strings.Split(normalizeReply(reply), "\n")
	end := len(lines)
	for i, line := range lines {
		if isEndLine(line) {
			end = i
			break
		}
	}
	lines = lines[:end]

	start := -1
	for i, line := range lines {
		match := actionLine.FindStringSubmatch(strings.TrimRight(line, " \t"))
		if match == nil {
			if trimmed := strings.TrimSpace(line); trimmed != "" &&
				!isFence(trimmed) {
				action.Notes = append(action.Notes, trimmed)
			}
			continue
		}
		action.Kind = strings.TrimSuffix(strings.Fields(match[1])[0], ":")
		action.Argument = strings.TrimSpace(match[2])
		start = i
		break
	}
	if start < 0 {
		if strings.TrimSpace(reply) == "" {
			return action, errors.New("empty reply")
		}
		return action, errors.New("no action line (# script, # write file:," +
			" # edit file:, # search, # man:, # question, # message," +
			" # complete)")
	}
	body := lines[start+1:]
	for len(body) > 0 && (strings.TrimSpace(body[len(body)-1]) == "" ||
		isFence(strings.TrimSpace(body[len(body)-1]))) {
		body = body[:len(body)-1]
	}
	for len(body) > 0 && isFence(strings.TrimSpace(body[0])) {
		body = body[1:]
	}
	action.Body = strings.Join(body, "\n")

	if action.Kind != "write" {
		for _, line := range body {
			m := actionLine.FindStringSubmatch(line)
			if m != nil && secondActionKinds[m[1]] {
				return action, fmt.Errorf("one action per reply; found "+
					"a second action line %q", line)
			}
		}
	}
	switch action.Kind {
	case "write", "edit":
		if action.Argument == "" {
			return action, errors.New("# " + action.Kind + " file: needs a path")
		}
	case "man":
		if action.Argument == "" {
			return action, errors.New("# man: needs a page name")
		}
	case "script", "search", "question":
		if strings.TrimSpace(action.Body) == "" {
			return action, errors.New("# " + action.Kind + " has an empty body")
		}
	}
	return action, nil
}

// A body may mention searching or reading a man page, but a line that
// starts another runnable action means the model packed two into one.
var secondActionKinds = map[string]bool{
	"script": true, "write file:": true, "edit file:": true,
	"question": true, "complete": true,
}

func isFence(line string) bool {
	return strings.HasPrefix(line, "```")
}
