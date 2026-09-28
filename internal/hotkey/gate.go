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

// RepeatGap separates a real key release from the fake Release+Press pairs
// that keyboard autorepeat injects while a repeatable key stays held. Those
// pairs arrive at the repeat period — about 30 ms at the typical 33 repeats/s —
// far tighter than any human re-press (100 ms and up). So a release is real
// only when no press of the same key follows it within RepeatGap.
//
// Without this, holding Alt_R a fraction past the autorepeat delay fires a
// press/release storm: every fake press toggles recording in tap mode, and the
// overlapping recordings each transcribe and paste — which is exactly how a
// duplicated beginning of the transcribed text appears.
const RepeatGap = 80 * time.Millisecond

// gate turns the raw stream into hotkey events according to the key category.
//
// A free key does nothing on its own, so its presses pass through unchanged and
// hold-to-talk works. A typing key is pressed constantly while writing, so only
// a solo press counts: the key must go down and come back up without any other
// key in between and within SoloTimeout. Such a press is reported once, as a
// single Down event, because there is no way to tell a deliberate hold from
// ordinary typing.
//
// Both categories share one rule: a release is confirmed only RepeatGap later.
// If a press of the same key arrives first, the pair was autorepeat — the key
// never left the finger — and neither edge is reported.
func gate(ctx context.Context, in <-chan raw, key Key, timeout time.Duration) <-chan Event {
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		var pressedAt time.Time
		var dirty bool

		// A pending release waits out RepeatGap: only a timer fire, not the
		// key-up itself, completes the release.
		var upTimer *time.Timer
		var upC <-chan time.Time

		release := func() {
			if key.Category == Free {
				emit(ctx, out, Event{Down: false})
				return
			}
			// A typing key fires on release if the hold stayed solo and short.
			if !pressedAt.IsZero() && !dirty && time.Since(pressedAt) <= timeout {
				emit(ctx, out, Event{Down: true})
			}
			pressedAt = time.Time{}
		}

		// flushUp settles a deferred release now: another key going down while
		// the timer runs proves the target really left the finger — autorepeat
		// would only ever inject a press of the target itself.
		flushUp := func() {
			if upTimer == nil {
				return
			}
			upTimer.Stop()
			upTimer, upC = nil, nil
			release()
		}
		defer func() {
			if upTimer != nil {
				upTimer.Stop()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case <-upC:
				upTimer, upC = nil, nil
				release()
			case event, ok := <-in:
				if !ok {
					return
				}
				if event.cancel {
					emit(ctx, out, Event{Cancel: true})
					continue
				}
				if event.target {
					if event.down {
						if upTimer != nil {
							// A press inside RepeatGap of the release is an
							// autorepeat continuation: swallow the pair and keep
							// the original hold — pressedAt and dirty stay.
							upTimer.Stop()
							upTimer, upC = nil, nil
							continue
						}
						if key.Category == Free {
							emit(ctx, out, Event{Down: true})
						} else {
							pressedAt, dirty = time.Now(), false
						}
					} else {
						// Defer the release; a matching press may still prove
						// it was autorepeat rather than a real key-up.
						if upTimer != nil {
							upTimer.Stop()
						}
						upTimer = time.NewTimer(RepeatGap)
						upC = upTimer.C
					}
					continue
				}
				// Another key went down. If a release was pending, it is real —
				// flush it before marking the (already finished) hold dirty.
				flushUp()
				// Another key down while the modifier is held means the
				// modifier was doing its normal job, like Shift for a capital.
				dirty = true
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
