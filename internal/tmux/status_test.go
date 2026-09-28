package tmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The indicator strings are pinned verbatim: the status line is the one
// place a wrong colour code or a missing reset is visible to the user every
// second, so exact equality beats a fuzzy Contains here.
func TestIndicatorMapsEachStatePairToAnExactString(t *testing.T) {
	tests := []struct {
		name     string
		statuses Statuses
		want     string
	}{
		{"both idle",
			Statuses{In: "idle", Out: "idle"},
			"#[fg=#98C379]in:idle#[default] #[fg=#98C379]out:idle#[default] "},
		{"recording keeps out idle",
			Statuses{In: "recording", Out: "idle"},
			"#[fg=#E06C75]in:rec…#[default] #[fg=#98C379]out:idle#[default] "},
		{"transcribing keeps out idle",
			Statuses{In: "transcribing", Out: "idle"},
			"#[fg=#E5C07B]in:transcribe…#[default] #[fg=#98C379]out:idle#[default] "},
		{"input error keeps out idle",
			Statuses{In: "error", Out: "idle"},
			"#[fg=#E06C75]in:err#[default] #[fg=#98C379]out:idle#[default] "},
		{"speaking keeps in idle",
			Statuses{In: "idle", Out: "speaking"},
			"#[fg=#98C379]in:idle#[default] #[fg=#61AFEF]out:speak…#[default] "},
		{"output error keeps in idle",
			Statuses{In: "idle", Out: "error"},
			"#[fg=#98C379]in:idle#[default] #[fg=#E06C75]out:err#[default] "},
		{"recording while speaking",
			Statuses{In: "recording", Out: "speaking"},
			"#[fg=#E06C75]in:rec…#[default] #[fg=#61AFEF]out:speak…#[default] "},
		{"transcribing over an output error",
			Statuses{In: "transcribing", Out: "err"},
			"#[fg=#E5C07B]in:transcribe…#[default] #[fg=#E06C75]out:err#[default] "},
		{"short aliases render identically",
			Statuses{In: "rec", Out: "speak"},
			"#[fg=#E06C75]in:rec…#[default] #[fg=#61AFEF]out:speak…#[default] "},
		{"pasted means the input is ready again",
			Statuses{In: "pasted", Out: "idle"},
			"#[fg=#98C379]in:idle#[default] #[fg=#98C379]out:idle#[default] "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Indicator(tt.statuses); got != tt.want {
				t.Fatalf("Indicator(%+v) = %q, want %q", tt.statuses, got, tt.want)
			}
		})
	}
}

// A daemon whose state never arrived — socket down, wedged, stale peer —
// must not show the same green "idle" as a healthy one; grey "off" is the
// honest answer.
func TestIndicatorUnknownStateRendersOffAndDiffersFromIdle(t *testing.T) {
	idle := Indicator(Statuses{In: "idle", Out: "idle"})
	for name, statuses := range map[string]Statuses{
		"zero value (socket never answered)": {},
		"explicit off":                       {In: "off", Out: "off"},
		"unrecognized garbage":               {In: "wedged", Out: "???"},
	} {
		got := Indicator(statuses)
		if got == idle {
			t.Fatalf("%s: Indicator(%+v) = %q, must differ from idle %q", name, statuses, got, idle)
		}
		for _, want := range []string{"in:off", "out:off", "#5C6370"} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: Indicator(%+v) = %q, want substring %q", name, statuses, got, want)
			}
		}
	}
}

// Park/restore against a fake tmux that persists options the way the real
// server does: session-set values in one directory, the global fallback in
// another. This is what lets a status line survive the listener dying.
func TestSetAndRestoreKeepsUserStatusRight(t *testing.T) {
	_, store := installStatusFakeTmux(t)
	// The user's own status-right, set on the session itself.
	writeStoreFile(t, store, "session", "status-right", "#[fg=red]USER-RIGHT")

	status, err := NewStatus("hey-agent", "vim", "tap", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := status.Configure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.Set(ctx, Statuses{In: "recording", Out: "speaking"}); err != nil {
		t.Fatal(err)
	}

	shown := readStoreFile(t, store, "session", "status-right")
	for _, want := range []string{"in:rec…", "out:speak…", "USER-RIGHT"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("borrowed status-right %q misses %q", shown, want)
		}
	}

	if err := status.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if got := readStoreFile(t, store, "session", "status-right"); got != "#[fg=red]USER-RIGHT" {
		t.Fatalf("restored status-right = %q, want the user's original", got)
	}
}

