package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// First-run onboarding. puffy runs it on a terminal when ~/.puffy/config
// does not exist yet, and again on puffy --setup. It asks for the API
// key, checks it, picks a confidential-compute model, checks the local
// pieces (knowledge base, sqlite-vector, embedder, doas) and writes
// ~/.puffy/config. It never changes anything outside ~/.puffy.

// configKeys are the settings onboarding writes to ~/.puffy/config.
var configKeys = []string{"PUFFY_MODEL", "PUFFY_API_KEY_FILE",
	"PUFFY_MAX_TOKENS", "PUFFY_NO_TEACH"}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".puffy", "config")
}

// loadConfigFile reads ~/.puffy/config (lines of PUFFY_NAME=value) into
// the environment. A variable already set in the environment wins, so
// "PUFFY_MODEL=x puffy ..." still works for one run.
func loadConfigFile() {
	file, err := os.Open(configPath())
	if err != nil {
		return
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") ||
			!strings.HasPrefix(name, "PUFFY_") {
			continue
		}
		if _, set := os.LookupEnv(name); !set {
			os.Setenv(name, strings.Trim(value, `"'`))
		}
	}
}

func needsOnboarding(s Settings) bool {
	_, err := os.Stat(configPath())
	return os.IsNotExist(err) && s.Interactive
}

type setupSession struct {
	settings Settings
	paint    Painter
	tty      *os.File
	in       *bufio.Reader
}

func runSetup(s Settings, firstRun bool) int {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "puffy: setup needs a terminal")
		return 1
	}
	defer tty.Close()
	session := &setupSession{settings: s, paint: newPainter(true), tty: tty,
		in: bufio.NewReader(tty)}
	return session.run(firstRun)
}

func (w *setupSession) say(format string, args ...any) {
	fmt.Fprintf(w.tty, format, args...)
}

func (w *setupSession) heading(text string) {
	w.say("\n%s\n", w.paint.action(text))
}

func (w *setupSession) ask(question, fallback string) string {
	shown := question
	if fallback != "" {
		shown += " [" + fallback + "]"
	}
	w.say("%s ", w.paint.prompt(shown+":"))
	line, _ := w.in.ReadString('\n')
	if line = strings.TrimSpace(line); line == "" {
		return fallback
	}
	return line
}

// askSecret reads a line without echo, using stty(1) on the terminal.
func (w *setupSession) askSecret(question string) string {
	stty := func(mode string) {
		command := exec.Command("stty", mode)
		command.Stdin = w.tty
		command.Run()
	}
	stty("-echo") // before the prompt, so fast typing is not echoed
	w.say("%s ", w.paint.prompt(question+":"))
	line, _ := w.in.ReadString('\n')
	stty("echo")
	w.say("\n")
	return strings.TrimSpace(line)
}

func (w *setupSession) ok(text string) {
	w.say("  %s %s\n", w.paint.good("ok"), text)
}

func (w *setupSession) warn(text, fix string) {
	w.say("  %s %s\n", w.paint.notice("!!"), text)
	if fix != "" {
		w.say("     %s %s\n", w.paint.comment("fix:"), fix)
	}
}

