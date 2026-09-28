// Package target answers one question: which tmux pane is the user looking at
// right now?
//
// hey-agent pastes a finished transcription into that pane. The target is
// resolved twice per recording — once when recording starts, and once more
// right before the text is sent — because the user may switch windows or
// close the pane while the microphone is open. Pasting into a stale pane
// would leak dictated text somewhere it does not belong, so every doubt is
// resolved by refusing to send: no attached client, an ambiguous choice, a
// vanished pane, or a moved focus all end in an error, never in a guess.
//
// Tested against tmux 3.2a. The resolver relies on list-clients expanding
// pane/window/session formats against each client's current view, which any
// reasonably modern tmux does.
package target

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Sentinel errors of the fail-closed matrix (SPEC-HEY-AGENT.md §3). Callers
// match them with errors.Is; every one of them means "do not paste".
var (
	// ErrNoTmux means we could not talk to tmux at all: the binary is
	// missing, the call failed, or tmux answered in a format we do not
	// understand. It is deliberately distinct from ErrNoClient — a broken
	// tmux is an operational problem, an empty client list is a normal
	// situation.
	ErrNoTmux = errors.New("tmux is unavailable")

	// ErrNoClient means tmux answered but there is no attached client to aim
	// at: every client is detached, or nobody is connected at all. A dead or
	// unreachable tmux server also lands here — "no server running" means no
	// pane is being watched by anyone.
	ErrNoClient = errors.New("no attached tmux client")

	// ErrAmbiguous means two or more attached clients were active within the
	// grace window, so "the pane the user is watching" cannot be chosen
	// honestly. Refusing is deliberate: guessing here is exactly how
	// dictated text ends up pasted into a stranger's terminal.
	ErrAmbiguous = errors.New("ambiguous tmux target")

	// ErrPaneGone means the pane remembered from the first resolve no longer
	// exists on the server at verify time — it was closed or killed, or its
	// window or session died with it.
	ErrPaneGone = errors.New("target pane is gone")

	// ErrTargetMoved means the remembered pane still exists, but the
	// freshest client is now looking at a different pane, window, or
	// session.
	ErrTargetMoved = errors.New("tmux target moved")
)

// Target is one resolved pane plus the breadcrumbs needed to recognise it
// again and to explain the choice in logs.
type Target struct {
	PaneID      string    // "%7" — the id tmux -t accepts
	SessionID   string    // "$5" — stays put when the session is renamed
	SessionName string    // "5" — for human-readable diagnostics
	WindowID    string    // "@7" — stays put when windows are renumbered
	WindowIndex int       // 1 — for diagnostics
	WindowName  string    // "codex" — for diagnostics
	ClientTTY   string    // "/dev/pts/1" — the terminal that won the resolve
	Activity    time.Time // when the winning client last sent input
}

func (t Target) String() string {
	return fmt.Sprintf("pane %s (session %s window %d %q, client %s)",
		t.PaneID, t.SessionName, t.WindowIndex, t.WindowName, t.ClientTTY)
}

// same reports whether two resolves point at the same pane of the same window
// of the same session. IDs are compared rather than names and indices:
// SessionID survives a rename and WindowID survives a move-window renumber,
// so an idle user does not end up looking like a moved target.
func (t Target) same(o Target) bool {
	return t.PaneID == o.PaneID &&
		t.WindowID == o.WindowID &&
		t.SessionID == o.SessionID
}

// Runner executes tmux with the given arguments and returns its combined
// output. It is the seam tests inject fakes through; production wires
// exec.CommandContext.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// Option configures a Resolver.
type Option func(*Resolver)

// WithSocket makes every tmux call go through `tmux -L name` — the named
// socket form, not a filesystem path. The JOH-354 test stand runs its own
// tmux server as `tmux -L heydev`, and the resolver must follow it there or
// it would resolve panes on the user's real server instead.
func WithSocket(name string) Option {
	return func(r *Resolver) { r.socket = name }
}

// WithRunner replaces the tmux invocation. Intended for tests.
func WithRunner(run Runner) Option {
	return func(r *Resolver) { r.run = run }
}

// WithActivityGrace widens what counts as "two clients active at the same
// moment". See DefaultActivityGrace for why the default is one second.
func WithActivityGrace(grace time.Duration) Option {
	return func(r *Resolver) { r.grace = grace }
}

// DefaultActivityGrace is how close two clients' activity timestamps may be
// before Resolve refuses to pick a winner.
//
// The spec (SPEC-HEY-AGENT.md §3) marks two clients with equal activity as
// ambiguous but fixes no grace. One second is chosen because tmux reports
// client_activity in whole seconds: timestamps one tick apart can describe
// the same moment of attention — or up to almost two real seconds — while a
// wider gap is a trustworthy ordering.
const DefaultActivityGrace = time.Second

// Resolver finds the pane the user is looking at. It is safe to keep one for
// the lifetime of the daemon; every call asks tmux afresh.
type Resolver struct {
	run    Runner
	socket string
	grace  time.Duration
}

// New builds a Resolver talking to the default tmux server (or the socket
// given by WithSocket). With the default Runner it fails fast when the tmux
// binary is missing; a fake Runner skips that check entirely.
func New(opts ...Option) (*Resolver, error) {
	r := &Resolver{grace: DefaultActivityGrace}
	for _, opt := range opts {
		opt(r)
	}
	if r.grace < 0 {
		return nil, errors.New("activity grace must not be negative")
	}
	if r.run == nil {
		if _, err := exec.LookPath("tmux"); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoTmux, err)
		}
		r.run = defaultRunner
	}
	return r, nil
}
