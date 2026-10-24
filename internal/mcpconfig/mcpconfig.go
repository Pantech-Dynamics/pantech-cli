// Package mcpconfig adds the Pantech MCP server to an AI app's own config
// file: which file each app reads on each OS, the entry it expects, and a
// merge that keeps every other server and setting in the file.
//
// Nothing here runs commands or talks to the network; internal/cli decides
// what to do with the result.
package mcpconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// RemoteURL is the hosted MCP server (Streamable HTTP, OAuth sign-in).
const RemoteURL = "https://mcp.pantechdynamics.com/mcp"

// ServerName is the name the server is added under in every app.
const ServerName = "pantech"

// LocalCommand runs the server on this machine, as "pantech-mcp stdio".
const LocalCommand = "pantech-mcp"

// Command is the local command written into an app's config. The CLI sets
// it to the absolute path it found pantech-mcp at: desktop apps started from
// the dock or Start menu do not get the shell's PATH (~/go/bin, Homebrew),
// so a bare "pantech-mcp" would not start there.
var Command = LocalCommand

// Format is how a client's config file is written.
type Format int

const (
	// None: the app has no config file the CLI writes; it prints steps.
	None Format = iota
	JSON
	TOML
)

// Client is one AI app.
type Client struct {
	ID   string
	Name string
	// Remote and Local say which ways the app can use the server.
	Remote, Local bool
	// Format of the file Path returns; None when the app has no file for
	// the mode, or the CLI only prints steps for it.
	Format Format
	// Key is the JSON object servers live under: "mcpServers", or
	// "servers" for VS Code. For TOML it is the table prefix.
	Key string
}

// Clients is every app `pantech mcp install` knows, in the order they are
// listed.
var Clients = []Client{
	{ID: "claude-code", Name: "Claude Code", Remote: true, Local: true, Format: JSON, Key: "mcpServers"},
	{ID: "claude", Name: "Claude", Remote: true, Local: true, Format: JSON, Key: "mcpServers"},
	{ID: "cursor", Name: "Cursor", Remote: true, Local: true, Format: JSON, Key: "mcpServers"},
	{ID: "vscode", Name: "VS Code", Remote: true, Local: true, Format: JSON, Key: "servers"},
	{ID: "windsurf", Name: "Windsurf", Remote: true, Local: true, Format: JSON, Key: "mcpServers"},
	{ID: "codex", Name: "Codex", Remote: true, Local: true, Format: TOML, Key: "mcp_servers"},
	{ID: "gemini", Name: "Gemini CLI", Remote: true, Local: true, Format: JSON, Key: "mcpServers"},
	{ID: "chatgpt", Name: "ChatGPT", Remote: true},
	{ID: "grok", Name: "Grok", Remote: true},
}

// IDs are the client ids, for completion and help.
func IDs() []string {
	ids := make([]string, len(Clients))
	for i, c := range Clients {
		ids[i] = c.ID
	}
	return ids
}

