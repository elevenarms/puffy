package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// handle runs one action. It returns the text for the next user turn, or
// done when the task is complete, or quit when the user ends the run.
func (a *Agent) handle(action Action) (result string, done, quit bool) {
	a.logf("=== action %s %s\n", action.Kind, action.Argument)
	switch action.Kind {
	case "complete":
		if action.Body != "" && a.settings.Quiet {
			fmt.Println(cleanControl(action.Body))
		}
		return "", true, false
	case "message":
		if a.settings.Quiet {
			fmt.Println(cleanControl(action.Body))
		}
		return "The user saw the message. Continue.", false, false
	case "question":
		if a.tty == nil {
			return "There is no terminal, so the user cannot answer. " +
				"Decide yourself and say what you chose.", false, false
		}
		if a.settings.Quiet {
			fmt.Println(cleanControl(action.Body))
		}
		answer := a.prompt("answer> ")
		a.logf("=== answer\n%s\n", answer)
		return "The user answered: " + answer, false, false
	case "search":
		return a.search(action), false, false
	case "man":
		return a.readMan(action), false, false
	case "script", "write", "edit":
		return a.runChange(action)
	}
	return "Unknown action " + action.Kind, false, false
}

// runChange asks the user, then runs a script, write or edit.
func (a *Agent) runChange(action Action) (string, bool, bool) {
	switch {
	case action.Kind == "script" && a.settings.NoScripts:
		return "Scripts are turned off for this run (PUFFY_NO_SCRIPTS). " +
			"Use another action.", false, false
	case action.Kind != "script" && a.settings.NoWrites:
		return "Writes and edits are turned off for this run " +
			"(PUFFY_NO_WRITES). Use another action.", false, false
	}
	if !a.settings.NoAsk {
		if a.tty == nil {
			return "There is no terminal to approve this action and " +
				"PUFFY_NO_ASK is not set, so it was not run. Finish with " +
				"# complete and tell the user what to run.", false, false
		}
		verdict, reason := a.approve(action)
		switch verdict {
		case "quit":
			return "", false, true
		case "explain":
			a.logf("=== user asked for an explanation\n")
			return "The user did not run this yet. Explain it for a " +
				"learner first with # message: what each command, flag " +
				"and file does, why it is the OpenBSD way, and which man " +
				"pages cover it. Then propose the action again.", false, false
		case "no":
			a.logf("=== rejected: %s\n", reason)
			text := "The user rejected this action."
			if reason != "" {
				text += " Their reason: " + reason
			}
			return text, false, false
		}
	}
	switch action.Kind {
	case "script":
		return a.runScript(action.Body), false, false
	case "write":
		return applyWrite(action.Argument, action.Body), false, false
	default:
		return applyEdit(action.Argument, action.Body), false, false
	}
}

func (a *Agent) approve(action Action) (verdict, reason string) {
	what := map[string]string{"script": "Run this script",
		"write": "Write " + action.Argument, "edit": "Edit " +
			action.Argument}[action.Kind]
	if a.settings.Quiet {
		fmt.Fprintf(a.tty, "%s\n", cleanControl(action.Body))
	}
	for {
		answer := strings.ToLower(a.prompt(what +
			"? [y]es [n]o [e]xplain [q]uit: "))
		switch {
		case answer == "y" || answer == "yes":
			return "yes", ""
		case answer == "e" || answer == "explain" || answer == "?":
			return "explain", ""
		case answer == "q" || answer == "quit":
			return "quit", ""
		case answer == "n" || answer == "no":
			return "no", a.prompt("Why not? (optional, sent to the model) ")
		case strings.HasPrefix(answer, "n "):
			return "no", strings.TrimSpace(answer[2:])
		}
	}
}

// Scripts -------------------------------------------------------------------

// ScriptRunner runs one script at a time. Ctrl-C stops the script, not
// puffy: the model hears that the user interrupted it.
type ScriptRunner struct {
	settings    Settings
	mutex       sync.Mutex
	running     *exec.Cmd
	interrupted bool
}

func newScriptRunner(s Settings) *ScriptRunner {
	runner := &ScriptRunner{settings: s}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	go func() {
		for range signals {
			runner.mutex.Lock()
			if runner.running == nil {
				runner.mutex.Unlock()
				fmt.Fprintln(os.Stderr, "\npuffy: interrupted")
				os.Exit(130)
			}
			runner.interrupted = true
			runner.running.Process.Signal(syscall.SIGINT)
			runner.mutex.Unlock()
		}
	}()
	return runner
}

