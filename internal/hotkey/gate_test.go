package hotkey

import (
	"context"
	"testing"
	"time"
)

func drain(t *testing.T, out <-chan Event, want int) []Event {
	t.Helper()
	var got []Event
	deadline := time.After(time.Second)
	for len(got) < want {
		select {
		case event, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, event)
		case <-deadline:
			return got
		}
	}
	// Give a stray extra event a chance to show up before declaring success.
	select {
	case event := <-out:
		got = append(got, event)
	case <-time.After(50 * time.Millisecond):
	}
	return got
}

func TestGateFreeKeyPassesPressAndRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 4)
	out := gate(ctx, in, Key{Name: "Alt_R", Category: Free}, SoloTimeout)
	in <- raw{target: true, down: true}
	in <- raw{target: true, down: false}
	got := drain(t, out, 2)
	if len(got) != 2 || !got[0].Down || got[1].Down {
		t.Fatalf("expected a press then a release, got %+v", got)
	}
}

func TestGateTypingKeyReportsSoloPress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 4)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, SoloTimeout)
	in <- raw{target: true, down: true}
	in <- raw{target: true, down: false}
	got := drain(t, out, 1)
	if len(got) != 1 || !got[0].Down {
		t.Fatalf("expected one press, got %+v", got)
	}
}

func TestGateTypingKeyIgnoresShiftedCharacter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 4)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, SoloTimeout)
	// Shift held down, a letter typed, Shift released: an ordinary capital.
	in <- raw{target: true, down: true}
	in <- raw{down: true}
	in <- raw{target: true, down: false}
	if got := drain(t, out, 1); len(got) != 0 {
		t.Fatalf("a capital letter must not trigger the hotkey, got %+v", got)
	}
}

func TestGateTypingKeyIgnoresLongHold(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 4)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, 20*time.Millisecond)
	in <- raw{target: true, down: true}
	time.Sleep(60 * time.Millisecond)
	in <- raw{target: true, down: false}
	if got := drain(t, out, 1); len(got) != 0 {
		t.Fatalf("a long hold must not trigger the hotkey, got %+v", got)
	}
}

func TestGateEscapeEmitsCancelWithoutHotkeyEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 2)
	out := gate(ctx, in, Key{Name: "Alt_R", Category: Free}, SoloTimeout)
	in <- raw{cancel: true, down: true}
	got := drain(t, out, 1)
	if len(got) != 1 || !got[0].Cancel || got[0].Down {
		t.Fatalf("expected one cancel event, got %+v", got)
	}
}

// repeatBurst replays what X11 autorepeat produces for a held key: a Release
// immediately followed by a Press, over and over, at the repeat period.
func repeatBurst(in chan<- raw, pairs int) {
	for i := 0; i < pairs; i++ {
		in <- raw{target: true, down: false}
		in <- raw{target: true, down: true}
		time.Sleep(30 * time.Millisecond)
	}
	in <- raw{target: true, down: false}
}

func TestGateFreeKeySwallowsAutorepeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 16)
	out := gate(ctx, in, Key{Name: "Alt_R", Category: Free}, SoloTimeout)
	in <- raw{target: true, down: true}
	go repeatBurst(in, 3)
	got := drain(t, out, 2)
	if len(got) != 2 || !got[0].Down || got[1].Down {
		t.Fatalf("autorepeat must collapse to one press and one release, got %+v", got)
	}
}

func TestGateTypingKeySwallowsAutorepeat(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 16)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, SoloTimeout)
	in <- raw{target: true, down: true}
	repeatBurst(in, 2)
	got := drain(t, out, 1)
	if len(got) != 1 || !got[0].Down {
		t.Fatalf("a held-but-repeating solo press must report once, got %+v", got)
	}
}

func TestGateTypingKeyAutorepeatHoldIsNotSolo(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 16)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, 60*time.Millisecond)
	in <- raw{target: true, down: true}
	// Hold well past the solo timeout; every repeat pair keeps the hold alive.
	repeatBurst(in, 3)
	if got := drain(t, out, 1); len(got) != 0 {
		t.Fatalf("a long hold with autorepeat must not trigger, got %+v", got)
	}
}

func TestGateDistinctTapsStillCount(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 16)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, SoloTimeout)
	in <- raw{target: true, down: true}
	in <- raw{target: true, down: false}
	time.Sleep(150 * time.Millisecond) // human re-press gap, past RepeatGap
	in <- raw{target: true, down: true}
	in <- raw{target: true, down: false}
	got := drain(t, out, 2)
	if len(got) != 2 || !got[0].Down || !got[1].Down {
		t.Fatalf("two deliberate taps must report two presses, got %+v", got)
	}
}

func TestGateKeyAfterReleaseFlushesPress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan raw, 16)
	out := gate(ctx, in, Key{Name: "Shift_L", Category: Typing}, SoloTimeout)
	in <- raw{target: true, down: true}
	in <- raw{target: true, down: false}
	in <- raw{down: true} // a letter right after releasing the modifier
	got := drain(t, out, 1)
	if len(got) != 1 || !got[0].Down {
		t.Fatalf("release followed by typing still counts as a solo press, got %+v", got)
	}
}