func (w *setupSession) run(firstRun bool) int {
	s := w.settings
	if firstRun {
		w.say("\n%s\n", w.paint.action("Welcome to puffy."))
	} else {
		w.say("\n%s\n", w.paint.action("puffy setup"))
	}
	w.say(`
puffy helps you run, configure and program OpenBSD, and teaches you as it
goes. You give it a task in plain words. The model answers with one action
at a time (a script, a file write or edit, a man page lookup) and puffy
asks before running anything that changes the system:

  [y]es  run it     [n]o  refuse, and tell it why
  [e]xplain  have it explain each command and flag first   [q]uit

Answers come from OpenBSD's own manual pages and package catalog, stored
on this machine. Only the chat itself goes over the network, to
TrustedRouter, on confidential-compute hardware by default.
`)

	// 1. API key
	w.heading("1. TrustedRouter API key")
	keyFile := s.APIKeyFile
	key := s.APIKey
	if key != "" {
		w.ok("found a key in " + keyFile)
		if strings.HasPrefix(strings.ToLower(w.ask("Replace it? (y/n)", "n")), "y") {
			key = ""
		}
	}
	for key == "" {
		w.say("  Create one at https://trustedrouter.com (keys start with sk-tr-).\n")
		key = w.askSecret("Paste your API key (input is hidden)")
		if key == "" {
			w.say("  No key entered. Run puffy --setup when you have one.\n")
			return 1
		}
		s.APIKey = key
		models, err := fetchModels(s)
		if isNetworkError(err) {
			w.warn("cannot reach TrustedRouter to check the key ("+
				err.Error()+"); saving it unchecked",
				"puffy works offline with local search until the network "+
					"is back; puffy --doctor checks the key")
		} else if err != nil {
			w.warn("the key did not work: "+err.Error(), "check the key and try again")
			key = ""
			continue
		}
		if err := os.MkdirAll(filepath.Dir(keyFile), 0o700); err != nil {
			w.warn("cannot create "+filepath.Dir(keyFile), "")
			return 1
		}
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			w.warn("cannot write "+keyFile+": "+err.Error(), "")
			return 1
		}
		w.ok(fmt.Sprintf("key works (%d models offered); saved to %s, mode 0600",
			len(models), keyFile))
	}
	s.APIKey = key

	// 2. Model
	w.heading("2. Model")
	model := w.chooseModel(s)

	// 3. Teaching
	w.heading("3. Teaching")
	w.say("  With teaching on, puffy explains each step, names the man pages\n" +
		"  behind it, and ends with what changed, how to undo it, and what to\n" +
		"  read next. Turn it off for terse output.\n")
	teach := !strings.HasPrefix(strings.ToLower(
		w.ask("Keep teaching on? (y/n)", "y")), "n")

	// 4. Local checks
	w.heading("4. This machine")
	w.checkMachine(s)

	// 5. Save
	values := map[string]string{
		"PUFFY_MODEL":        model,
		"PUFFY_API_KEY_FILE": keyFile,
		"PUFFY_MAX_TOKENS":   fmt.Sprint(s.MaxTokens),
		"PUFFY_NO_TEACH":     map[bool]string{true: "0", false: "1"}[teach],
	}
	var text strings.Builder
	text.WriteString("# puffy settings, written by puffy --setup on " +
		time.Now().Format("2006-01-02") + ".\n# Environment variables " +
		"override these. See README.md for all settings.\n")
	for _, name := range configKeys {
		text.WriteString(name + "=" + values[name] + "\n")
	}
	if err := os.WriteFile(configPath(), []byte(text.String()), 0o600); err != nil {
		w.warn("cannot write "+configPath()+": "+err.Error(), "")
		return 1
	}
	w.heading("Done")
	w.ok("settings saved to " + configPath())
	w.say(`
Try:
  puffy --search "redirect a port with pf"     search the docs yourself
  puffy explain how OpenBSD boots, step by step
  puffy enable ntpd and show me how to check it
  puffy --setup                                 run this again
`)
	return 0
}

type routerModel struct {
	ID      string `json:"id"`
	Context int    `json:"context_length"`
	Arch    struct {
		Output []string `json:"output_modalities"`
	} `json:"architecture"`
	Router struct {
		Confidential bool   `json:"provider_confidential_compute"`
		Tier         string `json:"privacy_tier_label"`
		Prompt       int64  `json:"prompt_price_microdollars_per_million_tokens"`
		Completion   int64  `json:"completion_price_microdollars_per_million_tokens"`
		Chat         *bool  `json:"supports_chat"`
	} `json:"trustedrouter"`
}

