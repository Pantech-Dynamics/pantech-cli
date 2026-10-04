// Package output prints for people (tables, colour in a terminal) or for
// programs (--json: the API's own body).
package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

// Printer writes to a terminal or a pipe.
type Printer struct {
	Out, Err io.Writer
	JSON     bool // --json
	Quiet    bool // --quiet: ids only
	color    bool
}

// New prints to stdout/stderr, in colour only in a terminal without NO_COLOR.
func New(jsonOut, quiet bool) *Printer {
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(os.Stdout.Fd()))
	return &Printer{Out: os.Stdout, Err: os.Stderr, JSON: jsonOut, Quiet: quiet, color: color}
}

// Interactive reports whether stdin and stderr are a terminal, so it is
// fine to ask a question.
func Interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// RawJSON prints an API body, indented.
func (p *Printer) RawJSON(body []byte) {
	var buf bytes.Buffer
	if json.Indent(&buf, body, "", "  ") != nil {
		buf.Reset()
		buf.Write(body)
	}
	buf.WriteByte('\n')
	_, _ = p.Out.Write(buf.Bytes())
}

// Value prints any value as JSON.
func (p *Printer) Value(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	_, _ = fmt.Fprintln(p.Out, string(data))
}

// Table prints rows under a header, aligned.
func (p *Printer) Table(header []string, rows [][]string) {
	w := tabwriter.NewWriter(p.Out, 0, 0, 3, ' ', 0)
	heads := make([]string, len(header))
	for i, h := range header {
		heads[i] = p.Dim(strings.ToUpper(h))
	}
	fmt.Fprintln(w, strings.Join(heads, "\t"))
	for _, row := range rows {
		fmt.Fprintln(w, strings.Join(row, "\t"))
	}
	_ = w.Flush()
}

// Fields prints label/value pairs, aligned.
func (p *Printer) Fields(pairs [][2]string) {
	w := tabwriter.NewWriter(p.Out, 0, 0, 2, ' ', 0)
	for _, kv := range pairs {
		fmt.Fprintf(w, "%s\t%s\n", p.Dim(kv[0]), kv[1])
	}
	_ = w.Flush()
}

// Line prints a line to stdout.
func (p *Printer) Line(format string, a ...any) { fmt.Fprintf(p.Out, format+"\n", a...) }

// Note prints progress and hints to stderr, so stdout stays clean for pipes.
func (p *Printer) Note(format string, a ...any) { fmt.Fprintf(p.Err, format+"\n", a...) }

// Success is a done line, on stderr.
func (p *Printer) Success(format string, a ...any) {
	fmt.Fprintf(p.Err, "%s %s\n", p.paint("32", "✓"), fmt.Sprintf(format, a...))
}

func (p *Printer) Dim(s string) string  { return p.paint("2", s) }
func (p *Printer) Bold(s string) string { return p.paint("1", s) }

// State colours a resource state: running green, stopped dim, failed red, the rest yellow.
func (p *Printer) State(s string) string {
	switch s {
	case "running", "active", "succeeded", "provisioned", "present", "available":
		return p.paint("32", s)
	case "stopped", "absent", "deleted":
		return p.paint("2", s)
	case "failed", "error", "payment_failed":
		return p.paint("31", s)
	default:
		return p.paint("33", s)
	}
}

func (p *Printer) paint(code, s string) string {
	if !p.color || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Or returns *s, or dash when it is nil or empty.
func Or(s *string) string {
	if s == nil || *s == "" {
		return "—"
	}
	return *s
}
