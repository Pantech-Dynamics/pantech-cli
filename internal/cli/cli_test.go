package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

func TestMoney(t *testing.T) {
	for minor, want := range map[int64]string{
		0:          "NGN 0.00",
		5:          "NGN 0.05",
		150000:     "NGN 1,500.00",
		1700000:    "NGN 17,000.00",
		1234567890: "NGN 12,345,678.90",
	} {
		if got := money(minor, "NGN"); got != want {
			t.Errorf("money(%d) = %q, want %q", minor, got, want)
		}
	}
}

func TestDefaultUser(t *testing.T) {
	s := func(v string) *string { return &v }
	for image, want := range map[*string]string{
		s("ubuntu-24-04"): "ubuntu",
		s("debian-12"):    "debian",
		s("rocky-9"):      "rocky",
		s("windows-2022"): "root",
		nil:               "root",
	} {
		if got := defaultUser(image); got != want {
			t.Errorf("defaultUser(%v) = %q, want %q", image, got, want)
		}
	}
}

func TestKeyFileWithoutKeychain(t *testing.T) {
	t.Setenv("PANTECH_CONFIG_DIR", t.TempDir())
	t.Setenv("PANTECH_NO_KEYRING", "1")
	where, err := config.SaveKey("work", "PAN_one")
	if err != nil || where != config.InFile {
		t.Fatalf("SaveKey = %q, %v", where, err)
	}
	if _, err := config.SaveKey("home", "PAN_two"); err != nil {
		t.Fatal(err)
	}
	if k, _ := config.LoadKey("work"); k != "PAN_one" {
		t.Fatalf("work key = %q", k)
	}
	if err := config.DeleteKey("work"); err != nil {
		t.Fatal(err)
	}
	if k, _ := config.LoadKey("work"); k != "" {
		t.Fatalf("deleted key still there: %q", k)
	}
	if k, _ := config.LoadKey("home"); k != "PAN_two" {
		t.Fatalf("deleting one profile's key touched another's: %q", k)
	}
}

func TestProductionEndpointsCannotBeOverridden(t *testing.T) {
	t.Setenv("PANTECH_API_URL", "https://api.example.test")
	t.Setenv("PANTECH_CONSOLE_URL", "https://localhost:3000")
	a := &app{cfg: &config.Config{Profiles: map[string]config.Profile{}}}
	if got := a.baseURL(); got != "https://api.pantechdynamics.com" {
		t.Fatalf("baseURL = %q, want production API", got)
	}
	if DefaultConsoleURL != "https://console.pantechdynamics.com" {
		t.Fatalf("DefaultConsoleURL = %q, want production console", DefaultConsoleURL)
	}

	root, _ := NewRoot()
	if flag := root.PersistentFlags().Lookup("api-url"); flag != nil {
		t.Fatalf("--api-url should not exist: %+v", flag)
	}
	login, _, err := root.Find([]string{"auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	if flag := login.Flags().Lookup("console-url"); flag != nil {
		t.Fatalf("--console-url should not exist: %+v", flag)
	}
}

// A profile signed in on staging by v0.1.2 or older holds a key the
// production API refuses as invalid; say why instead of sending it.
func TestProfileSignedInOnAnotherAPI(t *testing.T) {
	f, srv := newFakeAPI(t)
	t.Setenv("PANTECH_CONFIG_DIR", t.TempDir())
	t.Setenv("PANTECH_NO_KEYRING", "1")
	if _, err := config.SaveKey("default", "PAN_staging"); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	a := &app{
		apiURL: srv.URL,
		cfg:    &config.Config{Profiles: map[string]config.Profile{"default": {APIURL: "https://api-dev.example.test"}}},
		out:    &output.Printer{Out: &out, Err: &errOut},
	}
	root := newRoot(a)
	root.SetArgs([]string{"auth", "status"})
	err := root.Execute()
	if !IsNotSignedIn(err) || !strings.Contains(err.Error(), "signed in on https://api-dev.example.test") || !strings.Contains(err.Error(), "pantech auth login") {
		t.Fatalf("err = %v", err)
	}
	if len(f.sent("GET", "/me")) != 0 {
		t.Fatal("sent the other API's key to this one")
	}
}