func (a *Agent) runScript(script string) string {
	runner := a.runner
	ctx, cancel := context.WithTimeout(context.Background(),
		a.settings.ScriptTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	command.Stdin = nil // /dev/null; doas reads its password from /dev/tty
	var output bytes.Buffer
	var sink io.Writer = &output
	if !a.settings.Quiet {
		fmt.Println(a.out.label("--- output"))
		sink = io.MultiWriter(&output, os.Stdout)
	}
	command.Stdout, command.Stderr = sink, sink
	command.WaitDelay = 5 * time.Second

	runner.mutex.Lock()
	runner.interrupted = false
	err := command.Start()
	if err == nil {
		runner.running = command
	}
	runner.mutex.Unlock()
	if err == nil {
		err = command.Wait()
	}
	runner.mutex.Lock()
	runner.running = nil
	interrupted := runner.interrupted
	runner.mutex.Unlock()

	status := 0
	var exitError *exec.ExitError
	switch {
	case errors.As(err, &exitError):
		status = exitError.ExitCode()
	case err != nil:
		status = -1
		output.WriteString("\n" + err.Error())
	}
	if status == 0 {
		a.say("%s\n", a.out.good("--- exit status 0"))
	} else {
		a.say("%s\n", a.out.bad(fmt.Sprintf("--- exit status %d", status)))
	}
	a.logf("=== script output (exit %d)\n%s\n", status, output.String())

	note := ""
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		note = fmt.Sprintf(" It was killed after %s (PUFFY_SCRIPT_TIMEOUT).",
			a.settings.ScriptTimeout)
	case interrupted:
		note = " The user interrupted it with Ctrl-C."
	}
	return fmt.Sprintf("Script exit status %d.%s Output:\n%s", status, note,
		clip(output.String(), a.settings.OutputChars))
}

// clip keeps the head and tail of long output; the full text is in the log.
func clip(text string, limit int) string {
	if text == "" {
		return "(no output)"
	}
	if len(text) <= limit {
		return text
	}
	half := limit / 2
	return text[:half] + fmt.Sprintf("\n[... %d characters cut; narrow the "+
		"command (grep, head, tail) to see them ...]\n",
		len(text)-limit) + text[len(text)-half:]
}

// Writes and edits ----------------------------------------------------------

func applyWrite(path, body string) string {
	content := body
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	// Overwrite in place so the file keeps its owner and permissions.
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		return "Write failed: " + err.Error()
	}
	return fmt.Sprintf("Wrote %d bytes to %s.", len(content), path)
}

var editBlock = regexp.MustCompile(
	`(?s)<<<<<<< SEARCH\n(.*?)\n?=======\n(.*?)\n?>>>>>>> REPLACE`)

func applyEdit(path, body string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "Edit failed: " + err.Error()
	}
	blocks := editBlock.FindAllStringSubmatch(body, -1)
	if len(blocks) == 0 {
		return "Edit failed: no SEARCH/REPLACE block. Use <<<<<<< SEARCH, " +
			"=======, >>>>>>> REPLACE lines."
	}
	text := string(data)
	for number, block := range blocks {
		search, replace := block[1], block[2]
		if search == "" {
			return fmt.Sprintf("Edit failed: block %d has an empty search.",
				number+1)
		}
		count := strings.Count(text, search)
		if count != 1 {
			return fmt.Sprintf("Edit failed: block %d search text matched "+
				"%d times; it must match exactly once. Nothing was changed. "+
				"Read the file again and copy the lines exactly.",
				number+1, count)
		}
		text = strings.Replace(text, search, replace, 1)
	}
	info, _ := os.Stat(path)
	if err := os.WriteFile(path, []byte(text), info.Mode().Perm()); err != nil {
		return "Edit failed: " + err.Error()
	}
	return fmt.Sprintf("Edited %s (%d block(s)).", path, len(blocks))
}

// Knowledge base actions ----------------------------------------------------

func (a *Agent) search(action Action) string {
	if a.kb == nil {
		return "The knowledge base is not available: " + a.kbError.Error() +
			". Use man -k or man in a script instead."
	}
	kind := ""
	if action.Argument == "man" || action.Argument == "pkg" {
		kind = action.Argument
	}
	query := strings.TrimSpace(action.Body)
	hits, err := a.kb.Search(query, kind, 6)
	if err != nil {
		return "Search failed: " + err.Error()
	}
	result := formatHits(hits, 1800)
	a.logf("=== search results\n%s\n", result)
	if !a.settings.Quiet {
		fmt.Println(a.out.label("--- found"))
		for _, hit := range hits {
			fmt.Println("  " + a.out.label(hitLabel(hit)) + " " +
				a.out.comment(hit.Title))
		}
	}
	return "Search results for: " + query + "\n\n" + result
}

var manName = regexp.MustCompile(`^([^\s()]+)\s*(?:\(([0-9a-zA-Z]+)\))?$`)

func (a *Agent) readMan(action Action) string {
	if a.kb == nil {
		return "The knowledge base is not available. Use man in a script."
	}
	match := manName.FindStringSubmatch(action.Argument)
	if match == nil {
		return "Bad # man: line; use # man: NAME(SECTION), like pf.conf(5)."
	}
	heading := strings.TrimSpace(strings.SplitN(action.Body, "\n", 2)[0])
	hits, err := a.kb.Page(match[1], match[2], heading)
	if err != nil {
		return "Read failed: " + err.Error()
	}
	if len(hits) == 0 {
		return "No manual page or package named " + action.Argument +
			" in the knowledge base. Try # search."
	}
	var out strings.Builder
	for _, hit := range hits {
		out.WriteString(hit.Body + "\n\n")
	}
	if !a.settings.Quiet {
		fmt.Println(a.out.label(fmt.Sprintf("--- read %s (%d sections)",
			hitLabel(hits[0]), len(hits))))
	}
	text := out.String()
	if len(text) > 24000 {
		var headings []string
		for _, hit := range hits {
			headings = append(headings, hit.Heading)
		}
		text = clip(text, 24000) + "\nThis page is long. Its headings: " +
			strings.Join(headings, ", ") + ". Ask for one heading."
	}
	return text
}
