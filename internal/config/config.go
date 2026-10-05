// Package config keeps the CLI's profiles (which organization)
// in ~/.config/pantech/config.json, and each profile's API key in the OS
// keychain, or in a file only you can read where there is none.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const DefaultProfile = "default"

type Profile struct {
	OrganizationID   string   `json:"organization_id,omitempty"`
	OrganizationName string   `json:"organization_name,omitempty"`
	KeyID            string   `json:"key_id,omitempty"`
	Scopes           []string `json:"scopes,omitempty"`
	ExpiresAt        string   `json:"expires_at,omitempty"`
	// Saved by v0.1.2 and older, which could sign in elsewhere than
	// production: a key from another API is refused by this one.
	APIURL string `json:"api_url,omitempty"`
}

type Config struct {
	// Current is the profile used when --profile is not given.
	Current  string             `json:"current,omitempty"`
	Profiles map[string]Profile `json:"profiles"`
}

// Name is "pantech", or "pantech-staging" in a staging build, which so keeps
// its profiles and keys apart from production's.
var Name = "pantech"

// Dir is $PANTECH_CONFIG_DIR, else $XDG_CONFIG_HOME/pantech, else ~/.config/pantech.
func Dir() (string, error) {
	if d := os.Getenv("PANTECH_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, Name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", Name), nil
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Load reads the config, or returns an empty one if there is none yet.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	cfg := &Config{Profiles: map[string]Profile{}}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", p, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	return cfg, nil
}

func (c *Config) Save() error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p, append(data, '\n'))
}

// Name is the profile to use: the one asked for, else the current one, else "default".
func (c *Config) Name(asked string) string {
	switch {
	case asked != "":
		return asked
	case c.Current != "":
		return c.Current
	default:
		return DefaultProfile
	}
}

func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// writeFile replaces a file atomically, mode 0600.
func writeFile(p string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
