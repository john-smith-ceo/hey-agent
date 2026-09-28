package target

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Resolve asks tmux which pane the user is watching right now.
//
// "Watching" is approximated by client_activity. tmux cannot see which
// terminal the window manager has focused — there is no reliable
// "last-focused pane" to ask for — but it does see which terminal the user
// last sent input to, and somebody about to dictate has just pressed keys
// there. Among attached clients the freshest activity wins; clients whose
// activity falls inside the grace window make the choice ambiguous and
// Resolve refuses.
func (r *Resolver) Resolve(ctx context.Context) (Target, error) {
	out, err := r.tmux(ctx, "list-clients", "-F", clientsFormat)
	if err != nil {
		return Target{}, err
	}
	clients, err := parseClients(out)
	if err != nil {
		return Target{}, err
	}
	best, err := pick(clients, r.grace)
	if err != nil {
		return Target{}, err
	}
	return Target{
		PaneID:      best.paneID,
		SessionID:   best.sessionID,
		SessionName: best.sessionName,
		WindowID:    best.windowID,
		WindowIndex: best.windowIndex,
		WindowName:  best.windowName,
		ClientTTY:   best.tty,
		Activity:    time.Unix(best.activity, 0),
	}, nil
}

// Verify checks that prev is still the pane the user is watching.
//
// The daemon resolves twice per recording — once when recording starts and
// once right before send — and this is the second look. Between "recording
// started" and "transcription ready" the user may have switched to another
// window or closed the pane; without this check the text would be pasted
// into a pane that is no longer being looked at, which is a leak.
//
// Resolve errors pass through with their sentinel intact: no attached client
// (ErrNoClient) or fresh ambiguity (ErrAmbiguous) are just as fatal for
// delivery. When the resolve succeeds but points elsewhere, the remembered
// pane is looked up so the two failure stories stay apart — a pane that
// vanished entirely is ErrPaneGone, a pane that is alive but no longer
// watched is ErrTargetMoved.
func (r *Resolver) Verify(ctx context.Context, prev Target) error {
	if prev.PaneID == "" {
		// An empty remembered target is a caller bug. Fail closed rather
		// than verify "nothing" successfully.
		return fmt.Errorf("%w: empty previous target", ErrPaneGone)
	}
	now, err := r.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("re-resolve %s: %w", prev.PaneID, err)
	}
	if now.same(prev) {
		return nil
	}
	alive, err := r.paneExists(ctx, prev.PaneID)
	if err != nil {
		return fmt.Errorf("check pane %s: %w", prev.PaneID, err)
	}
	if !alive {
		return fmt.Errorf("%w: %s", ErrPaneGone, prev)
	}
	return fmt.Errorf("%w: was %s, now %s", ErrTargetMoved, prev, now)
}

// tmux runs tmux with the resolver's socket flags prepended and classifies
// failures. A cancelled context reports the context error rather than a tmux
// problem: a cancelled Resolve is not "tmux is broken".
func (r *Resolver) tmux(ctx context.Context, args ...string) ([]byte, error) {
	out, err := r.run(ctx, r.argv(args)...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		stderr := strings.TrimSpace(string(out))
		if looksLikeNoServer(stderr) {
			return nil, fmt.Errorf("%w: %s", ErrNoClient, stderr)
		}
		if stderr != "" {
			return nil, fmt.Errorf("%w: %v: %s", ErrNoTmux, err, stderr)
		}
		return nil, fmt.Errorf("%w: %v", ErrNoTmux, err)
	}
	return out, nil
}

// argv puts global flags in front of a tmux subcommand, turning
// ("list-clients", "-F", ...) into `tmux -L heydev list-clients -F ...` when
// a socket is configured.
func (r *Resolver) argv(args []string) []string {
	var argv []string
	if r.socket != "" {
		argv = append(argv, "-L", r.socket)
	}
	return append(argv, args...)
}

// paneExists asks the server whether paneID is still alive anywhere — panes
// are addressed by globally unique %ids, so a single list covers every
// session.
func (r *Resolver) paneExists(ctx context.Context, paneID string) (bool, error) {
	out, err := r.tmux(ctx, "list-panes", "-a", "-F", "#{pane_id}")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == paneID {
			return true, nil
		}
	}
	return false, nil
}

// looksLikeNoServer recognises the tmux stderr of "the server is not there".
// A missing server is filed under ErrNoClient rather than ErrNoTmux: tmux
// itself works, there is simply nobody attached who could be watching a
// pane. The match is best-effort — unrecognised failures stay ErrNoTmux,
// which is just as fail-closed for the caller.
func looksLikeNoServer(stderr string) bool {
	for _, pattern := range []string{
		"no server running",
		"error connecting to",
		"failed to connect to server",
	} {
		if strings.Contains(stderr, pattern) {
			return true
		}
	}
	return false
}

func defaultRunner(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "tmux", args...).CombinedOutput()
}