// Find returns the client with id.
func Find(id string) (Client, bool) {
	for _, c := range Clients {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

// WritesFile reports whether installing for the mode writes a config file.
// Claude (the app) has a file only for the local server: the hosted one is
// added as a connector in its settings.
func (c Client) WritesFile(local bool) bool {
	if c.Format == None {
		return false
	}
	if c.ID == "claude" && !local {
		return false
	}
	return true
}

// Env is what Path reads from the environment, so tests can choose it.
type Env struct {
	GOOS string
	Home string
	// Getenv reads APPDATA, XDG_CONFIG_HOME and CODEX_HOME; nil reads nothing.
	Getenv func(string) string
}

func (e Env) get(k string) string {
	if e.Getenv == nil {
		return ""
	}
	return e.Getenv(k)
}

// Path is the config file the client reads, or "" when it has none:
//
//	claude-code  ~/.claude.json (user scope; used only when the claude command is not installed)
//	claude       macOS   ~/Library/Application Support/Claude/claude_desktop_config.json
//	             Windows %APPDATA%\Claude\claude_desktop_config.json
//	             Linux   $XDG_CONFIG_HOME/Claude/claude_desktop_config.json (~/.config)
//	cursor       ~/.cursor/mcp.json on every OS
//	vscode       macOS   ~/Library/Application Support/Code/User/mcp.json
//	             Windows %APPDATA%\Code\User\mcp.json
//	             Linux   $XDG_CONFIG_HOME/Code/User/mcp.json (~/.config)
//	windsurf     ~/.codeium/windsurf/mcp_config.json on every OS
//	codex        $CODEX_HOME/config.toml, else ~/.codex/config.toml
//	gemini       ~/.gemini/settings.json on every OS
func (c Client) Path(e Env) string {
	appConfig := func() string {
		switch e.GOOS {
		case "darwin":
			return filepath.Join(e.Home, "Library", "Application Support")
		case "windows":
			if d := e.get("APPDATA"); d != "" {
				return d
			}
			return filepath.Join(e.Home, "AppData", "Roaming")
		}
		if d := e.get("XDG_CONFIG_HOME"); d != "" {
			return d
		}
		return filepath.Join(e.Home, ".config")
	}
	switch c.ID {
	case "claude-code":
		return filepath.Join(e.Home, ".claude.json")
	case "claude":
		return filepath.Join(appConfig(), "Claude", "claude_desktop_config.json")
	case "cursor":
		return filepath.Join(e.Home, ".cursor", "mcp.json")
	case "vscode":
		return filepath.Join(appConfig(), "Code", "User", "mcp.json")
	case "windsurf":
		return filepath.Join(e.Home, ".codeium", "windsurf", "mcp_config.json")
	case "codex":
		if d := e.get("CODEX_HOME"); d != "" {
			return filepath.Join(d, "config.toml")
		}
		return filepath.Join(e.Home, ".codex", "config.toml")
	case "gemini":
		return filepath.Join(e.Home, ".gemini", "settings.json")
	}
	return ""
}

// CheckURL accepts an http(s) URL with a host, for --url.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || strings.ContainsAny(raw, "\"\\ \t\r\n") {
		return fmt.Errorf("%q is not a server URL: it must look like %s", raw, RemoteURL)
	}
	return nil
}

// Entry is the server's entry in the client's JSON config: the hosted
// server at serverURL, or the local command. Each app names the URL field
// its own way.
func (c Client) Entry(local bool, serverURL string) map[string]any {
	if local {
		e := map[string]any{"command": Command, "args": []any{"stdio"}}
		if c.ID == "vscode" || c.ID == "claude-code" {
			e["type"] = "stdio"
		}
		return e
	}
	switch c.ID {
	case "vscode", "claude-code":
		return map[string]any{"type": "http", "url": serverURL}
	case "windsurf":
		return map[string]any{"serverUrl": serverURL}
	case "gemini":
		return map[string]any{"httpUrl": serverURL}
	}
	return map[string]any{"url": serverURL}
}

// ErrInvalid is wrapped by a merge that cannot read the existing file.
var ErrInvalid = errors.New("invalid config file")

// MergeJSON sets <key>.pantech = entry in a JSON config and returns the new
// file. Every other key, and every other server, is kept as it was (numbers
// included); an existing pantech entry is replaced. Empty input starts a new
// file. Input that is not a JSON object (or uses comments) is an error.
func MergeJSON(existing []byte, key string, entry map[string]any) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if dec.More() {
			return nil, fmt.Errorf("%w: more than one JSON value", ErrInvalid)
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: not a JSON object", ErrInvalid)
		}
		doc = obj
	}
	servers := map[string]any{}
	if v, ok := doc[key]; ok && v != nil {
		s, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%w: %q is not an object", ErrInvalid, key)
		}
		servers = s
	}
	servers[ServerName] = entry
	doc[key] = servers
	return encode(doc)
}

func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Snippet is the JSON an app's file would hold for the server alone, for
// --print.
func Snippet(key string, entry map[string]any) string {
	out, _ := encode(map[string]any{key: map[string]any{ServerName: entry}})
	return string(out)
}

// HasJSON reports whether a JSON config already has a pantech server.
func HasJSON(data []byte, key string) bool {
	var doc map[string]any
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	servers, _ := doc[key].(map[string]any)
	_, ok := servers[ServerName]
	return ok
}

// WriteFile writes data to path atomically, readable only by the user. An
// existing file is first copied to path + ".bak". It returns the backup's
// path, or "" when there was nothing to back up.
func WriteFile(path string, data []byte) (backup string, err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if old, err := os.ReadFile(path); err == nil {
		backup = path + ".bak"
		if err := writeAtomic(backup, old); err != nil {
			return "", fmt.Errorf("backing up %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return backup, writeAtomic(path, data)
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
