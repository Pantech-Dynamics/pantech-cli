package output

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func printer(human, color bool) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &Printer{Out: &out, Err: &errOut, human: human, color: color, errColor: color}, &out, &errOut
}

func TestTableAlignsColouredCells(t *testing.T) {
	p, out, _ := printer(true, true)
	p.Table([]string{"name", "state", "id"}, [][]string{
		{"web-1", p.State("running"), p.Dim("vm_1")},
		{"worker-staging", p.State("stopped"), p.Dim("vm_2")},
	})
	lines := strings.Split(strings.TrimRight(ansi.ReplaceAllString(out.String(), ""), "\n"), "\n")
	// In characters, not bytes: ● is three bytes and one column.
	column := func(line, sub string) int { return utf8.RuneCountInString(line[:strings.Index(line, sub)]) }
	col := column(lines[0], "ID")
	for _, l := range lines[1:] {
		if column(l, "vm_") != col {
			t.Fatalf("columns do not line up:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func TestPipedOutputIsPlain(t *testing.T) {
	p, out, _ := printer(false, false)
	p.Table([]string{"state"}, [][]string{{p.State("running")}})
	if got := out.String(); got != "STATE\nrunning\n" {
		t.Fatalf("piped table = %q: no symbols, no colour", got)
	}
}

func TestDetailLinesUpLabels(t *testing.T) {
	p, out, _ := printer(true, false)
	p.Print(Detail{Title: "web-1", State: "● running", Subtitle: "vm_1", Sections: [][]Pair{
		{{"Plan", "starter"}},
		{{"Public IP", "203.0.113.80"}},
	}})
	want := "web-1  ● running\nvm_1\n\n  Plan       starter\n\n  Public IP  203.0.113.80\n"
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
}

func TestErrorFormatting(t *testing.T) {
	p, _, errOut := printer(false, false)
	p.Error(errors.New("no VM named \"x\"\nSee: pantech vm list"))
	if got := errOut.String(); got != "✗ No VM named \"x\"\n  See: pantech vm list\n" {
		t.Fatalf("got %q", got)
	}
}

func TestTimes(t *testing.T) {
	now = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }
	defer func() { now = time.Now }()
	for in, want := range map[string]string{
		"2026-10-04T11:59:30Z": "just now",
		"2026-10-04T11:55:00Z": "5 min ago",
		"2026-10-03T12:00:00Z": "1 day ago",
		"2026-09-12T12:00:00Z": "22 days ago",
		"2027-01-02T12:00:00Z": "in 3 months",
	} {
		if got := Ago(&in); got != want {
			t.Errorf("Ago(%s) = %q, want %q", in, got, want)
		}
	}
	if Ago(nil) != "—" {
		t.Error("nil should be a dash")
	}
	bad := "yesterday"
	if Ago(&bad) != "yesterday" {
		t.Error("unparseable times come back as they were")
	}
}

func TestElapsed(t *testing.T) {
	for d, want := range map[time.Duration]string{4 * time.Second: "4s", 72 * time.Second: "1:12", 600 * time.Second: "10:00"} {
		if got := Elapsed(d); got != want {
			t.Errorf("Elapsed(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestCleanStripsControlCharacters(t *testing.T) {
	for in, want := range map[string]string{
		"web-1":                      "web-1",
		"a\x1b[2Jb":                  "a[2Jb",
		"x\x1b]8;;https://evil\x07y": "x]8;;https://evily",
		"tab\tnew\nline\rcr\x7f":     "tabnewlinecr",
		"c1\u009b31m\u0085":          "c131m",
		"bad\xffbyte":                "bad\ufffdbyte",
		"héllo · 日本":                 "héllo · 日本",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTableAndDetailCleanCellsButKeepOwnColour(t *testing.T) {
	p, out, _ := printer(true, true)
	p.Table([]string{"name", "state"}, [][]string{{"evil\x1b]0;title\x07\x1b[2J", p.State("running")}})
	p.Print(Detail{Title: "t\x1b[2J", Subtitle: "s\x07", Sections: [][]Pair{{{"Name", "v\x1b[1A\r"}}}})
	got := out.String()
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J", "\x1b[1A", "\r"} {
		if strings.Contains(got, bad) {
			t.Fatalf("output has %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "\x1b[32m● running\x1b[0m") {
		t.Fatalf("lost its own colour: %q", got)
	}

	// Piped, even an exact colour sequence from outside goes.
	p, out, _ = printer(false, false)
	p.Table([]string{"name"}, [][]string{{"red\x1b[31m"}})
	if got := out.String(); got != "NAME\nred[31m\n" {
		t.Fatalf("piped = %q", got)
	}
}

func TestMessagesCleanTheirArguments(t *testing.T) {
	p, _, errOut := printer(true, false)
	p.Success("Created %s.", "x\x1b[2J")
	p.Note("Saved to %s", "y\x07")
	p.Next("Then", "pantech \x1b[2J")
	p.Error(errors.New("bad\x1b]0;t\x07 name\nSee: z\x1b[1A"))
	if got := errOut.String(); strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("stderr %q", got)
	}
}