func fetchModels(s Settings) ([]routerModel, error) {
	request, _ := http.NewRequest("GET", s.APIBase+"/models", nil)
	request.Header.Set("Authorization", "Bearer "+s.APIKey)
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s from %s/models", response.Status, s.APIBase)
	}
	var list struct {
		Data []routerModel `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		return nil, err
	}
	return list.Data, nil
}

// suggestedModels are offered first, in this order, when the router
// lists them in a confidential privacy tier.
var suggestedModels = []string{"trustedrouter/e2e",
	"trustedrouter/confidential", "qwen/qwen3-32b",
	"trustedrouter/professional"}

// isConfidential needs both facts: the provider runs confidential compute
// and the router's privacy tier says so (some "confidential" providers
// are only in a no-store tier).
func isConfidential(model routerModel) bool {
	return model.Router.Confidential &&
		strings.Contains(model.Router.Tier, "Confidential")
}

func (w *setupSession) chooseModel(s Settings) string {
	models, err := fetchModels(s)
	if err != nil {
		w.warn("could not list models: "+err.Error(),
			"keeping "+s.Model+"; run puffy --setup again when online")
		return s.Model
	}
	byID := map[string]routerModel{}
	for _, model := range models {
		byID[model.ID] = model
	}
	var choices []routerModel
	for _, id := range suggestedModels {
		if model, ok := byID[id]; ok && isConfidential(model) {
			choices = append(choices, model)
		}
	}
	confidential := 0
	for _, model := range models {
		if isConfidential(model) && isChatModel(model) {
			confidential++
		}
	}
	w.say("  %d of %d models run on confidential compute. Suggested:\n\n",
		confidential, len(models))
	for i, model := range choices {
		w.say("  %s %-28s %-26s $%.2f / $%.2f per M tokens\n",
			w.paint.action(fmt.Sprintf("%d)", i+1)), model.ID,
			w.paint.comment(model.Router.Tier),
			float64(model.Router.Prompt)/1e6,
			float64(model.Router.Completion)/1e6)
	}
	w.say("  %s list every confidential chat model\n",
		w.paint.action(fmt.Sprintf("%d)", len(choices)+1)))
	for {
		answer := w.ask("Choose a number, or type a model id", "1")
		var number int
		if _, err := fmt.Sscan(answer, &number); err == nil {
			switch {
			case number >= 1 && number <= len(choices):
				w.ok("model " + choices[number-1].ID)
				return choices[number-1].ID
			case number == len(choices)+1:
				w.listConfidential(models)
				continue
			}
		}
		model, ok := byID[answer]
		switch {
		case !ok:
			w.warn(answer+" is not offered by the router", "")
		case !isConfidential(model):
			w.warn(answer+" is not confidential compute ("+model.Router.Tier+")",
				"pick one from the list, or set PUFFY_MODEL yourself later")
		default:
			w.ok("model " + answer)
			return answer
		}
	}
}

func isChatModel(model routerModel) bool {
	if model.Router.Chat != nil {
		return *model.Router.Chat
	}
	for _, kind := range model.Arch.Output {
		if kind == "text" {
			return true
		}
	}
	return false
}

func (w *setupSession) listConfidential(models []routerModel) {
	var ids []string
	for _, model := range models {
		if isConfidential(model) && isChatModel(model) {
			ids = append(ids, model.ID)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		w.say("    %s\n", id)
	}
}

func (w *setupSession) checkMachine(s Settings) {
	if kb, err := openKnowledgeBase(s); err != nil {
		w.warn("knowledge base: "+err.Error(),
			"doas make install from the puffy source, or set PUFFY_KB")
	} else {
		var docs int
		kb.db.QueryRow("SELECT count(*) FROM docs").Scan(&docs)
		w.ok(fmt.Sprintf("knowledge base: %d chunks, OpenBSD %s man pages "+
			"and packages", docs, kb.meta["openbsd_release"]))
		if kb.vectors {
			w.ok("sqlite-vector loaded from " + s.VectorExt)
		} else {
			w.warn("sqlite-vector not loaded; search is keyword-only",
				"install vector.so to "+s.VectorExt+" or set PUFFY_VECTOR_EXT")
		}
		kb.close()
	}
	if s.EmbedURL == "" {
		if _, err := exec.LookPath(s.EmbedServer); err != nil {
			w.warn(s.EmbedServer+" not found; search is keyword-only",
				"doas pkg_add llama.cpp")
		} else if _, err := os.Stat(s.EmbedModel); err != nil {
			w.warn("embedding model "+s.EmbedModel+" not found",
				"install it there or set PUFFY_EMBED_MODEL")
		} else {
			w.ok("local embedder: " + filepath.Base(s.EmbedModel))
		}
	}
	w.checkDoas()
}

// checkDoas explains root access the OpenBSD way. It only suggests;
// /etc/doas.conf is root's to write.
func (w *setupSession) checkDoas() {
	name := "you"
	wheel := false
	if current, err := user.Current(); err == nil {
		name = current.Username
		if current.Uid == "0" {
			w.warn("you are root; puffy works best as a normal user with doas",
				"create a user (adduser(8)) and run puffy as that user")
			return
		}
		if groups, err := current.GroupIds(); err == nil {
			for _, gid := range groups {
				if group, err := user.LookupGroupId(gid); err == nil &&
					group.Name == "wheel" {
					wheel = true
				}
			}
		}
	}
	if _, err := os.Stat("/etc/doas.conf"); err != nil {
		rule := "permit persist " + name
		if wheel {
			rule = "permit persist :wheel"
		}
		w.warn("no /etc/doas.conf: puffy's scripts cannot do root work yet",
			"as root: echo '"+rule+"' > /etc/doas.conf  (see doas.conf(5))")
		return
	}
	w.ok("doas is configured (/etc/doas.conf); root steps will ask for " +
		"your password")
}
