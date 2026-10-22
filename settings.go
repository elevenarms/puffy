package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Settings are read once from PUFFY_* environment variables.
type Settings struct {
	APIBase        string
	APIKey         string
	APIKeyFile     string
	Model          string
	MaxTokens      int
	ContextChars   int
	OutputChars    int
	KnowledgeBase  string
	VectorExt      string
	EmbedModel     string
	EmbedServer    string
	EmbedURL       string
	LogFolder      string
	ScriptTimeout  time.Duration
	ExtraSystem    string
	NoAsk          bool
	NoScripts      bool
	NoWrites       bool
	Quiet          bool
	Teach          bool
	Interactive    bool
	TerminalStdin  bool
	TerminalOutput bool
}

func loadSettings() Settings {
	loadConfigFile()
	home, _ := os.UserHomeDir()
	puffyHome := filepath.Join(home, ".puffy")
	share := "/usr/local/share/puffy"

	s := Settings{
		APIBase:      env("PUFFY_API_BASE", "https://api.trustedrouter.com/v1"),
		APIKeyFile:   env("PUFFY_API_KEY_FILE", filepath.Join(puffyHome, "key")),
		Model:        env("PUFFY_MODEL", "trustedrouter/e2e"),
		MaxTokens:    envInt("PUFFY_MAX_TOKENS", 4096),
		ContextChars: envInt("PUFFY_CONTEXT_CHARS", 300000),
		OutputChars:  envInt("PUFFY_OUTPUT_CHARS", 16000),
		KnowledgeBase: env("PUFFY_KB",
			firstExisting(filepath.Join(puffyHome, "puffy-kb.db"),
				filepath.Join(share, "puffy-kb.db"))),
		VectorExt: env("PUFFY_VECTOR_EXT",
			firstExisting(filepath.Join(puffyHome, "vector.so"),
				"/usr/local/lib/puffy/vector.so")),
		EmbedModel: env("PUFFY_EMBED_MODEL",
			firstExisting(filepath.Join(puffyHome,
				"nomic-embed-text-v1.5.Q8_0.gguf"),
				filepath.Join(share, "nomic-embed-text-v1.5.Q8_0.gguf"))),
		EmbedServer: env("PUFFY_EMBED_SERVER",
			firstExisting("/usr/local/libexec/puffy/llama-server",
				"llama-server")),
		EmbedURL:      env("PUFFY_EMBED_URL", ""),
		LogFolder:     env("PUFFY_LOG_FOLDER", filepath.Join(puffyHome, "logs")),
		ScriptTimeout: time.Duration(envInt("PUFFY_SCRIPT_TIMEOUT", 900)) * time.Second,
		ExtraSystem:   env("PUFFY_EXTRA_SYSTEM_TEXT", ""),
		NoAsk:         envBool("PUFFY_NO_ASK"),
		NoScripts:     envBool("PUFFY_NO_SCRIPTS"),
		NoWrites:      envBool("PUFFY_NO_WRITES"),
		Quiet:         envBool("PUFFY_QUIET"),
		Teach:         !envBool("PUFFY_NO_TEACH"),
	}
	s.APIKey = strings.TrimSpace(os.Getenv("PUFFY_API_KEY"))
	if s.APIKey == "" {
		if data, err := os.ReadFile(s.APIKeyFile); err == nil {
			s.APIKey = strings.TrimSpace(string(data))
		}
	}
	s.TerminalStdin = isTerminal(os.Stdin)
	s.TerminalOutput = isTerminal(os.Stdout)
	s.Interactive = s.TerminalStdin && canOpenTTY()
	return s
}

func env(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok && value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if value, err := strconv.Atoi(os.Getenv(name)); err == nil && value > 0 {
		return value
	}
	return fallback
}

func envBool(name string) bool {
	switch strings.ToLower(os.Getenv(name)) {
	case "1", "yes", "true", "on":
		return true
	}
	return false
}

// firstExisting returns the first path that exists, or the last one so the
// error message names the system-wide location.
func firstExisting(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return paths[len(paths)-1]
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func canOpenTTY() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	tty.Close()
	return true
}
