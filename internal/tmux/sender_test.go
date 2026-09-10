package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSenderPastesAndSubmits(t *testing.T) {
	logPath, stdinPath := installFakeTmux(t)
	sender, err := New("hey-agent:0.0")
	if err != nil {
		t.Fatal(err)
	}
	sender.SubmitDelay = 0
	if err := sender.Send(context.Background(), "hello from voice", true); err != nil {
		t.Fatal(err)
	}

	commands := readTestFile(t, logPath)
	lines := strings.Split(strings.TrimSpace(commands), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected three tmux commands, got %d:\n%s", len(lines), commands)
	}
	if !strings.HasPrefix(lines[0], "load-buffer -b hey-agent-") {
		t.Fatalf("unexpected load command: %s", lines[0])
	}
	if !strings.Contains(lines[1], "paste-buffer -d -b hey-agent-") || !strings.HasSuffix(lines[1], "-t hey-agent:0.0") {
		t.Fatalf("unexpected paste command: %s", lines[1])
	}
	if lines[2] != "send-keys -t hey-agent:0.0 Enter" {
		t.Fatalf("unexpected submit command: %s", lines[2])
	}
	if got := readTestFile(t, stdinPath); got != "hello from voice" {
		t.Fatalf("unexpected pasted text: %q", got)
	}
}

func TestSenderCanPasteWithoutSubmitting(t *testing.T) {
	logPath, _ := installFakeTmux(t)
	sender, err := New("hey-agent:0.0")
	if err != nil {
		t.Fatal(err)
	}
	sender.SubmitDelay = 0
	if err := sender.Send(context.Background(), "review me", false); err != nil {
		t.Fatal(err)
	}

	commands := readTestFile(t, logPath)
	if strings.Contains(commands, "send-keys") {
		t.Fatalf("manual-review delivery submitted text:\n%s", commands)
	}
}

func installFakeTmux(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	stdinPath := filepath.Join(dir, "stdin.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HEY_CODEX_TMUX_LOG"
if [ "$1" = "load-buffer" ]; then
  cat >> "$HEY_CODEX_TMUX_STDIN"
fi
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEY_CODEX_TMUX_LOG", logPath)
	t.Setenv("HEY_CODEX_TMUX_STDIN", stdinPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath, stdinPath
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
