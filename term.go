package main

import (
	"os"
	"strings"
	"unicode/utf8"
)

// Terminal color, decided once per stream. Color is on only when the
// stream is a terminal, NO_COLOR is unset (https://no-color.org), and
// TERM is not "dumb". PUFFY_COLOR=always or never overrides that. Only
// the basic SGR codes are used, so the OpenBSD console (wscons, vt220),
// xterm and tmux all render them.
type Painter struct{ on bool }

const (
	sgrReset   = "\x1b[0m"
	sgrBold    = "\x1b[1m"
	sgrDim     = "\x1b[2m"
	sgrRed     = "\x1b[31m"
	sgrGreen   = "\x1b[32m"
	sgrYellow  = "\x1b[33m"
	sgrBlue    = "\x1b[34m"
	sgrMagenta = "\x1b[35m"
	sgrCyan    = "\x1b[36m"
)

func newPainter(isTerminal bool) Painter {
	switch strings.ToLower(os.Getenv("PUFFY_COLOR")) {
	case "always", "yes", "1":
		return Painter{true}
	case "never", "no", "0":
		return Painter{false}
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return Painter{false}
	}
	term := os.Getenv("TERM")
	return Painter{isTerminal && term != "" && term != "dumb"}
}

func (p Painter) paint(style, text string) string {
	if !p.on || text == "" {
		return text
	}
	return style + text + sgrReset
}

func (p Painter) action(text string) string  { return p.paint(sgrBold+sgrCyan, text) }
func (p Painter) comment(text string) string { return p.paint(sgrDim, text) }
func (p Painter) notice(text string) string  { return p.paint(sgrYellow, text) }
func (p Painter) good(text string) string    { return p.paint(sgrGreen, text) }
func (p Painter) bad(text string) string     { return p.paint(sgrBold+sgrRed, text) }
func (p Painter) label(text string) string   { return p.paint(sgrBlue, text) }
func (p Painter) prompt(text string) string  { return p.paint(sgrBold+sgrMagenta, text) }

// replyLine colors one line of a model reply by its role in the protocol.
func (p Painter) replyLine(line string) string {
	switch {
	case !p.on:
		return line
	case isEndLine(line):
		return p.comment(line)
	case actionLine.MatchString(strings.TrimRight(line, " \t")):
		return p.action(line)
	case strings.HasPrefix(line, "#"):
		return p.comment(line)
	}
	return line
}

// cleanControl removes C0 and C1 control characters other than newline
// and tab, and bytes that are not UTF-8, so model text cannot move the
// cursor, retitle the terminal or hide output with escape sequences.
func cleanControl(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == utf8.RuneError: // a stray byte like 0x9b, 8-bit CSI
			return -1
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		}
		return r
	}, text)
}

// lineWriter collects streamed text and prints whole lines, colored.
// flush prints a final partial line.
type lineWriter struct {
	painter Painter
	write   func(string)
	pending string
}

func (w *lineWriter) add(piece string) {
	w.pending += cleanControl(piece)
	for {
		cut := strings.IndexByte(w.pending, '\n')
		if cut < 0 {
			return
		}
		w.write(w.painter.replyLine(w.pending[:cut]) + "\n")
		w.pending = w.pending[cut+1:]
	}
}

func (w *lineWriter) flush() {
	if w.pending != "" {
		w.write(w.painter.replyLine(w.pending) + "\n")
		w.pending = ""
	}
}
