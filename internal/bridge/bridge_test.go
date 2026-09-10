package bridge

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/hotkey"
)

type cancelRecorder struct {
	started chan struct{}
	done    chan struct{}
}

func (r *cancelRecorder) Record(ctx context.Context, _ bool) (string, error) {
	close(r.started)
	<-ctx.Done()
	close(r.done)
	return "", ctx.Err()
}

type countingTranscriber struct{ called chan struct{} }

func (t *countingTranscriber) Transcribe(context.Context, string) (string, error) {
	t.called <- struct{}{}
	return "unexpected", nil
}

type fakeSender struct{}

func (fakeSender) Send(context.Context, string, bool) error { return nil }
func (fakeSender) Target() string                           { return "test:0.0" }

func TestHandleCancelStopsRecordingWithoutTranscription(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	states := make(chan string, 2)
	b := &Bridge{
		config:    Config{Log: io.Discard, State: func(value string) { states <- value }},
		recording: true,
		cancel:    cancel,
	}

	b.handle(hotkey.Event{Cancel: true})

	b.mu.Lock()
	recording, activeCancel := b.recording, b.cancel
	b.mu.Unlock()
	if recording || activeCancel != nil {
		t.Fatalf("cancel must clear recording state, recording=%v cancel=%v", recording, activeCancel != nil)
	}
	select {
	case state := <-states:
		if state != "idle" {
			t.Fatalf("cancel must return to idle, got %q", state)
		}
	default:
		t.Fatal("cancel did not publish idle state")
	}
}

func TestCancelNeverCallsTranscriber(t *testing.T) {
	recorder := &cancelRecorder{started: make(chan struct{}), done: make(chan struct{})}
	transcriber := &countingTranscriber{called: make(chan struct{}, 1)}
	b := &Bridge{
		config:     Config{Log: io.Discard},
		recorder:   recorder,
		transcribe: transcriber,
		sender:     fakeSender{},
	}

	b.start(true)
	select {
	case <-recorder.started:
	case <-time.After(time.Second):
		t.Fatal("recording did not start")
	}
	b.handle(hotkey.Event{Cancel: true})
	select {
	case <-recorder.done:
	case <-time.After(time.Second):
		t.Fatal("recording did not cancel")
	}
	select {
	case <-transcriber.called:
		t.Fatal("cancelled recording was transcribed")
	case <-time.After(50 * time.Millisecond):
	}
}
