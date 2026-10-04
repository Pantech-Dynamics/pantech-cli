package cli

import (
	"testing"

	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
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

// The flags for the team's own stacks work but stay out of every help.
func TestDevFlagsAreHiddenButKept(t *testing.T) {
	root := NewRoot()
	api := root.PersistentFlags().Lookup("api-url")
	if api == nil || !api.Hidden {
		t.Fatalf("--api-url should exist and be hidden: %+v", api)
	}
	login, _, err := root.Find([]string{"auth", "login"})
	if err != nil {
		t.Fatal(err)
	}
	console := login.Flags().Lookup("console-url")
	if console == nil || !console.Hidden {
		t.Fatalf("--console-url should exist and be hidden: %+v", console)
	}
}
