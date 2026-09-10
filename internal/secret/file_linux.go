//go:build linux

package secret

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Linux has no single keychain command every desktop ships, so the key lives in
// a file only its owner can read. The permission check is deliberate: a key
// readable by other accounts is not a stored secret, it is a published one.
func path(service string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	name := strings.TrimPrefix(service, "hey-agent.")
	return filepath.Join(home, ".config", "hey-agent", name), nil
}

func Load(service string) (string, error) {
	file, err := path(service)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(file)
	if err != nil {
		return "", fmt.Errorf("key not found at %s", file)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s is readable by other accounts; run: chmod 600 %s", file, file)
	}
	contents, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(contents))
	if key == "" {
		return "", fmt.Errorf("%s is empty", file)
	}
	return key, nil
}

func Save(service, value string) error {
	if value == "" {
		return errors.New("API key is empty")
	}
	file, err := path(service)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, []byte(value+"\n"), 0o600)
}

// Location reports where the key is kept, for messages shown to the user.
func Location(service string) string {
	file, err := path(service)
	if err != nil {
		return "~/.config/hey-agent"
	}
	return file
}
