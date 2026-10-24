package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// fakeTools makes the named programs look installed and records what is
// run; HOME is a fresh directory, so no real AI app's config is touched.
func fakeTools(t *testing.T, installed ...string) (home string, ran *[][]string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	oldLook, oldRun := lookPath, runTool
	t.Cleanup(func() { lookPath, runTool = oldLook, oldRun })
	lookPath = func(name string) (string, error) {
		for _, n := range installed {
			if n == name {
				return "/fake/bin/" + name, nil
			}
		}
		return "", exec.ErrNotFound
	}
	ran = &[][]string{}
	runTool = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*ran = append(*ran, append([]string{name}, args...))
		if strings.HasSuffix(name, "pantech-mcp") {
			return []byte("pantech-mcp v0.1.0\n"), nil
		}
		return nil, nil
	}
	return home, ran
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, b)
	}
	return m
}

func TestMCPInstallWritesEachClientsFile(t *testing.T) {
	_, srv := newFakeAPI(t)
	for _, tc := range []struct {
		args       []string
		file, key  string
		wantPantch map[string]any
	}{
		{[]string{"cursor"}, ".cursor/mcp.json", "mcpServers", map[string]any{"url": "https://mcp.pantechdynamics.com/mcp"}},
		{[]string{"cursor", "--local"}, ".cursor/mcp.json", "mcpServers", map[string]any{"command": "/fake/bin/pantech-mcp", "args": []any{"stdio"}}},
		{[]string{"windsurf"}, ".codeium/windsurf/mcp_config.json", "mcpServers", map[string]any{"serverUrl": "https://mcp.pantechdynamics.com/mcp"}},
		{[]string{"gemini"}, ".gemini/settings.json", "mcpServers", map[string]any{"httpUrl": "https://mcp.pantechdynamics.com/mcp"}},
		{[]string{"vscode", "--url", "https://mcp-dev.pantechdynamics.com/mcp"}, vscodeFile(), "servers", map[string]any{"type": "http", "url": "https://mcp-dev.pantechdynamics.com/mcp"}},
		{[]string{"claude", "--local"}, claudeFile(), "mcpServers", map[string]any{"command": "/fake/bin/pantech-mcp", "args": []any{"stdio"}}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			home, _ := fakeTools(t, "pantech-mcp")
			path := filepath.Join(home, tc.file)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			before := `{"keep":true,"` + tc.key + `":{"github":{"url":"https://example.test"},"pantech":{"url":"old"}}}`
			if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
				t.Fatal(err)
			}
			r := run(t, srv, "", append([]string{"mcp", "install"}, tc.args...)...)
			if r.err != nil {
				t.Fatalf("%v\n%s", r.err, r.stderr)
			}
			got := readJSON(t, path)
			servers := got[tc.key].(map[string]any)
			if got["keep"] != true || servers["github"] == nil {
				t.Fatalf("dropped other settings: %v", got)
			}
			if b, _ := json.Marshal(servers["pantech"]); !jsonEqual(t, b, tc.wantPantch) {
				t.Fatalf("pantech = %s, want %v", b, tc.wantPantch)
			}
			if b, _ := os.ReadFile(path + ".bak"); string(b) != before {
				t.Fatalf("backup = %q", b)
			}
			if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
				t.Fatalf("mode %v", fi.Mode().Perm())
			}
			if !strings.Contains(r.stderr, "Added Pantech") {
				t.Fatalf("stderr: %s", r.stderr)
			}
		})
	}
}

func vscodeFile() string {
	if isDarwin() {
		return "Library/Application Support/Code/User/mcp.json"
	}
	return ".config/Code/User/mcp.json"
}

func claudeFile() string {
	if isDarwin() {
		return "Library/Application Support/Claude/claude_desktop_config.json"
	}
	return ".config/Claude/claude_desktop_config.json"
}

func isDarwin() bool { env, _ := mcpEnv(); return env.GOOS == "darwin" }

