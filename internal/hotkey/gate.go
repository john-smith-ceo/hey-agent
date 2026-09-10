package hotkey

import (
	"context"
	"time"
)

// raw is one keyboard event as the platform listener sees it. Only three facts
// matter: whether it belongs to the configured key, whether it is a press, and
// whether it is the cancel key — Escape, which aborts the recording instead of
// finishing it.
type raw struct {
	target bool
	down   bool
	cancel bool
}

// SoloTimeout bounds how long a typing modifier may stay down and still count
// as a deliberate solo press rather than an ordinary Shift for a capital.
const SoloTimeout = 400 * time.Millisecond

// gate turns the raw stream into hotkey events according to the key category.
//
// A free key does nothing on its own, so its presses pass through unchanged and
// hold-to-talk works. A typing key is pressed constantly while writing, so only
// a solo press counts: the key must go down and come back up without any other
// key in between and within SoloTimeout. Such a press is reported once, as a
// single Down event, because there is no way to tell a deliberate hold from
// ordinary typing.
func gate(ctx context.Context, in <-chan raw, key Key, timeout time.Duration) <-chan Event {
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var pressedAt time.Time
		var dirty bool
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-in:
				if !ok {
					return
				}
				if event.cancel {
					emit(ctx, out, Event{Cancel: true})
					continue
				}
				if key.Category == Free {
					if event.target {
						emit(ctx, out, Event{Down: event.down})
					}
					continue
				}
				switch {
				case event.target && event.down:
					pressedAt, dirty = time.Now(), false
				case event.target && !event.down:
					if !pressedAt.IsZero() && !dirty && time.Since(pressedAt) <= timeout {
						emit(ctx, out, Event{Down: true})
					}
					pressedAt = time.Time{}
				case event.down:
					// Another key went down while the modifier was held, so the
					// modifier was doing its normal job.
					dirty = true
				}
			}
		}
	}()
	return out
}

func emit(ctx context.Context, out chan<- Event, event Event) {
	select {
	case out <- event:
	case <-ctx.Done():
	default:
	}
}
