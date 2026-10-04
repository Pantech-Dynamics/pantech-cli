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
		{{"Public IP", "102.211.122.80"}},
	}})
	want := "web-1  ● running\nvm_1\n\n  Plan       starter\n\n  Public IP  102.211.122.80\n"
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
