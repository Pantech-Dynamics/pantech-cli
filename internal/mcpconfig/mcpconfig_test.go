package mcpconfig

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestEveryClientHasAPathOrPrintsSteps(t *testing.T) {
	e := Env{GOOS: "linux", Home: "/home/ada"}
	for _, c := range Clients {
		if got := c.Path(e); (got != "") != (c.Format != None) {
			t.Errorf("%s: Path = %q with format %v", c.ID, got, c.Format)
		}
		if !c.Remote {
			t.Errorf("%s: every client can use the hosted server", c.ID)
		}
	}
	if _, ok := Find("nope"); ok {
		t.Fatal("found an unknown client")
	}
}

func TestPaths(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	for _, tc := range []struct {
		id   string
		env  Env
		want string
	}{
		{"claude", Env{GOOS: "darwin", Home: "/Users/ada"}, "/Users/ada/Library/Application Support/Claude/claude_desktop_config.json"},
		{"claude", Env{GOOS: "linux", Home: "/home/ada"}, "/home/ada/.config/Claude/claude_desktop_config.json"},
		{"claude", Env{GOOS: "linux", Home: "/home/ada", Getenv: env(map[string]string{"XDG_CONFIG_HOME": "/xdg"})}, "/xdg/Claude/claude_desktop_config.json"},
		{"claude", Env{GOOS: "windows", Home: "/h", Getenv: env(map[string]string{"APPDATA": "/appdata"})}, "/appdata/Claude/claude_desktop_config.json"},
		{"vscode", Env{GOOS: "darwin", Home: "/Users/ada"}, "/Users/ada/Library/Application Support/Code/User/mcp.json"},
		{"vscode", Env{GOOS: "linux", Home: "/home/ada"}, "/home/ada/.config/Code/User/mcp.json"},
		{"vscode", Env{GOOS: "windows", Home: "/h"}, "/h/AppData/Roaming/Code/User/mcp.json"},
		{"cursor", Env{GOOS: "windows", Home: "/h"}, "/h/.cursor/mcp.json"},
		{"windsurf", Env{GOOS: "darwin", Home: "/h"}, "/h/.codeium/windsurf/mcp_config.json"},
		{"codex", Env{GOOS: "linux", Home: "/h"}, "/h/.codex/config.toml"},
		{"codex", Env{GOOS: "linux", Home: "/h", Getenv: env(map[string]string{"CODEX_HOME": "/ch"})}, "/ch/config.toml"},
		{"gemini", Env{GOOS: "linux", Home: "/h"}, "/h/.gemini/settings.json"},
		{"claude-code", Env{GOOS: "darwin", Home: "/h"}, "/h/.claude.json"},
		{"chatgpt", Env{GOOS: "darwin", Home: "/h"}, ""},
		{"grok", Env{GOOS: "darwin", Home: "/h"}, ""},
	} {
		c, _ := Find(tc.id)
		if got := filepath.ToSlash(c.Path(tc.env)); got != tc.want {
			t.Errorf("%s on %s: %q, want %q", tc.id, tc.env.GOOS, got, tc.want)
		}
	}
}

// The entry each app expects, hosted and local.
func TestEntries(t *testing.T) {
	local := map[string]any{"command": "pantech-mcp", "args": []any{"stdio"}}
	typedLocal := map[string]any{"type": "stdio", "command": "pantech-mcp", "args": []any{"stdio"}}
	for id, want := range map[string][2]map[string]any{
		"claude-code": {{"type": "http", "url": RemoteURL}, typedLocal},
		"claude":      {{"url": RemoteURL}, local},
		"cursor":      {{"url": RemoteURL}, local},
		"vscode":      {{"type": "http", "url": RemoteURL}, typedLocal},
		"windsurf":    {{"serverUrl": RemoteURL}, local},
		"gemini":      {{"httpUrl": RemoteURL}, local},
	} {
		c, _ := Find(id)
		if got := c.Entry(false, RemoteURL); !reflect.DeepEqual(got, want[0]) {
			t.Errorf("%s remote = %v, want %v", id, got, want[0])
		}
		if got := c.Entry(true, RemoteURL); !reflect.DeepEqual(got, want[1]) {
			t.Errorf("%s local = %v, want %v", id, got, want[1])
		}
	}
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, b)
	}
	return m
}

