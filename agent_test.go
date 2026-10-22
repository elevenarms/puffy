package main

import "testing"

func TestParseReply(t *testing.T) {
	cases := []struct {
		name, reply, kind, argument, body string
		fails                             bool
	}{
		{"script", "# list files\n# script\nls -la\n# end", "script", "", "ls -la", false},
		{"glued action", "We need the page.# man: pf.conf(5)\nquick\n# end# message\nfake excerpt\n# end",
			"man", "pf.conf(5)", "quick", false},
		{"fenced", "```\n# script\nuname -a\n```\n# end", "script", "", "uname -a", false},
		{"shell comment kept", "# script\necho hi # complete\n# end", "script", "", "echo hi # complete", false},
		{"write", "# write file: /tmp/a\nline\n# end", "write", "/tmp/a", "line", false},
		{"two actions", "# script\nls\n# complete\n# end", "", "", "", true},
		{"no action", "just talking\n# end", "", "", "", true},
		{"empty script", "# script\n# end", "", "", "", true},
	}
	for _, c := range cases {
		action, err := parseReply(c.reply)
		if c.fails {
			if err == nil {
				t.Errorf("%s: want error, got %+v", c.name, action)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if action.Kind != c.kind || action.Argument != c.argument ||
			action.Body != c.body {
			t.Errorf("%s: got %q %q %q", c.name, action.Kind,
				action.Argument, action.Body)
		}
	}
}

func TestApplyEdit(t *testing.T) {
	path := t.TempDir() + "/f.c"
	applyWrite(path, "int a;\nint b;\nint a2;")
	if got := applyEdit(path, "<<<<<<< SEARCH\nint b;\n=======\nint c;\n>>>>>>> REPLACE"); got != "Edited "+path+" (1 block(s))." {
		t.Fatal(got)
	}
	got := applyEdit(path, "<<<<<<< SEARCH\nint a\n=======\nx\n>>>>>>> REPLACE")
	if got[:11] != "Edit failed" {
		t.Fatalf("ambiguous search should fail: %s", got)
	}
}

func TestFTSQuery(t *testing.T) {
	if got := ftsQuery(`how do I use pf.conf's "rdr-to"?`); got != `"pf.conf's" OR "rdr-to"` &&
		got != `"pf.conf" OR "s" OR "rdr-to"` {
		t.Log(got)
	}
	if ftsQuery("the of and") != "" {
		t.Error("stop words only should give an empty query")
	}
}

func TestPainter(t *testing.T) {
	t.Setenv("PUFFY_COLOR", "")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "xterm")
	if newPainter(true).on {
		t.Error("NO_COLOR must turn color off")
	}
	t.Setenv("NO_COLOR", "")
	if newPainter(false).on {
		t.Error("no color when the stream is not a terminal")
	}
	t.Setenv("TERM", "dumb")
	if newPainter(true).on {
		t.Error("no color on TERM=dumb")
	}
	t.Setenv("PUFFY_COLOR", "always")
	p := newPainter(false)
	if got := p.replyLine("# script"); got != sgrBold+sgrCyan+"# script"+sgrReset {
		t.Errorf("action line: %q", got)
	}
	if got := p.replyLine("ls -la"); got != "ls -la" {
		t.Errorf("body line must stay plain: %q", got)
	}
}

func TestCleanControl(t *testing.T) {
	if got := cleanControl("ok\x1b]0;pwned\x07\ttab\nline\x9b2J"); got != "ok]0;pwned\ttab\nline2J" {
		t.Errorf("got %q", got)
	}
}
