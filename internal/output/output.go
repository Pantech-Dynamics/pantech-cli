// Package output prints for people (aligned columns, state symbols, colour in
// a terminal) or for programs (--json: the API's own body; piped: plain text).
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// Printer writes results to Out and everything else (progress, hints,
// summaries) to Err, so stdout pipes cleanly.
type Printer struct {
	Out, Err io.Writer
	JSON     bool // --json
	Quiet    bool // --quiet: ids only

	// human: stdout is a terminal, so symbols and summaries are welcome.
	human bool
	// color: colour on stdout; errColor: colour on stderr.
	color, errColor bool
	// live: stderr is a terminal, so progress can redraw a line.
	live bool
}

// New prints to stdout and stderr, in colour only to a terminal and never with NO_COLOR.
func New(jsonOut, quiet bool) *Printer {
	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
	outTTY := term.IsTerminal(int(os.Stdout.Fd()))
	errTTY := term.IsTerminal(int(os.Stderr.Fd()))
	return &Printer{
		Out: os.Stdout, Err: os.Stderr, JSON: jsonOut, Quiet: quiet,
		human: outTTY, color: outTTY && !noColor, errColor: errTTY && !noColor, live: errTTY && os.Getenv("CI") == "",
	}
}

func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

func (p *Printer) RawJSON(body []byte) {
	var buf bytes.Buffer
	if json.Indent(&buf, body, "", "  ") != nil {
		buf.Reset()
		buf.Write(body)
	}
	buf.WriteByte('\n')
	_, _ = p.Out.Write(buf.Bytes())
}

func (p *Printer) Value(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	_, _ = fmt.Fprintln(p.Out, string(data))
}

func (p *Printer) Line(format string, a ...any) { fmt.Fprintf(p.Out, format+"\n", a...) }

func (p *Printer) Note(format string, a ...any) { fmt.Fprintf(p.Err, format+"\n", a...) }

func (p *Printer) Success(format string, a ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", paint(p.errColor, green, "✓"), fmt.Sprintf(format, a...))
}

func (p *Printer) Warn(format string, a ...any) {
	if p.Quiet || p.JSON {
		return
	}
	fmt.Fprintf(p.Err, "%s %s\n", paint(p.errColor, yellow, "!"), fmt.Sprintf(format, a...))
}

func (p *Printer) Next(label, command string) {
	if p.Quiet || p.JSON {
		return
	}
	fmt.Fprintf(p.Err, "%s %s\n", paint(p.errColor, dim, label+":"), command)
}

// Summary is a count under a list, on stderr, only for a person reading it.
func (p *Printer) Summary(parts ...string) {
	if !p.human {
		return
	}
	var kept []string
	for _, s := range parts {
		if s != "" {
			kept = append(kept, s)
		}
	}
	fmt.Fprintf(p.Err, "\n%s\n", paint(p.errColor, dim, strings.Join(kept, " · ")))
}

func (p *Printer) Dim(s string) string  { return paint(p.color, dim, s) }
func (p *Printer) Bold(s string) string { return paint(p.color, bold, s) }

// State is a resource state with its symbol: ● running, ○ stopped, ◐ on its
// way, ✗ failed. Piped, it is the plain word.
func (p *Printer) State(s string) string {
	if !p.human {
		return s
	}
	glyph, style := stateLook(s)
	return paint(p.color, style, glyph+" "+s)
}

func stateLook(s string) (string, string) {
	switch s {
	case "running", "active", "succeeded", "provisioned", "available":
		return "●", green
	case "stopped", "deleted", "unavailable":
		return "○", dim
	case "failed", "payment_failed", "error":
		return "✗", red
	case "unknown":
		return "?", dim
	default: // pending, provisioning, starting, stopping, deleting, submitting, awaiting_payment…
		return "◐", yellow
	}
}

// Table prints rows under a header in aligned columns, measured by what is
// visible, so colour does not throw the alignment. Piped, the header stays
// and symbols and colour go.
func (p *Printer) Table(header []string, rows [][]string) {
	all := append([][]string{header}, rows...)
	widths := make([]int, len(header))
	for _, row := range all {
		for i, cell := range row {
			if w := visibleWidth(cell); i < len(widths) && w > widths[i] {
				widths[i] = w
			}
		}
	}
	var b strings.Builder
	for r, row := range all {
		for i, cell := range row {
			if r == 0 {
				cell = paint(p.color, dim, strings.ToUpper(cell))
			}
			b.WriteString(cell)
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visibleWidth(cell)+3))
			}
		}
		b.WriteByte('\n')
	}
	_, _ = io.WriteString(p.Out, b.String())
}

type Pair [2]string

// Detail is one resource: a title line (name and state), a subtitle (its
// id), groups of label/value pairs, and what to run next.
type Detail struct {
	Title, State, Subtitle string
	Sections               [][]Pair
	Next                   [][2]string // label, command
}

func (p *Printer) Print(d Detail) {
	var b strings.Builder
	b.WriteString(p.Bold(d.Title))
	if d.State != "" {
		b.WriteString("  " + d.State)
	}
	b.WriteByte('\n')
	if d.Subtitle != "" {
		b.WriteString(p.Dim(d.Subtitle) + "\n")
	}
	width := 0
	for _, sec := range d.Sections {
		for _, pair := range sec {
			width = max(width, visibleWidth(pair[0]))
		}
	}
	for _, sec := range d.Sections {
		if len(sec) == 0 {
			continue
		}
		b.WriteByte('\n')
		for _, pair := range sec {
			fmt.Fprintf(&b, "  %s%s  %s\n", p.Dim(pair[0]), strings.Repeat(" ", width-visibleWidth(pair[0])), pair[1])
		}
	}
	_, _ = io.WriteString(p.Out, b.String())
	if len(d.Next) > 0 && !p.Quiet {
		fmt.Fprintln(p.Err)
		for _, n := range d.Next {
			p.Next(n[0], n[1])
		}
	}
}

// Error writes a failure for a person: ✗ and the first line, the rest under
// it, and parenthesised details (code, request id) and "See:" lines dimmed.
func (p *Printer) Error(err error) {
	lines := strings.Split(err.Error(), "\n")
	// Go errors start lower-case; a sentence on screen does not.
	if r, size := utf8.DecodeRuneInString(lines[0]); r != utf8.RuneError {
		lines[0] = strings.ToUpper(string(r)) + lines[0][size:]
	}
	fmt.Fprintf(p.Err, "%s %s\n", paint(p.errColor, red, "✗"), paint(p.errColor, bold, lines[0]))
	for _, l := range lines[1:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "(") || strings.HasPrefix(t, "See:") {
			l = paint(p.errColor, dim, l)
		}
		fmt.Fprintf(p.Err, "  %s\n", strings.TrimPrefix(l, "  "))
	}
}

func Or(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	return *s
}

const (
	bold   = "1"
	dim    = "2"
	red    = "31"
	green  = "32"
	yellow = "33"
)

func paint(on bool, code, s string) string {
	if !on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func visibleWidth(s string) int { return utf8.RuneCountInString(ansi.ReplaceAllString(s, "")) }
