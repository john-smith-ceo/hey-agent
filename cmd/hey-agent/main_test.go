package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveDotenvKeyUsesPrivateFileAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hey-agent", ".env")
	if err := saveDotenvKey(path, "test-key"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("dotenv mode = %o, want 600", got)
	}
	if got, err := loadDotenvKey(path); err != nil || got != "test-key" {
		t.Fatalf("loaded key = %q, err = %v", got, err)
	}
}

func TestSaveDotenvKeyRejectsEmptyAndDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", ".env")
	if err := saveDotenvKey(path, " \n"); err == nil {
		t.Fatal("empty key must be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("empty key created file: %v", err)
	}
}

func TestSaveDotenvKeyRejectsMultilineValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := saveDotenvKey(path, "first\nsecond"); err == nil {
		t.Fatal("multiline key must be rejected")
	}
}

func TestLoadDotenvKeyAcceptsCanonicalName(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("HEY_AGENT_API_KEY=canonical\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadDotenvKey(path)
	if err != nil || got != "canonical" {
		t.Fatalf("loaded key = %q, err = %v", got, err)
	}
}

func TestTmuxSessionTargetDisambiguatesNumericSessionNames(t *testing.T) {
	if got, want := tmuxSessionTarget("1"), "1:"; got != want {
		t.Fatalf("tmux session target = %q, want %q", got, want)
	}
}

func TestRuntimeSettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.json")
	want := runtimeSettings{Mode: "push", Silence: "2s", Submit: true}
	if err := saveRuntimeSettings(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadRuntimeSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("runtime settings = %+v, want %+v", got, want)
	}
}
