package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const keyringService = "pantech-cli"

const (
	InKeychain = "keychain"
	InFile     = "file"
)

// SaveKey stores a profile's API key in the OS keychain (the macOS Keychain,
// or the Secret Service on Linux), or, where there is no keychain (a headless
// server), in credentials.json beside the config, readable only by you. It
// says which.
func SaveKey(profile, key string) (string, error) {
	if os.Getenv("PANTECH_NO_KEYRING") == "" {
		if err := keyring.Set(keyringService, profile, key); err == nil {
			// A key moved into the keychain should not linger in the file.
			_ = deleteFileKey(profile)
			return InKeychain, nil
		}
	}
	return InFile, setFileKey(profile, key)
}

// LoadKey returns a profile's stored key, or "" if it has none. Not in the
// keychain, or no keychain on this machine: then the file is where it went.
func LoadKey(profile string) (string, error) {
	if os.Getenv("PANTECH_NO_KEYRING") == "" {
		if key, err := keyring.Get(keyringService, profile); err == nil {
			return key, nil
		}
	}
	return fileKey(profile)
}

// DeleteKey removes a profile's key from wherever it is. The keychain is
// asked best-effort: "not there" and "no keychain on this machine" read alike.
// With PANTECH_NO_KEYRING set it is left alone, like SaveKey and LoadKey do.
func DeleteKey(profile string) error {
	if os.Getenv("PANTECH_NO_KEYRING") == "" {
		_ = keyring.Delete(keyringService, profile)
	}
	return deleteFileKey(profile)
}

func credentialsPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

func readCredentials() (map[string]string, error) {
	p, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	creds := map[string]string{}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return creds, nil
	}
	if err != nil {
		return nil, err
	}
	return creds, json.Unmarshal(data, &creds)
}

func writeCredentials(creds map[string]string) error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	if len(creds) == 0 {
		err := os.Remove(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p, append(data, '\n'))
}

func fileKey(profile string) (string, error) {
	creds, err := readCredentials()
	if err != nil {
		return "", err
	}
	return creds[profile], nil
}

func setFileKey(profile, key string) error {
	creds, err := readCredentials()
	if err != nil {
		return err
	}
	creds[profile] = key
	return writeCredentials(creds)
}

func deleteFileKey(profile string) error {
	creds, err := readCredentials()
	if err != nil {
		return err
	}
	if _, ok := creds[profile]; !ok {
		return nil
	}
	delete(creds, profile)
	return writeCredentials(creds)
}
