package tray

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveSmoke exercises the real session bus when HEY_AGENT_TRAY_LIVE=1.
// Temporary manual check for JOH-360 — skipped by default.
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("HEY_AGENT_TRAY_LIVE") != "1" {
		t.Skip("set HEY_AGENT_TRAY_LIVE=1 to run against the real bus")
	}
	tr, err := New(Config{
		Actions: Actions{
			Quit: func() { t.Log("quit clicked") },
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.Run(ctx)

	for _, s := range []State{StateRecording, StateTranscribing, StateSpeaking, StateError, StateIdle} {
		tr.SetState(s)
		time.Sleep(1200 * time.Millisecond)
	}
	tr.SetToggles(Toggles{Bound: true, Submit: true})
	time.Sleep(1200 * time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