func jsonEqual(t *testing.T, got []byte, want any) bool {
	t.Helper()
	w, _ := json.Marshal(want)
	var a, b any
	_ = json.Unmarshal(got, &a)
	_ = json.Unmarshal(w, &b)
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func TestMCPInstallCodexKeepsTheRestOfTheFile(t *testing.T) {
	_, srv := newFakeAPI(t)
	home, _ := fakeTools(t)
	path := filepath.Join(home, ".codex", "config.toml")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	before := "model = \"o3\"  # mine\n\n[mcp_servers.github]\ncommand = \"gh-mcp\"\n"
	_ = os.WriteFile(path, []byte(before), 0o600)
	r := run(t, srv, "", "mcp", "install", "codex")
	if r.err != nil {
		t.Fatal(r.err)
	}
	b, _ := os.ReadFile(path)
	want := before + "\n[mcp_servers.pantech]\nurl = \"https://mcp.pantechdynamics.com/mcp\"\n"
	if string(b) != want {
		t.Fatalf("config.toml = %q\nwant %q", b, want)
	}
	if !strings.Contains(r.stderr, "codex mcp login pantech") {
		t.Fatalf("no login step: %s", r.stderr)
	}
}

func TestMCPInstallLeavesAnUnreadableFileAlone(t *testing.T) {
	_, srv := newFakeAPI(t)
	home, _ := fakeTools(t)
	path := filepath.Join(home, ".cursor", "mcp.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(`{"mcpServers": {`), 0o600)
	r := run(t, srv, "", "mcp", "install", "cursor")
	if r.err == nil || !strings.Contains(r.err.Error(), "left as it was") || !strings.Contains(r.err.Error(), "--print") {
		t.Fatalf("err = %v", r.err)
	}
	if b, _ := os.ReadFile(path); string(b) != `{"mcpServers": {` {
		t.Fatalf("changed: %q", b)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote a backup")
	}
}

func TestMCPInstallPrintChangesNothing(t *testing.T) {
	_, srv := newFakeAPI(t)
	home, _ := fakeTools(t, "claude")
	for _, client := range []string{"cursor", "vscode", "windsurf", "codex", "gemini", "claude-code"} {
		r := run(t, srv, "", "mcp", "install", client, "--print")
		if r.err != nil || !strings.Contains(r.stdout, "https://mcp.pantechdynamics.com/mcp") {
			t.Fatalf("%s: %v\n%s", client, r.err, r.stdout)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("--print wrote %v", entries)
	}
	r := run(t, srv, "", "mcp", "install", "cursor", "--print")
	if !strings.Contains(r.stdout, "cursor://anysphere.cursor-deeplink/mcp/install?name=pantech&config=") {
		t.Fatalf("no Cursor link: %s", r.stdout)
	}
}

func TestMCPInstallUsesTheAppsOwnCLI(t *testing.T) {
	_, srv := newFakeAPI(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"claude-code"}, "claude mcp add --transport http --scope user pantech https://mcp.pantechdynamics.com/mcp"},
		{[]string{"claude-code", "--local"}, "claude mcp add --scope user pantech -- /fake/bin/pantech-mcp stdio"},
		{[]string{"gemini"}, "gemini mcp add --scope user --transport http pantech https://mcp.pantechdynamics.com/mcp"},
	} {
		home, ran := fakeTools(t, "claude", "gemini", "pantech-mcp")
		r := run(t, srv, "", append([]string{"mcp", "install"}, tc.args...)...)
		if r.err != nil {
			t.Fatal(r.err)
		}
		if len(*ran) != 2 || strings.Join((*ran)[1], " ") != tc.want || (*ran)[0][2] != "remove" {
			t.Fatalf("%v ran %v", tc.args, *ran)
		}
		if entries, _ := os.ReadDir(home); len(entries) != 0 {
			t.Fatalf("also wrote %v", entries)
		}
	}
}

// Without the claude command, and with no Claude Code state to edit, it
// prints the command rather than create Claude Code's file.
func TestMCPInstallClaudeCodeWithoutClaude(t *testing.T) {
	_, srv := newFakeAPI(t)
	home, _ := fakeTools(t)
	r := run(t, srv, "", "mcp", "install", "claude-code")
	if r.err != nil || !strings.Contains(r.stdout, "claude mcp add") {
		t.Fatalf("%v\n%s", r.err, r.stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err == nil {
		t.Fatal("created ~/.claude.json")
	}
	_ = os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups":3}`), 0o600)
	if r := run(t, srv, "", "mcp", "install", "claude-code"); r.err != nil {
		t.Fatal(r.err)
	}
	got := readJSON(t, filepath.Join(home, ".claude.json"))
	if got["numStartups"] != 3.0 || got["mcpServers"].(map[string]any)["pantech"] == nil {
		t.Fatalf("~/.claude.json = %v", got)
	}
}

func TestMCPInstallPrintsStepsForAppsWithoutAFile(t *testing.T) {
	_, srv := newFakeAPI(t)
	home, _ := fakeTools(t)
	for client, want := range map[string]string{
		"chatgpt": "developer mode",
		"grok":    "Bearer $PANTECH_API_KEY",
		"claude":  "Add custom connector",
	} {
		r := run(t, srv, "", "mcp", "install", client)
		if r.err != nil || !strings.Contains(r.stdout, want) || !strings.Contains(r.stdout, "https://mcp.pantechdynamics.com/mcp") {
			t.Fatalf("%s: %v\n%s", client, r.err, r.stdout)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("wrote %v", entries)
	}
}

func TestMCPInstallMistakes(t *testing.T) {
	_, srv := newFakeAPI(t)
	fakeTools(t)
	for _, args := range [][]string{
		{"nope"},
		{"chatgpt", "--local"},
		{"grok", "--local"},
		{"cursor", "--local", "--url", "https://x.test/mcp"},
		{"cursor", "--url", "mcp.example.test"},
		{},
	} {
		r := run(t, srv, "", append([]string{"mcp", "install"}, args...)...)
		if r.err == nil || !IsUsage(r.err) {
			t.Errorf("%v: err = %v", args, r.err)
		}
	}
}

func TestMCPInstallLocalWarnsWithoutPantechMCP(t *testing.T) {
	_, srv := newFakeAPI(t)
	fakeTools(t)
	r := run(t, srv, "", "mcp", "install", "cursor", "--local")
	if r.err != nil || !strings.Contains(r.stderr, "pantech-mcp is not on your PATH") || !strings.Contains(r.stderr, "go install github.com/Pantech-Dynamics/pantech-mcp/cmd/pantech-mcp@latest") {
		t.Fatalf("%v\n%s", r.err, r.stderr)
	}
}

func TestMCPStatusAndListClients(t *testing.T) {
	_, srv := newFakeAPI(t)
	home, _ := fakeTools(t, "pantech-mcp")
	_ = os.MkdirAll(filepath.Join(home, ".cursor"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte(`{"mcpServers":{"pantech":{"url":"x"}}}`), 0o600)

	r := run(t, srv, "", "--json", "mcp", "status")
	if r.err != nil {
		t.Fatal(r.err)
	}
	var s mcpStatus
	if err := json.Unmarshal([]byte(r.stdout), &s); err != nil {
		t.Fatalf("%v\n%s", err, r.stdout)
	}
	if s.HostedURL != "https://mcp.pantechdynamics.com/mcp" || !s.Local.Installed || s.Local.Version != "pantech-mcp v0.1.0" {
		t.Fatalf("status = %+v", s)
	}
	for _, c := range s.Clients {
		if c.Configured != (c.ID == "cursor") {
			t.Errorf("%s configured = %v", c.ID, c.Configured)
		}
	}
	if r := run(t, srv, "", "-q", "mcp", "status"); strings.TrimSpace(r.stdout) != "cursor" {
		t.Fatalf("quiet status = %q", r.stdout)
	}
	if r := run(t, srv, "", "mcp", "status"); !strings.Contains(r.stdout, "mcp.pantechdynamics.com") || !strings.Contains(r.stdout, "cursor") {
		t.Fatalf("status:\n%s", r.stdout)
	}

	r = run(t, srv, "", "-q", "mcp", "list-clients")
	if got := strings.Fields(r.stdout); strings.Join(got, ",") != "claude-code,claude,cursor,vscode,windsurf,codex,gemini,chatgpt,grok" {
		t.Fatalf("list-clients = %v", got)
	}
}

func TestMCPInstallCompletesClientNames(t *testing.T) {
	root := newRoot(&app{})
	install, _, err := root.Find([]string{"mcp", "install"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(install.ValidArgs, ",") != "claude-code,claude,cursor,vscode,windsurf,codex,gemini,chatgpt,grok" {
		t.Fatalf("ValidArgs = %v", install.ValidArgs)
	}
}

// logout runs "auth logout" for a profile signed in with PAN_test.
func logout(t *testing.T, srvURL string, profile config.Profile, args ...string) (result, *config.Config) {
	t.Helper()
	t.Setenv("PANTECH_CONFIG_DIR", t.TempDir())
	t.Setenv("PANTECH_NO_KEYRING", "1")
	t.Setenv("PANTECH_API_KEY", "")
	if _, err := config.SaveKey("default", "PAN_test"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	cfg := &config.Config{Current: "default", Profiles: map[string]config.Profile{"default": profile}}
	a := &app{apiURL: srvURL, cfg: cfg, out: &output.Printer{Out: &out, Err: &errOut}}
	root := newRoot(a)
	root.SetArgs(append([]string{"auth", "logout"}, args...))
	root.SetOut(&errOut)
	root.SetErr(&errOut)
	err := root.Execute()
	return result{out.String(), errOut.String(), err}, cfg
}

func TestLogoutRevokesTheKey(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /me", 204, "")
	r, cfg := logout(t, srv.URL, config.Profile{KeyID: "key_1"})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/me")) != 1 {
		t.Fatalf("DELETE /me sent %d times", len(f.sent("DELETE", "/me")))
	}
	if !strings.Contains(r.stderr, "key_1) is revoked") || strings.Contains(r.stderr, "still works") {
		t.Fatalf("stderr: %s", r.stderr)
	}
	if k, _ := config.LoadKey("default"); k != "" || len(cfg.Profiles) != 0 {
		t.Fatal("the key or profile is still stored")
	}
}

func TestLogoutWithAnOlderAPIOrAGoneKey(t *testing.T) {
	for status, wantStillWorks := range map[int]bool{404: true, 405: true, 401: false} {
		f, srv := newFakeAPI(t)
		f.on("DELETE /me", status, `{"status":0}`)
		r, _ := logout(t, srv.URL, config.Profile{KeyID: "key_1"})
		if r.err != nil {
			t.Fatalf("%d: %v", status, r.err)
		}
		if len(f.sent("DELETE", "/me")) != 1 {
			t.Fatalf("%d: retried", status)
		}
		if strings.Contains(r.stderr, "still works") != wantStillWorks || strings.Contains(r.stderr, "Could not") || strings.Contains(r.stderr, "revoked") {
			t.Fatalf("%d: stderr: %s", status, r.stderr)
		}
	}
}

func TestLogoutErrorIsAWarningNotAFailure(t *testing.T) {
	f, srv := newFakeAPI(t)
	f.on("DELETE /me", 503, "")
	r, _ := logout(t, srv.URL, config.Profile{KeyID: "key_1"})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(f.sent("DELETE", "/me")) != 1 || !strings.Contains(r.stderr, "Could not revoke") || !strings.Contains(r.stderr, "still works") {
		t.Fatalf("calls %d, stderr: %s", len(f.sent("DELETE", "/me")), r.stderr)
	}
}

func TestLogoutKeepKey(t *testing.T) {
	f, srv := newFakeAPI(t)
	r, _ := logout(t, srv.URL, config.Profile{KeyID: "key_1"}, "--keep-key")
	if r.err != nil || len(f.sent("DELETE", "/me")) != 0 || !strings.Contains(r.stderr, "still works") {
		t.Fatalf("%v, stderr: %s", r.err, r.stderr)
	}
	// A key from another API is never sent to this one.
	r, _ = logout(t, srv.URL, config.Profile{KeyID: "key_1", APIURL: "https://api-dev.example.test"})
	if r.err != nil || len(f.sent("DELETE", "/me")) != 0 {
		t.Fatalf("%v: sent another API's key", r.err)
	}
}