// Every JSON client: an empty file, other servers kept, our entry
// replaced, unknown keys kept, and a file it cannot read left alone.
func TestMergeJSONEveryClient(t *testing.T) {
	for _, c := range Clients {
		if c.Format != JSON {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			k := c.Key
			entry := c.Entry(false, RemoteURL)
			wantEntry := decode(t, mustJSON(t, entry))

			for name, tc := range map[string]struct {
				in   string
				keep map[string]any // top-level keys that must survive as they were
			}{
				"empty":        {in: ""},
				"whitespace":   {in: " \n"},
				"empty object": {in: "{}"},
				"null servers": {in: `{"` + k + `": null}`},
				"other servers": {
					in:   `{"theme":"dark","n":12345678901234567890,"` + k + `":{"github":{"url":"https://example.test/mcp?a=1&b=2"}}}`,
					keep: map[string]any{"theme": "dark", "n": 1.2345678901234567e19},
				},
				"pantech replaced": {in: `{"` + k + `":{"pantech":{"command":"old"},"other":{"command":"x"}}}`},
			} {
				out, err := MergeJSON([]byte(tc.in), k, entry)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				got := decode(t, out)
				servers := got[k].(map[string]any)
				if !reflect.DeepEqual(servers["pantech"], wantEntry["pantech"]) && !reflect.DeepEqual(servers["pantech"], wantEntry) {
					t.Errorf("%s: pantech = %v, want %v", name, servers["pantech"], wantEntry)
				}
				for key, v := range tc.keep {
					if !reflect.DeepEqual(got[key], v) {
						t.Errorf("%s: %s = %v, want %v", name, key, got[key], v)
					}
				}
				if strings.Contains(tc.in, "github") {
					if servers["github"] == nil || !strings.Contains(string(out), "a=1&b=2") || !strings.Contains(string(out), "12345678901234567890") {
						t.Errorf("%s: other server or number changed:\n%s", name, out)
					}
				}
				if strings.Contains(tc.in, `"other"`) && servers["other"] == nil {
					t.Errorf("%s: dropped another server:\n%s", name, out)
				}
			}

			for _, bad := range []string{`{"`, `[1]`, `// comment` + "\n{}", `{"` + k + `": []}`, `{} {}`} {
				if _, err := MergeJSON([]byte(bad), k, entry); !errors.Is(err, ErrInvalid) {
					t.Errorf("MergeJSON(%q) = %v, want ErrInvalid", bad, err)
				}
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMergeTOML(t *testing.T) {
	remote := TOMLTable(false, RemoteURL)
	local := TOMLTable(true, "")
	if local != "[mcp_servers.pantech]\ncommand = \"pantech-mcp\"\nargs = [\"stdio\"]\n" {
		t.Fatalf("local table = %q", local)
	}
	for name, tc := range map[string]struct{ in, table, want string }{
		"empty": {"", remote, remote},
		"no final newline": {
			"model = \"o3\"", remote,
			"model = \"o3\"\n\n" + remote,
		},
		"appended after other servers": {
			"# my config\r\nmodel = \"o3\"   # keep me\r\n\r\n[mcp_servers.github]\r\nurl = \"https://example.test\"\r\n",
			remote,
			"# my config\r\nmodel = \"o3\"   # keep me\r\n\r\n[mcp_servers.github]\r\nurl = \"https://example.test\"\r\n\n" + remote,
		},
		"replaced in the middle": {
			"model = \"o3\"\n\n[mcp_servers.pantech]\ncommand = \"old\"\n\n[mcp_servers.pantech.env]\nX = \"1\"\n\n# the next one\n[mcp_servers.other]\ncommand = \"x\"\n",
			remote,
			"model = \"o3\"\n\n" + remote + "\n# the next one\n[mcp_servers.other]\ncommand = \"x\"\n",
		},
		"replaced at the end": {
			"[profiles.fast]\nmodel = \"o4\"\n\n[mcp_servers.pantech]\nurl = \"https://old.test/mcp\"\n",
			local,
			"[profiles.fast]\nmodel = \"o4\"\n\n" + local,
		},
		"quoted header": {
			"[\"mcp_servers\".\"pantech\"]\nurl = \"x\"\n[other]\na = 1\n",
			remote,
			remote + "\n[other]\na = 1\n",
		},
		"header inside a multi-line string is not a header": {
			"note = \"\"\"\n[mcp_servers.pantech]\n\"\"\"\n", remote,
			"note = \"\"\"\n[mcp_servers.pantech]\n\"\"\"\n\n" + remote,
		},
	} {
		got, err := MergeTOML([]byte(tc.in), tc.table)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, tc.want)
		}
	}
	for _, bad := range []string{
		"[mcp_servers]\npantech = { url = \"x\" }\n",
		"mcp_servers.pantech.url = \"x\"\n",
		"mcp_servers = { pantech = { url = \"x\" } }\n",
		"[mcp_servers.pantech]\nurl = \"x\"\n[other]\n[mcp_servers.pantech.env]\nA = \"1\"\n",
	} {
		if _, err := MergeTOML([]byte(bad), remote); !errors.Is(err, ErrInvalid) {
			t.Errorf("MergeTOML(%q) = %v, want ErrInvalid", bad, err)
		}
	}
	if !HasTOML([]byte("[mcp_servers.pantech]\n")) || HasTOML([]byte("[mcp_servers.other]\n")) {
		t.Fatal("HasTOML")
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		RemoteURL:                          true,
		"http://127.0.0.1:8080/mcp":        true,
		"mcp.pantechdynamics.com":          false,
		"ftp://x/mcp":                      false,
		"https://x/\"quoted\"":             false,
		"https://":                         false,
		"https://x/mcp\n[mcp_servers.bad]": false,
	} {
		if err := CheckURL(raw); (err == nil) != ok {
			t.Errorf("CheckURL(%q) = %v", raw, err)
		}
	}
}

func TestWriteFileBacksUpAndIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dir", "mcp.json")
	backup, err := WriteFile(path, []byte("one"))
	if err != nil || backup != "" {
		t.Fatalf("first write: %q, %v", backup, err)
	}
	backup, err = WriteFile(path, []byte("two"))
	if err != nil || backup != path+".bak" {
		t.Fatalf("second write: %q, %v", backup, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "two" {
		t.Fatalf("file = %q", b)
	}
	if b, _ := os.ReadFile(backup); string(b) != "one" {
		t.Fatalf("backup = %q", b)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{path, backup} {
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v", p, fi.Mode().Perm())
			}
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatalf("left temporary files: %v", entries)
	}
}
