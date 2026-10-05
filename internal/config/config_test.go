package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// isolate points the config at a fresh directory and keeps keys out of the keychain.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PANTECH_CONFIG_DIR", dir)
	t.Setenv("PANTECH_NO_KEYRING", "1")
	return dir
}

func TestDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for name, c := range map[string]struct {
		configDir, xdg string
		want           string
	}{
		"PANTECH_CONFIG_DIR wins": {"/tmp/p", "/tmp/x", "/tmp/p"},
		"XDG_CONFIG_HOME next":    {"", "/tmp/x", filepath.Join("/tmp/x", "pantech")},
		"else ~/.config/pantech":  {"", "", filepath.Join(home, ".config", "pantech")},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("PANTECH_CONFIG_DIR", c.configDir)
			t.Setenv("XDG_CONFIG_HOME", c.xdg)
			got, err := Dir()
			if err != nil || got != c.want {
				t.Fatalf("Dir() = %q, %v; want %q", got, err, c.want)
			}
		})
	}
}

func TestLoadWithoutAFileIsEmpty(t *testing.T) {
	isolate(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Current != "" || cfg.Profiles == nil || len(cfg.Profiles) != 0 {
		t.Fatalf("cfg = %+v, want empty with a usable map", cfg)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := isolate(t)
	cfg := &Config{Current: "cfgtest-work", Profiles: map[string]Profile{
		"cfgtest-work": {OrganizationID: "org_1", OrganizationName: "Acme", KeyID: "key_1", Scopes: []string{"write"}, ExpiresAt: "2027-01-01T00:00:00Z"},
		"home":         {OrganizationID: "org_2"},
	}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("config.json mode %o, want 600", mode)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("loaded %+v, want %+v", got, cfg)
	}
	// No temporary file is left behind by the atomic write.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("left %s behind", e.Name())
		}
	}
}

func TestLoadRejectsBrokenJSON(t *testing.T) {
	dir := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("err = %v, want a not-valid-JSON error", err)
	}
}

func TestLoadFillsAMissingProfilesMap(t *testing.T) {
	dir := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"current":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || cfg.Profiles == nil {
		t.Fatalf("cfg = %+v, %v", cfg, err)
	}
}

func TestName(t *testing.T) {
	for name, c := range map[string]struct {
		current, asked, want string
	}{
		"asked wins":      {"cfgtest-work", "home", "home"},
		"then current":    {"cfgtest-work", "", "cfgtest-work"},
		"then default":    {"", "", DefaultProfile},
		"asked, no state": {"", "ci", "ci"},
	} {
		cfg := &Config{Current: c.current}
		if got := cfg.Name(c.asked); got != c.want {
			t.Errorf("%s: Name(%q) = %q, want %q", name, c.asked, got, c.want)
		}
	}
}

func TestNamesAreSorted(t *testing.T) {
	cfg := &Config{Profiles: map[string]Profile{"zeta": {}, "alpha": {}, "mid": {}}}
	if got := cfg.Names(); !reflect.DeepEqual(got, []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("Names() = %v", got)
	}
}

func TestKeysInTheFile(t *testing.T) {
	dir := isolate(t)
	if k, err := LoadKey("cfgtest-work"); err != nil || k != "" {
		t.Fatalf("no key yet: got %q, %v", k, err)
	}
	where, err := SaveKey("cfgtest-work", "PAN_one")
	if err != nil || where != InFile {
		t.Fatalf("SaveKey = %q, %v", where, err)
	}
	info, err := os.Stat(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("credentials.json mode %o, want 600", mode)
	}
	if _, err := SaveKey("cfgtest-work", "PAN_two"); err != nil {
		t.Fatal(err)
	}
	if k, _ := LoadKey("cfgtest-work"); k != "PAN_two" {
		t.Fatalf("a second save must replace the key: %q", k)
	}
	if err := DeleteKey("cfgtest-work"); err != nil {
		t.Fatal(err)
	}
	// The last key gone, the file goes too.
	if _, err := os.Stat(filepath.Join(dir, "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials.json still there: %v", err)
	}
	if err := DeleteKey("cfgtest-never-saved"); err != nil {
		t.Fatalf("deleting a missing key: %v", err)
	}
}
