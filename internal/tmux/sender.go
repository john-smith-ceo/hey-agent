package tmux

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultSubmitDelay is the pause between the paste and the Enter that submits
// it. tmux acknowledges the paste immediately, but the receiving program is
// still digesting the bracketed-paste terminator; an Enter arriving in the same
// burst of events gets swallowed as pasted input instead of acting as submit.
const DefaultSubmitDelay = 250 * time.Millisecond

// Sender delivers text to one explicit tmux pane, and submits it there when
// asked. Submitting is a deliberate choice of the caller, never a default.
type Sender struct {
	target string

	// Socket names an alternate tmux server for `tmux -L name`. Empty talks
	// to the default server — the JOH-354 stand runs its own server, and
	// pasting must not leak onto the user's real one.
	Socket string

	// SubmitDelay is exported so that a slow TUI can be given more room
	// without rebuilding.
	SubmitDelay time.Duration
}

func New(target string) (*Sender, error) {
	if strings.TrimSpace(target) == "" {
		return nil, errors.New("tmux target is empty")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, errors.New("tmux not found")
	}
	return &Sender{target: target, SubmitDelay: DefaultSubmitDelay}, nil
}

func (s *Sender) Target() string { return s.target }

// argv prepends the socket flag when the sender talks to a named server.
func (s *Sender) argv(args ...string) []string {
	if s.Socket != "" {
		return append([]string{"-L", s.Socket}, args...)
	}
	return args
}

func (s *Sender) tmux(ctx context.Context, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "tmux", s.argv(args...)...)
}

func (s *Sender) Check(ctx context.Context) error {
	output, err := s.tmux(ctx, "display-message", "-p", "-t", s.target, "#{pane_id}").CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux target %q is unavailable: %s", s.target, strings.TrimSpace(string(output)))
	}
	return nil
}

func (s *Sender) Send(ctx context.Context, text string, submit bool) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("refusing to send empty transcription")
	}
	buffer, err := bufferName()
	if err != nil {
		return err
	}
	load := s.tmux(ctx, "load-buffer", "-b", buffer, "-")
	load.Stdin = strings.NewReader(text)
	if output, err := load.CombinedOutput(); err != nil {
		return fmt.Errorf("load tmux buffer: %s", strings.TrimSpace(string(output)))
	}
	paste := s.tmux(ctx, "paste-buffer", "-d", "-b", buffer, "-t", s.target)
	if output, err := paste.CombinedOutput(); err != nil {
		return fmt.Errorf("paste into tmux target %q: %s", s.target, strings.TrimSpace(string(output)))
	}
	if !submit {
		return nil
	}
	if s.SubmitDelay > 0 {
		timer := time.NewTimer(s.SubmitDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	enter := s.tmux(ctx, "send-keys", "-t", s.target, "Enter")
	if output, err := enter.CombinedOutput(); err != nil {
		return fmt.Errorf("submit in tmux target %q: %s", s.target, strings.TrimSpace(string(output)))
	}
	return nil
}

func bufferName() (string, error) {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "hey-agent-" + hex.EncodeToString(bytes), nil
}
