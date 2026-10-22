// puffy — an OpenBSD system management, coding and teaching agent.
//
// puffy keeps ssa's loop: the model answers with exactly one plain-text
// action per reply (script, write, edit, search, man, question, message,
// complete), the user approves it, and the result goes back as the next
// turn. Inference goes to a TrustedRouter endpoint (confidential compute by
// default). OpenBSD documentation comes from a local SQLite knowledge base
// with FTS5 and sqlite-vector. Help is README.md.
package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	settings := loadSettings()
	words := os.Args[1:]

	if len(words) > 0 {
		switch words[0] {
		case "--search":
			os.Exit(runSearchCommand(settings, strings.Join(words[1:], " ")))
		case "--doctor":
			os.Exit(runDoctor(settings))
		case "--setup":
			os.Exit(runSetup(settings, false))
		case "--version":
			fmt.Println("puffy " + version)
			return
		case "-h", "--help":
			fmt.Print(usageText)
			return
		}
	}

	if needsOnboarding(settings) {
		if status := runSetup(settings, true); status != 0 {
			os.Exit(status)
		}
		settings = loadSettings()
		if len(words) == 0 {
			return
		}
	}

	agent, err := newAgent(settings)
	if err != nil {
		die(err.Error())
	}
	status := agent.run(strings.Join(words, " "))
	agent.close() // stops llama-server; os.Exit skips deferred calls
	os.Exit(status)
}

const version = "0.1.0"

const usageText = `usage: puffy [task ...]
       puffy --search query     search the OpenBSD knowledge base
       puffy --doctor           check the API key, knowledge base and embedder
       puffy --setup            first-run setup: key, model, checks
With no task on a terminal, puffy asks for one. Settings are PUFFY_*
environment variables; see README.md.
`

func die(message string) {
	fmt.Fprintln(os.Stderr, "puffy: "+message)
	os.Exit(1)
}
