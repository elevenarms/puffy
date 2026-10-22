package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Offline mode. puffy degrades instead of failing when the model cannot be
// reached: no API key, no network, DNS failure, timeout. Search and the
// man pages are local, so it still answers from the documentation and
// lets the user retry the model later. HTTP errors from a reachable
// router (bad key, no credit) are not offline; they are reported.

// isNetworkError is true when the request never got an HTTP answer.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var dnsError *net.DNSError
	var opError *net.OpError
	var urlError *url.Error
	switch {
	case errors.As(err, &dnsError), errors.As(err, &opError):
		return true
	case errors.Is(err, context.DeadlineExceeded):
		return true
	case errors.As(err, &urlError):
		return urlError.Timeout() || isNetworkError(urlError.Err)
	}
	text := err.Error()
	return strings.Contains(text, "connection refused") ||
		strings.Contains(text, "network is unreachable") ||
		strings.Contains(text, "no route to host") ||
		strings.Contains(text, "TLS handshake timeout")
}

// offline answers task from the local knowledge base, then (on a
// terminal) keeps a small prompt open: a line searches, "man NAME(S)
// [heading]" reads a page, "retry" tries the model again, an empty line
// quits. It returns true when the user asked to retry.
func (a *Agent) offline(task, reason string) (retry bool) {
	a.say("\n%s\n", a.out.notice("puffy: offline ("+reason+"). Answering "+
		"from the local OpenBSD 8.0 manual pages and package catalog."))
	a.logf("=== offline: %s\n", reason)
	if a.kb == nil {
		a.say("%s\n", a.out.bad("puffy: the knowledge base is not available "+
			"either: "+a.kbError.Error()))
		return false
	}
	if task != "" {
		a.offlineSearch(task)
	}
	if a.tty == nil {
		return false
	}
	a.say("%s\n", a.out.comment(
		"Type words to search, \"man pf.conf(5) [heading]\" to read a page,\n"+
			"\"retry\" to try the model again, or an empty line to quit."))
	for {
		line := a.prompt("puffy offline> ")
		switch {
		case line == "":
			return false
		case line == "retry" || line == "online":
			return true
		case strings.HasPrefix(line, "man "):
			fields := strings.SplitN(strings.TrimSpace(line[4:]), " ", 2)
			heading := ""
			if len(fields) == 2 {
				heading = fields[1]
			}
			text := a.readMan(Action{Kind: "man", Argument: fields[0],
				Body: heading})
			a.say("%s\n", cleanControl(text))
		default:
			a.offlineSearch(line)
		}
	}
}

func (a *Agent) offlineSearch(query string) {
	hits, err := a.kb.Search(query, "", 5)
	if err != nil {
		a.say("%s\n", a.out.bad("search failed: "+err.Error()))
		return
	}
	a.logf("=== offline search %q\n%s\n", query, formatHits(hits, 700))
	if len(hits) == 0 {
		a.say("no matches\n")
		return
	}
	for _, hit := range hits {
		title := ""
		if hit.Title != "" {
			title = " " + a.out.comment("— "+hit.Title)
		}
		a.say("\n%s%s\n", a.out.action(hitLabel(hit)), title)
		body := stripLabelLine(hit)
		if len(body) > 700 {
			body = body[:700] + " …"
		}
		a.say("%s\n", indent(cleanControl(body), "  "))
	}
	if first := hits[0]; first.Kind == "man" {
		a.say("\n%s\n", a.out.comment(fmt.Sprintf(
			"Read more: man %s %s   (or here: man %s(%s) %s)",
			first.Section, first.Name, first.Name, first.Section,
			first.Heading)))
	}
}

// stripLabelLine drops the "name(section) HEADING" line chunks start with.
func stripLabelLine(hit Hit) string {
	first, rest, found := strings.Cut(hit.Body, "\n")
	if found && strings.HasPrefix(first, hit.Name+"(") {
		return rest
	}
	return hit.Body
}

func indent(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// forcedOffline is PUFFY_OFFLINE=1: never call the model.
func forcedOffline() bool { return envBool("PUFFY_OFFLINE") }
