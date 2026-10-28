package output

import (
	"strings"
	"unicode/utf8"
)

// Clean makes text from outside the CLI (names, tags, emails, messages a
// customer or a cloud account chose) safe to print in a terminal: every C0
// and C1 control character, DEL and escape sequence introducer is removed,
// and bytes that are not UTF-8 become U+FFFD, so the text cannot move the
// cursor, clear the screen, retitle the window or forge a hyperlink.
// --json output is never cleaned: it is the API's body, escaped by JSON.
func Clean(s string) string {
	if isClean(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteRune(utf8.RuneError)
		case isControl(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }

func isClean(s string) bool {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if (r == utf8.RuneError && size == 1) || isControl(r) {
			return false
		}
		i += size
	}
	return true
}

// cleanStyled is Clean for text that may carry the Printer's own styling:
// only the exact colour sequences paint writes survive, and only when
// colour is on. Everything else that could steer the terminal goes.
func (p *Printer) cleanStyled(s string, color bool) string {
	if !color || !strings.Contains(s, "\x1b") {
		return Clean(s)
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			b.WriteString(Clean(s))
			return b.String()
		}
		b.WriteString(Clean(s[:i]))
		rest := s[i:]
		kept := false
		for _, seq := range ownSequences {
			if strings.HasPrefix(rest, seq) {
				b.WriteString(seq)
				s = rest[len(seq):]
				kept = true
				break
			}
		}
		if !kept {
			// Not ours: drop the ESC; Clean removes nothing else of it, but
			// without its ESC the rest is inert text.
			s = rest[1:]
		}
	}
}

// ownSequences are the only escape sequences paint writes.
var ownSequences = []string{"\x1b[0m", "\x1b[" + bold + "m", "\x1b[" + dim + "m", "\x1b[" + red + "m", "\x1b[" + green + "m", "\x1b[" + yellow + "m"}

// cleanArgs cleans the string arguments of a message, leaving its format
// (the CLI's own text) alone.
func cleanArgs(a []any) []any {
	out := make([]any, len(a))
	for i, v := range a {
		switch s := v.(type) {
		case string:
			out[i] = Clean(s)
		case *string:
			if s != nil {
				out[i] = Clean(*s)
			} else {
				out[i] = v
			}
		default:
			out[i] = v
		}
	}
	return out
}
