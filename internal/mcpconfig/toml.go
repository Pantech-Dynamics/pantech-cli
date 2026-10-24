package mcpconfig

import (
	"bytes"
	"fmt"
	"strings"
)

// Codex keeps its servers in TOML. Rather than take on a TOML library, the
// CLI edits only the [mcp_servers.pantech] table, and its sub-tables, as
// text: every other byte of the file is left exactly as it was.

const tomlTable = "mcp_servers." + ServerName

// TOMLTable is the [mcp_servers.pantech] table for the hosted server at
// serverURL (checked by CheckURL) or the local command.
func TOMLTable(local bool, serverURL string) string {
	if local {
		return fmt.Sprintf("[%s]\ncommand = %q\nargs = [\"stdio\"]\n", tomlTable, Command)
	}
	return fmt.Sprintf("[%s]\nurl = \"%s\"\n", tomlTable, tomlEscape(serverURL))
}

func tomlEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// MergeTOML puts table in place of an existing [mcp_servers.pantech] (and
// its [mcp_servers.pantech.*] sub-tables), or appends it. A pantech server
// written some other way (an inline table or dotted keys) is an error
// rather than a guess.
func MergeTOML(existing []byte, table string) ([]byte, error) {
	lines := splitLines(existing)
	start, end := -1, -1
	open := false      // inside our table or one of its sub-tables
	inServers := false // inside a plain [mcp_servers] table
	multi := ""        // the open multi-line string's delimiter
	for i, line := range lines {
		text := strings.TrimSpace(string(line))
		if multi != "" {
			if strings.Count(text, multi)%2 == 1 {
				multi = ""
			}
			continue
		}
		if name, ok := tableHeader(text); ok {
			inServers = name == "mcp_servers"
			ours := name == tomlTable || strings.HasPrefix(name, tomlTable+".")
			switch {
			case ours && start < 0:
				start, open = i, true
			case ours && !open:
				return nil, fmt.Errorf("%w: [%s] is split across the file; edit it by hand", ErrInvalid, name)
			case !ours && open:
				end, open = i, false
			}
			continue
		}
		key := strings.TrimSpace(strings.SplitN(text, "=", 2)[0])
		if strings.Contains(text, "=") && !strings.HasPrefix(text, "#") &&
			(key == "mcp_servers" || strings.HasPrefix(key, tomlTable) || (inServers && (key == ServerName || strings.HasPrefix(key, ServerName+".")))) {
			return nil, fmt.Errorf("%w: the %s server is written as %q; remove it, or edit it by hand", ErrInvalid, ServerName, key)
		}
		for _, d := range []string{`"""`, `'''`} {
			if strings.Count(text, d)%2 == 1 {
				multi = d
				break
			}
		}
	}
	if open {
		end = len(lines)
	}
	// Blank lines and comments just before the next table belong to it.
	for end > start+1 {
		t := strings.TrimSpace(string(lines[end-1]))
		if t != "" && !strings.HasPrefix(t, "#") {
			break
		}
		end--
	}

	var out bytes.Buffer
	if start < 0 {
		out.Write(existing)
		if len(existing) > 0 {
			if !bytes.HasSuffix(existing, []byte("\n")) {
				out.WriteByte('\n')
			}
			out.WriteByte('\n')
		}
		out.WriteString(table)
		return out.Bytes(), nil
	}
	for _, l := range lines[:start] {
		out.Write(l)
	}
	out.WriteString(table)
	if end < len(lines) && strings.TrimSpace(string(lines[end])) != "" {
		out.WriteByte('\n')
	}
	for _, l := range lines[end:] {
		out.Write(l)
	}
	return out.Bytes(), nil
}

// HasTOML reports whether a Codex config already has a pantech server.
func HasTOML(data []byte) bool {
	for _, line := range splitLines(data) {
		text := strings.TrimSpace(string(line))
		if name, ok := tableHeader(text); ok && (name == tomlTable || strings.HasPrefix(name, tomlTable+".")) {
			return true
		}
		if strings.HasPrefix(text, tomlTable) && strings.Contains(text, "=") {
			return true
		}
	}
	return false
}

// splitLines splits after each "\n", keeping it, so joining gives the input.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i+1])
		b = b[i+1:]
	}
	return out
}

// tableHeader reads "[a.b]  # comment" as "a.b"; an array of tables
// ("[[a]]") counts as a header too, with its name. Quoted parts are
// unquoted so ["mcp_servers"."pantech"] matches.
func tableHeader(text string) (string, bool) {
	if !strings.HasPrefix(text, "[") {
		return "", false
	}
	if i := strings.Index(text, "#"); i >= 0 {
		text = strings.TrimSpace(text[:i])
	}
	text = strings.TrimPrefix(strings.TrimSuffix(text, "]"), "[")
	text = strings.TrimPrefix(strings.TrimSuffix(text, "]"), "[")
	parts := strings.Split(text, ".")
	for i, p := range parts {
		parts[i] = strings.Trim(strings.TrimSpace(p), `"'`)
	}
	return strings.Join(parts, "."), true
}