// When the user's status-right was inherited from the global value rather
// than set on the session, restore must unset it again — writing the base
// back as a session option would pin today's global value forever.
func TestRestoreUnsetsStatusRightThatWasNeverSessionSet(t *testing.T) {
	_, store := installStatusFakeTmux(t)
	writeStoreFile(t, store, "global", "status-right", "GLOBAL-RIGHT")

	status, err := NewStatus("hey-agent", "zsh", "push", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := status.Configure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.Set(ctx, Statuses{In: "idle", Out: "idle"}); err != nil {
		t.Fatal(err)
	}
	if shown := readStoreFile(t, store, "session", "status-right"); !strings.Contains(shown, "GLOBAL-RIGHT") {
		t.Fatalf("borrowed status-right %q should still show the inherited base", shown)
	}

	if err := status.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store, "session", "status-right")); !os.IsNotExist(err) {
		t.Fatalf("status-right should be unset again, stat err = %v", err)
	}
	for _, parked := range []string{wasOption, baseOption, lenWas, lenBase} {
		if _, err := os.Stat(filepath.Join(store, "session", parked)); !os.IsNotExist(err) {
			t.Fatalf("parked option %s left behind, stat err = %v", parked, err)
		}
	}
}

// `hey-agent stop` reaches Restore without a Status object, e.g. after the
// listener was killed; the parked user options make that possible.
func TestPackageRestoreWorksAfterListenerDeath(t *testing.T) {
	_, store := installStatusFakeTmux(t)
	writeStoreFile(t, store, "session", "status-right", "USER-RIGHT")

	status, err := NewStatus("hey-agent", "vim", "tap", true)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := status.Configure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.Set(ctx, Statuses{In: "error", Out: "idle"}); err != nil {
		t.Fatal(err)
	}

	restored, err := Restore(ctx, "hey-agent")
	if err != nil || !restored {
		t.Fatalf("Restore = (%v, %v), want (true, nil)", restored, err)
	}
	if got := readStoreFile(t, store, "session", "status-right"); got != "USER-RIGHT" {
		t.Fatalf("restored status-right = %q, want the user's original", got)
	}
}

// A session the daemon never touched reports "nothing to restore" instead
// of clobbering the line.
func TestPackageRestoreOnUntouchedSessionReportsFalse(t *testing.T) {
	installStatusFakeTmux(t)
	restored, err := Restore(context.Background(), "pristine")
	if err != nil || restored {
		t.Fatalf("Restore on untouched session = (%v, %v), want (false, nil)", restored, err)
	}
}

// The dedicated listener window owns its whole status line, so both
// segments land on status-left next to the app name.
func TestSetInOwnSessionRendersBothSegmentsInStatusLeft(t *testing.T) {
	_, store := installStatusFakeTmux(t)
	status, err := NewStatus("hey-agent", "zsh", "push", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := status.Configure(ctx); err != nil {
		t.Fatal(err)
	}
	if err := status.Set(ctx, Statuses{In: "transcribing", Out: "off"}); err != nil {
		t.Fatal(err)
	}
	left := readStoreFile(t, store, "session", "status-left")
	for _, want := range []string{"in:transcribe…", "out:off", "zsh"} {
		if !strings.Contains(left, want) {
			t.Fatalf("status-left %q misses %q", left, want)
		}
	}
}

func TestSessionTargetDisambiguatesNumericSessionNames(t *testing.T) {
	if got, want := sessionTarget("1"), "1:"; got != want {
		t.Fatalf("tmux session target = %q, want %q", got, want)
	}
}

// installStatusFakeTmux puts a stateful tmux double first on PATH. It logs
// every invocation to $HEY_CODEX_TMUX_LOG like the sender test double does,
// and additionally keeps tmux options as plain files under <dir>/store —
// session-set options in "session", the global fallback in "global" — so
// park/restore can be exercised end to end.
func installStatusFakeTmux(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "commands.log")
	store := filepath.Join(dir, "store")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$HEY_CODEX_TMUX_LOG"
store="$(dirname "$HEY_CODEX_TMUX_LOG")/store"
mkdir -p "$store/session" "$store/global"

case "$1" in
set-option)
  # Always reached as: set-option -t <session>: [-u] <name> [value]
  if [ "$4" = "-u" ]; then
    rm -f "$store/session/$5"
  else
    printf '%s' "$5" > "$store/session/$4"
  fi
  ;;
show-option)
  # Reached as: show-option -t <session>: -qv <name> (session option)
  #         or: show-option -gqv <name>             (global option)
  dir="$store/session"
  name=""
  skip=""
  for arg in "$@"; do
    if [ -n "$skip" ]; then
      skip=""
      continue
    fi
    case "$arg" in
      -t)  skip=1 ;;
      -g*) dir="$store/global" ;;
      -*)  ;;
      *)   name="$arg" ;;
    esac
  done
  if [ -n "$name" ] && [ -f "$dir/$name" ]; then
    cat "$dir/$name"
  fi
  ;;
show-options)
  # show-options -t <session>: lists only the options set on the session
  # itself — exactly what the store's "session" directory holds.
  for f in "$store/session"/*; do
    if [ -e "$f" ]; then
      printf '%s %s\n' "$(basename "$f")" "$(cat "$f")"
    fi
  done
  ;;
esac
exit 0
`
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "session"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "global"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEY_CODEX_TMUX_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath, store
}

func writeStoreFile(t *testing.T, store, scope, option, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(store, scope, option), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readStoreFile(t *testing.T, store, scope, option string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store, scope, option))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
