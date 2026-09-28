package target

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// resolverBacked builds a Sender whose Resolver answers from canned tmux
// output. paneIDs lists what list-panes will report as alive.
func resolverBacked(t *testing.T, clients, paneIDs string) (*Sender, *fakeTmux) {
	t.Helper()
	fake := &fakeTmux{clients: clients, panes: paneIDs}
	res, err := New(WithRunner(fake.run))
	if err != nil {
		t.Fatalf("New resolver: %v", err)
	}
	return NewSender(res), fake
}

func TestSenderSendBeforeBeginFailsClosed(t *testing.T) {
	s, _ := resolverBacked(t, "", "")
	err := s.Send(context.Background(), "hello", false)
	if !errors.Is(err, ErrUnbound) {
		t.Fatalf("Send before BeginRecording = %v, want ErrUnbound", err)
	}
}

func TestSenderBeginResolveFailure(t *testing.T) {
	// No clients at all → Resolve fails → nothing gets bound → Send refuses.
	s, _ := resolverBacked(t, "", "")
	if err := s.BeginRecording(context.Background()); err == nil {
		t.Fatal("BeginRecording with no clients succeeded")
	}
	if got := s.Target(); got != "" {
		t.Fatalf("Target after failed resolve = %q, want empty", got)
	}
	if err := s.Send(context.Background(), "hello", false); !errors.Is(err, ErrUnbound) {
		t.Fatalf("Send after failed BeginRecording = %v, want ErrUnbound", err)
	}
}

func TestSenderHappyPath(t *testing.T) {
	s, _ := resolverBacked(t, standLine(100, "/dev/pts/1", "%7"), "%7")
	if err := s.BeginRecording(context.Background()); err != nil {
		t.Fatalf("BeginRecording: %v", err)
	}
	if got := s.Target(); got != "%7" {
		t.Fatalf("Target = %q, want %%7", got)
	}
	var pasted []string
	s.paste = func(ctx context.Context, pane, text string, submit bool) error {
		pasted = append(pasted, pane+":"+text)
		return nil
	}
	if err := s.Send(context.Background(), "hello", true); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(pasted) != 1 || pasted[0] != "%7:hello" {
		t.Fatalf("paste calls = %v, want one paste into %%7", pasted)
	}
}

func TestSenderRefusesWhenTargetMoved(t *testing.T) {
	s, fake := resolverBacked(t, standLine(100, "/dev/pts/1", "%7"), "%7")
	if err := s.BeginRecording(context.Background()); err != nil {
		t.Fatalf("BeginRecording: %v", err)
	}
	// The user switched panes while the audio was being transcribed.
	fake.clients = standLine(100, "/dev/pts/1", "%9")
	fake.panes = "%7\n%9"
	called := false
	s.paste = func(context.Context, string, string, bool) error { called = true; return nil }
	err := s.Send(context.Background(), "hello", false)
	if err == nil || !strings.Contains(err.Error(), "delivery refused") {
		t.Fatalf("Send after target moved = %v, want refused", err)
	}
	if called {
		t.Fatal("paste ran despite a moved target")
	}
}

func TestSenderRefusesEmptyText(t *testing.T) {
	s, _ := resolverBacked(t, standLine(100, "/dev/pts/1", "%7"), "%7")
	if err := s.BeginRecording(context.Background()); err != nil {
		t.Fatalf("BeginRecording: %v", err)
	}
	if err := s.Send(context.Background(), "   ", false); err == nil {
		t.Fatal("Send with whitespace text succeeded")
	}
}

func TestSenderRebindsPerRecording(t *testing.T) {
	s, fake := resolverBacked(t, standLine(100, "/dev/pts/1", "%7"), "%7\n%9")
	if err := s.BeginRecording(context.Background()); err != nil {
		t.Fatalf("first BeginRecording: %v", err)
	}
	// Between two recordings the user moved to another pane; the second
	// BeginRecording must rebind rather than keep the stale one.
	fake.clients = standLine(200, "/dev/pts/1", "%9")
	if err := s.BeginRecording(context.Background()); err != nil {
		t.Fatalf("second BeginRecording: %v", err)
	}
	if got := s.Target(); got != "%9" {
		t.Fatalf("Target = %q, want %%9", got)
	}
}
