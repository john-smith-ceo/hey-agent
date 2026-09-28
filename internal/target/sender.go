package target

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/tmux"
)

// ErrUnbound means Send was asked to deliver without a preceding
// BeginRecording — a caller bug; failing closed beats guessing a pane.
var ErrUnbound = errors.New("no pane bound to this recording")

// Sender is the fail-closed delivery path behind bridge.Sender (SPEC §3):
// every recording resolves its pane fresh and, after the transcription lands,
// verifies that the user is still looking at that pane before pasting.
//
// Two checks on purpose: Resolve at record start (the press is dropped when
// nowhere to type) and Verify just before Send — the seconds of speech and
// transcription are exactly when the user switches panes, and pasting into a
// stale pane is a leak, not a nuisance.
type Sender struct {
	res *Resolver

	// SubmitDelay is handed to each per-send tmux.Sender.
	SubmitDelay time.Duration

	// Socket names the tmux server the inner senders dial (`tmux -L name`) —
	// it must match the resolver's socket or resolve and paste would talk to
	// different servers.
	Socket string

	// paste substitutes the tmux delivery in tests — the verify-then-send
	// logic is what needs coverage, not the paste-buffer plumbing.
	paste func(ctx context.Context, pane, text string, submit bool) error

	mu    sync.Mutex
	bound Target
	have  bool
}

// NewSender wraps a Resolver as a recording-scoped sender.
func NewSender(res *Resolver) *Sender {
	return &Sender{res: res, SubmitDelay: tmux.DefaultSubmitDelay}
}

// BeginRecording resolves the pane the user is looking at right now and binds
// the coming Send to it. bridge.start calls this before opening the
// microphone, so a resolve failure drops the press instead of wasting a
// recording.
func (s *Sender) BeginRecording(ctx context.Context) error {
	t, err := s.res.Resolve(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.bound, s.have = Target{}, false
		return err
	}
	s.bound, s.have = t, true
	return nil
}

// Send re-verifies the bound pane, then pastes. Verify failure means the
// transcript is dropped — it is never written anywhere else.
func (s *Sender) Send(ctx context.Context, text string, submit bool) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("refusing to send empty transcription")
	}
	s.mu.Lock()
	bound, have := s.bound, s.have
	s.mu.Unlock()
	if !have {
		return ErrUnbound
	}
	if err := s.res.Verify(ctx, bound); err != nil {
		return fmt.Errorf("delivery refused, target moved: %w", err)
	}
	if s.paste != nil {
		return s.paste(ctx, bound.PaneID, text, submit)
	}
	inner, err := tmux.New(bound.PaneID)
	if err != nil {
		return err
	}
	inner.Socket = s.Socket
	inner.SubmitDelay = s.SubmitDelay
	return inner.Send(ctx, text, submit)
}

// Target reports the pane currently bound for delivery, or "" before the
// first recording resolves one. Status reads it, so "" is a valid answer.
func (s *Sender) Target() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.have {
		return ""
	}
	return s.bound.PaneID
}

// BoundTarget returns the full resolved target — the status-line publisher
// reads the session name out of it to know whose status-right to borrow.
func (s *Sender) BoundTarget() (Target, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bound, s.have
}
