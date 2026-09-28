package target

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// clientLine builds one list-clients row in clientsFormat field order.
func clientLine(activity int64, tty, session, sessionID, windowID string, windowIndex int, paneID, flags, windowName string) string {
	return fmt.Sprintf("%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s",
		activity, tty, session, sessionID, windowID, windowIndex, paneID, flags, windowName)
}

// standLine is clientLine with the values every test shares unless it is
// specifically about them.
func standLine(activity int64, tty, paneID string) string {
	return clientLine(activity, tty, "5", "$5", "@1", 1, paneID, "attached,UTF-8", "codex")
}

// fakeTmux serves canned output per tmux subcommand and records every call,
// so a test can watch Resolve and Verify without a real tmux server.
type fakeTmux struct {
	clients    string
	clientsErr error // together with clients as tmux's stderr
	panes      string
	panesErr   error
	calls      [][]string
}

func (f *fakeTmux) run(ctx context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	switch subcommand(args) {
	case "list-clients":
		if f.clientsErr != nil {
			return []byte(f.clients), f.clientsErr
		}
		return []byte(f.clients), nil
	case "list-panes":
		if f.panesErr != nil {
			return []byte(f.panes), f.panesErr
		}
		return []byte(f.panes), nil
	}
	return nil, fmt.Errorf("unexpected tmux args: %v", args)
}

// subcommand finds the tmux subcommand in argv — the first argument that is
// not a flag and not the value of a flag like -L that takes one.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-L", "-S":
			i++ // skip the flag's value
		default:
			if !strings.HasPrefix(args[i], "-") {
				return args[i]
			}
		}
	}
	return ""
}

func newResolver(t *testing.T, f *fakeTmux, opts ...Option) *Resolver {
	t.Helper()
	opts = append([]Option{WithRunner(f.run)}, opts...)
	r, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name       string
		clients    string
		clientsErr error
		wantPane   string
		wantTTY    string
		wantErr    error
	}{
		{
			name:    "empty client list",
			clients: "",
			wantErr: ErrNoClient,
		},
		{
			name:     "single attached client",
			clients:  standLine(1790580356, "/dev/pts/1", "%7") + "\n",
			wantPane: "%7",
			wantTTY:  "/dev/pts/1",
		},
		{
			name: "two clients, freshest wins",
			clients: standLine(1790580300, "/dev/pts/1", "%5") + "\n" +
				standLine(1790580356, "/dev/pts/3", "%7") + "\n",
			wantPane: "%7",
			wantTTY:  "/dev/pts/3",
		},
		{
			name: "two clients, equal activity",
			clients: standLine(1790580356, "/dev/pts/1", "%5") + "\n" +
				standLine(1790580356, "/dev/pts/3", "%7") + "\n",
			wantErr: ErrAmbiguous,
		},
		{
			// client_activity ticks in whole seconds, so one tick apart is
			// still "the same moment" as far as tmux can tell.
			name: "two clients, activity inside grace",
			clients: standLine(1790580355, "/dev/pts/1", "%5") + "\n" +
				standLine(1790580356, "/dev/pts/3", "%7") + "\n",
			wantErr: ErrAmbiguous,
		},
		{
			name: "two clients, activity beyond grace",
			clients: standLine(1790580350, "/dev/pts/1", "%5") + "\n" +
				standLine(1790580356, "/dev/pts/3", "%7") + "\n",
			wantPane: "%7",
			wantTTY:  "/dev/pts/3",
		},
		{
			// Nobody watches through a detached client, so it can neither
			// win nor trigger ambiguity, however fresh its activity.
			name: "detached client with fresher activity loses",
			clients: clientLine(1790580999, "/dev/pts/9", "agt", "$9", "@9", 1, "%9", "", "w") + "\n" +
				standLine(1790580356, "/dev/pts/1", "%7") + "\n",
			wantPane: "%7",
			wantTTY:  "/dev/pts/1",
		},
		{
			name:    "only detached clients",
			clients: clientLine(1790580356, "/dev/pts/9", "agt", "$9", "@9", 1, "%9", "", "w") + "\n",
			wantErr: ErrNoClient,
		},
		{
			name:    "garbage output",
			clients: "this is not tmux output\n",
			wantErr: ErrNoTmux,
		},
		{
			name:    "truncated row",
			clients: "1790580356\t/dev/pts/1\t5\n",
			wantErr: ErrNoTmux,
		},
		{
			name:       "tmux call fails",
			clients:    "exit status 1: some internal tmux problem",
			clientsErr: errors.New("exit status 1"),
			wantErr:    ErrNoTmux,
		},
		{
			// A dead socket answers like this; semantically it is "nobody
			// could be attached", not "tmux is broken".
			name:       "no server running",
			clients:    "error connecting to /tmp/tmux-1000/heydev (No such file or directory)",
			clientsErr: errors.New("exit status 1"),
			wantErr:    ErrNoClient,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeTmux{clients: tc.clients, clientsErr: tc.clientsErr}
			r := newResolver(t, f)
			got, err := r.Resolve(context.Background())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.PaneID != tc.wantPane {
				t.Fatalf("expected pane %s, got %s", tc.wantPane, got.PaneID)
			}
			if got.ClientTTY != tc.wantTTY {
				t.Fatalf("expected tty %s, got %s", tc.wantTTY, got.ClientTTY)
			}
		})
	}
}

