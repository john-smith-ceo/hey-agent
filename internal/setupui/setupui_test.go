package setupui

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/john-smith-ceo/hey-agent/internal/envfile"
)

// fakeRun records every invocation and answers by script name.
type fakeRun struct {
	calls   []string
	answers map[string]string
	err     map[string]error
}

func (f *fakeRun) run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	return f.answers[name], f.err[name]
}

func TestSetAPIKey_SavesAndRestarts(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	fr := &fakeRun{answers: map[string]string{"zenity": "sk-test\n"}}
	restarted := false
	u := &UI{EnvPath: env, Run: fr.run, Restart: func() error { restarted = true; return nil }}
	u.SetAPIKey(context.Background())
	m, err := envfile.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if m["HEY_AGENT_API_KEY"] != "sk-test" {
		t.Fatalf("key not saved: %v", m)
	}
	if !restarted {
		t.Fatal("daemon restart not requested")
	}
	// A notification must have gone out through some channel.
	if len(fr.calls) < 2 {
		t.Fatalf("no notification sent: %v", fr.calls)
	}
}

// realExitError produces a genuine *exec.ExitError — zenity's "user pressed
// Cancel" is exactly that shape, and cancelled() only trusts the real type.
func realExitError(t *testing.T) error {
	t.Helper()
	err := exec.Command("false").Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("expected ExitError, got %v", err)
	}
	return err
}

func TestSetAPIKey_CancelWritesNothing(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	cancel := realExitError(t)
	fr := &fakeRun{err: map[string]error{"zenity": cancel}}
	u := &UI{EnvPath: env, Run: fr.run}
	u.SetAPIKey(context.Background())
	if _, err := os.Stat(env); !os.IsNotExist(err) {
		t.Fatal("cancelled dialog wrote a file")
	}
	// Cancel is silent: no notification, no fallback dialog.
	if len(fr.calls) != 1 {
		t.Fatalf("cancel should stay silent, calls: %v", fr.calls)
	}
}

func TestSetAPIKey_EmptyRejected(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	fr := &fakeRun{answers: map[string]string{"zenity": "   \n"}}
	u := &UI{EnvPath: env, Run: fr.run}
	u.SetAPIKey(context.Background())
	if _, err := os.Stat(env); !os.IsNotExist(err) {
		t.Fatal("empty key wrote a file")
	}
}

func TestVoiceSettings_PartialUpdate(t *testing.T) {
	env := filepath.Join(t.TempDir(), ".env")
	if err := envfile.Update(env, map[string]string{"HEY_AGENT_TTS_VOICE": "onyx"}); err != nil {
		t.Fatal(err)
	}
	// voice blank, model set, url set — voice must survive untouched.
	fr := &fakeRun{answers: map[string]string{"zenity": "\x01gpt-tts-2\x01http://localhost:8080\n"}}
	u := &UI{EnvPath: env, Run: fr.run}
	u.VoiceSettings(context.Background())
	m, _ := envfile.Load(env)
	if m["HEY_AGENT_TTS_VOICE"] != "onyx" {
		t.Fatalf("existing voice clobbered: %v", m)
	}
	if m["HEY_AGENT_TTS_MODEL"] != "gpt-tts-2" || m["HEY_AGENT_TTS_BASE_URL"] != "http://localhost:8080" {
		t.Fatalf("fields not saved: %v", m)
	}
}

func TestNotify_FallsBackToZenity(t *testing.T) {
	fr := &fakeRun{err: map[string]error{"notify-send": errors.New("no such binary")}}
	u := &UI{Run: fr.run}
	u.Notify(context.Background(), "t", "body")
	var sawZenity bool
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "zenity") {
			sawZenity = true
		}
	}
	if !sawZenity {
		t.Fatalf("no zenity fallback: %v", fr.calls)
	}
}

func TestSaved_NoRestartHookTellsUser(t *testing.T) {
	fr := &fakeRun{}
	u := &UI{Run: fr.run}
	u.saved(context.Background(), "ok")
	var told bool
	for _, c := range fr.calls {
		if strings.Contains(c, "notify-send") {
			told = true
		}
	}
	if !told {
		t.Fatalf("user not informed: %v", fr.calls)
	}
}
