package tray

import "testing"

// Every daemon state must map to its spec icon name (SPEC §6:
// idle/rec/transcribe/speaking/error) with actual pixels behind it.
func TestIconForState_AllStates(t *testing.T) {
	want := map[State]string{
		StateIdle:         "idle",
		StateRecording:    "rec",
		StateTranscribing: "transcribe",
		StateSpeaking:     "speaking",
		StateError:        "error",
	}
	for state, name := range want {
		icon := IconForState(state)
		if icon.Name != name {
			t.Errorf("IconForState(%q).Name = %q, want %q", state, icon.Name, name)
		}
		if len(icon.Pixmaps) != len(iconSizes) {
			t.Fatalf("IconForState(%q): %d pixmaps, want %d", state, len(icon.Pixmaps), len(iconSizes))
		}
		for _, p := range icon.Pixmaps {
			if len(p.ARGB) != p.Width*p.Height*4 {
				t.Fatalf("%q %dx%d: ARGB len %d, want %d", name, p.Width, p.Height, len(p.ARGB), p.Width*p.Height*4)
			}
		}
	}
}

// States carry distinct colors — a red dot that looks like the grey one
// defeats the point of having an icon.
func TestIconForState_DistinctColors(t *testing.T) {
	center := func(s State) (a, r, g, b byte) {
		p := IconForState(s).Pixmaps[0]
		mid := (p.Height/2*p.Width + p.Width/2) * 4
		return p.ARGB[mid], p.ARGB[mid+1], p.ARGB[mid+2], p.ARGB[mid+3]
	}
	type px struct{ a, r, g, b byte }
	seen := map[px]State{}
	for _, s := range []State{StateIdle, StateRecording, StateTranscribing, StateSpeaking, StateError} {
		a, r, g, b := center(s)
		if a != 255 {
			t.Errorf("%s: center alpha %d, want 255", s, a)
		}
		key := px{a, r, g, b}
		if dup, ok := seen[key]; ok {
			t.Errorf("%s: same center pixel as %s", s, dup)
		}
		seen[key] = s
	}
}

// Unknown state → idle fallback: the icon must never come back blank.
func TestIconForState_UnknownFallsBackToIdle(t *testing.T) {
	for _, s := range []State{"bogus", "", "RECORDING"} {
		if icon := IconForState(s); icon.Name != "idle" {
			t.Errorf("IconForState(%q).Name = %q, want idle", s, icon.Name)
		}
	}
}

// The dot must have an opaque interior and a transparent outside — that is
// what makes it a circle and not a square.
func TestCirclePixmap_Geometry(t *testing.T) {
	p := circlePixmap(24, 0xE5, 0x39, 0x35)
	at := func(x, y int) byte { return p.ARGB[(y*p.Width+x)*4] } // alpha
	if at(12, 12) != 255 {
		t.Errorf("center alpha = %d, want 255", at(12, 12))
	}
	for _, c := range [][2]int{{0, 0}, {23, 0}, {0, 23}, {23, 23}} {
		if at(c[0], c[1]) != 0 {
			t.Errorf("corner %v alpha = %d, want 0", c, at(c[0], c[1]))
		}
	}
}

// Status mapping: only errors raise NeedsAttention; everything else stays
// Active (Passive would hide the icon entirely).
func TestStatusForState(t *testing.T) {
	for _, s := range []State{StateIdle, StateRecording, StateTranscribing, StateSpeaking} {
		if got := statusForState(s); got != "Active" {
			t.Errorf("statusForState(%q) = %q, want Active", s, got)
		}
	}
	if got := statusForState(StateError); got != "NeedsAttention" {
		t.Errorf("statusForState(error) = %q, want NeedsAttention", got)
	}
}