func TestResolveFillsDiagnostics(t *testing.T) {
	f := &fakeTmux{clients: standLine(1790580356, "/dev/pts/1", "%7") + "\n"}
	got, err := newResolver(t, f).Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Target{
		PaneID:      "%7",
		SessionID:   "$5",
		SessionName: "5",
		WindowID:    "@1",
		WindowIndex: 1,
		WindowName:  "codex",
		ClientTTY:   "/dev/pts/1",
		Activity:    time.Unix(1790580356, 0),
	}
	if got != want {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	target := Target{
		PaneID:      "%7",
		SessionID:   "$5",
		SessionName: "5",
		WindowID:    "@1",
		WindowIndex: 1,
		WindowName:  "codex",
		ClientTTY:   "/dev/pts/1",
		Activity:    time.Unix(1790580356, 0),
	}

	tests := []struct {
		name string
		// state of the world at verify time
		clients string
		panes   string
		wantErr error
	}{
		{
			name:    "target unchanged",
			clients: standLine(1790580356, "/dev/pts/1", "%7") + "\n",
			wantErr: nil,
		},
		{
			// The user switched windows during recording: the freshest
			// resolve names another pane, and %7 is still alive elsewhere.
			name:    "user moved to another window",
			clients: clientLine(1790580356, "/dev/pts/1", "5", "$5", "@2", 2, "%9", "attached,UTF-8", "hey-agent-voice") + "\n",
			panes:   "%7\n%9\n%5\n",
			wantErr: ErrTargetMoved,
		},
		{
			// %7 was closed and tmux focused %9 in its place; %7 is absent
			// from the pane list, which is what separates "gone" from
			// "moved".
			name:    "pane closed during recording",
			clients: clientLine(1790580356, "/dev/pts/1", "5", "$5", "@1", 1, "%9", "attached,UTF-8", "codex") + "\n",
			panes:   "%9\n%5\n",
			wantErr: ErrPaneGone,
		},
		{
			name:    "everybody detached during recording",
			clients: "",
			wantErr: ErrNoClient,
		},
		{
			name: "a second terminal appeared during recording",
			clients: standLine(1790580356, "/dev/pts/1", "%7") + "\n" +
				standLine(1790580356, "/dev/pts/3", "%5") + "\n",
			wantErr: ErrAmbiguous,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeTmux{clients: tc.clients, panes: tc.panes}
			r := newResolver(t, f)
			err := r.Verify(ctx, target)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}
		})
	}
}

// Resolve-then-Verify walks the real pair of calls the daemon makes: the
// pane resolved when recording starts is the one Verify re-checks.
func TestResolveThenVerifyRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := &fakeTmux{clients: standLine(1790580356, "/dev/pts/1", "%7") + "\n"}
	r := newResolver(t, f)

	target, err := r.Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Verify(ctx, target); err != nil {
		t.Fatalf("verify right after resolve should pass, got %v", err)
	}

	// Now the user switches to window 2, pane %9, while %7 keeps existing.
	f.clients = clientLine(1790580356, "/dev/pts/1", "5", "$5", "@2", 2, "%9", "attached,UTF-8", "hey-agent-voice") + "\n"
	f.panes = "%7\n%9\n%5\n"
	if err := r.Verify(ctx, target); !errors.Is(err, ErrTargetMoved) {
		t.Fatalf("expected ErrTargetMoved, got %v", err)
	}
}

func TestVerifyEmptyPrevFailsClosed(t *testing.T) {
	f := &fakeTmux{}
	r := newResolver(t, f)
	if err := r.Verify(context.Background(), Target{}); !errors.Is(err, ErrPaneGone) {
		t.Fatalf("expected ErrPaneGone, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("empty target must be refused without touching tmux, saw %v", f.calls)
	}
}

func TestContextCancellationIsNotATmuxError(t *testing.T) {
	f := &fakeTmux{
		clientsErr: errors.New("signal: killed"),
	}
	r := newResolver(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Resolve(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestNewFailsWhenTmuxBinaryIsMissing(t *testing.T) {
	// An empty PATH hides tmux; LookPath is what makes New fail fast.
	t.Setenv("PATH", t.TempDir())
	if _, err := New(); !errors.Is(err, ErrNoTmux) {
		t.Fatalf("expected ErrNoTmux, got %v", err)
	}
	// An injected Runner does not need the binary at all — this is what
	// keeps the whole suite runnable on machines without tmux.
	if _, err := New(WithRunner((&fakeTmux{}).run)); err != nil {
		t.Fatalf("fake runner should bypass the binary check, got %v", err)
	}
}

func TestSocketFlagIsPrepended(t *testing.T) {
	f := &fakeTmux{clients: standLine(1790580356, "/dev/pts/1", "%7") + "\n"}
	r := newResolver(t, f, WithSocket("heydev"))
	if _, err := r.Resolve(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected one tmux call, got %v", f.calls)
	}
	want := []string{"-L", "heydev", "list-clients", "-F", clientsFormat}
	got := f.calls[0]
	if len(got) != len(want) {
		t.Fatalf("expected args %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, got)
		}
	}
}

func TestNegativeGraceRejected(t *testing.T) {
	if _, err := New(WithRunner((&fakeTmux{}).run), WithActivityGrace(-time.Second)); err == nil {
		t.Fatal("expected an error for a negative grace")
	}
}
