package hotkey

import "context"

// Event is one thing the listener observed. Down is a press of the configured
// hotkey; Cancel is a press of the cancel key — Escape — and reports that the
// user wants the current recording dropped rather than transcribed.
type Event struct {
	Down   bool
	Cancel bool
}

type Listener interface {
	Start(context.Context) (<-chan Event, error)
}

// Validate reports whether this machine can listen for the key at all, so a
// bad choice is refused before anything else is set up.
func Validate(key Key) error {
	_, err := New(key)
	return err
}
