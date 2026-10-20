package bridge

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
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

// gateSender adds the optional BeginRecording hook: when it fails, the press
// must be dropped before the microphone ever opens. report, when set, hears
// a "resolve" marker so a test can check call ordering.
type gateSender struct {
	beginErr error
	begun    chan struct{}
	report   chan<- string
}

func (s *gateSender) Send(context.Context, string, bool) error { return nil }
func (s *gateSender) Target() string                           { return "test:0.0" }
func (s *gateSender) BeginRecording(context.Context) error {
	if s.report != nil {
		s.report <- "resolve"
	}
	select {
	case s.begun <- struct{}{}:
	default:
	}
	return s.beginErr
}

// A failed resolve must drop the press: no recording, no audio consumed, and
// a visible error so the user knows why the hotkey did nothing.
func TestStartFailsClosedWhenResolveFails(t *testing.T) {
	recorder := &okRecorder{calls: make(chan struct{}, 1)}
	sender := &gateSender{beginErr: errors.New("no attached tmux client"), begun: make(chan struct{}, 1)}
	states := make(chan string, 4)
	b := &Bridge{
		config:     Config{Log: io.Discard, State: func(v string) { states <- v }},
		recorder:   recorder,
		transcribe: &countingTranscriber{called: make(chan struct{}, 1)},
		sender:     sender,
		audible:    func(string) (bool, string) { return true, "" },
	}

	b.start(true)

	select {
	case <-sender.begun:
	case <-time.After(time.Second):
		t.Fatal("BeginRecording was not consulted")
	}
	select {
	case <-recorder.calls:
		t.Fatal("recording started despite a failed resolve")
	case <-time.After(100 * time.Millisecond):
	}
	var got string
	select {
	case got = <-states:
	case <-time.After(time.Second):
		t.Fatal("no state published for a dropped press")
	}
	if got != "error" {
		t.Fatalf("state = %q, want error", got)
	}
	b.mu.Lock()
	recording, inflight := b.recording, b.inflight
	b.mu.Unlock()
	if recording || inflight {
		t.Fatalf("press left bridge busy: recording=%v inflight=%v", recording, inflight)
	}
}

// The resolve must happen BEFORE the recorder opens — order is the contract.
func TestBeginRecordingRunsBeforeMicrophone(t *testing.T) {
	order := make(chan string, 2)
	recorder := &orderRecorder{order: order}
	sender := &gateSender{begun: make(chan struct{}, 1), report: order}
	b := &Bridge{
		config:     Config{Log: io.Discard},
		recorder:   recorder,
		transcribe: transcribeFunc(func(context.Context, string) (string, error) { return "", nil }),
		sender:     sender,
		audible:    func(string) (bool, string) { return true, "" },
	}
	b.start(true)
	first := <-order
	if first != "resolve" {
		t.Fatalf("first pipeline step = %q, want resolve before record", first)
	}
	select {
	case <-order:
	case <-time.After(time.Second):
		t.Fatal("recording did not start after a successful resolve")
	}
	time.Sleep(50 * time.Millisecond)
}

// orderRecorder reports into order when Record runs.
type orderRecorder struct{ order chan<- string }

func (r *orderRecorder) Record(context.Context, bool) (string, error) {
	r.order <- "record"
	return "", errors.New("stop here")
}

type transcribeFunc func(context.Context, string) (string, error)

func (f transcribeFunc) Transcribe(ctx context.Context, path string) (string, error) {
	return f(ctx, path)
}

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

type okRecorder struct{ calls chan struct{} }

func (r *okRecorder) Record(context.Context, bool) (string, error) {
	r.calls <- struct{}{}
	file, err := os.CreateTemp("", "hey-agent-test-*.wav")
	if err != nil {
		return "", err
	}
	defer file.Close()
	// send/Stat checks require a non-trivial payload.
	if _, err := file.Write(make([]byte, 2048)); err != nil {
		return "", err
	}
	return file.Name(), nil
}

type blockingTranscriber struct {
	mu      sync.Mutex
	release chan struct{}
}

// gate reads the current release channel under a lock: the test rearms it
// while a pipeline goroutine may still be parked in the previous call, so an
// unguarded field read would race the rearm.
func (t *blockingTranscriber) Transcribe(context.Context, string) (string, error) {
	t.mu.Lock()
	release := t.release
	t.mu.Unlock()
	<-release
	return "text", nil
}

// open closes the current release channel under the same lock, unblocking
// whichever Transcribe call holds it.
func (t *blockingTranscriber) open() {
	t.mu.Lock()
	release := t.release
	t.mu.Unlock()
	close(release)
}

func (t *blockingTranscriber) reset() {
	t.mu.Lock()
	t.release = make(chan struct{})
	t.mu.Unlock()
}

// A second press while the previous transcription is still being delivered
// must not spawn another pipeline — otherwise two recordings overlap and
// both paste, which is how the duplicated text defect showed up.
func TestStartIgnoredWhilePreviousDeliveryInFlight(t *testing.T) {
	recorder := &okRecorder{calls: make(chan struct{}, 4)}
	transcriber := &blockingTranscriber{release: make(chan struct{})}
	b := &Bridge{
		config:     Config{Log: io.Discard},
		recorder:   recorder,
		transcribe: transcriber,
		sender:     fakeSender{},
	}

	b.start(true)
	select {
	case <-recorder.calls:
	case <-time.After(time.Second):
		t.Fatal("first recording did not start")
	}
	// The first run is now parked in Transcribe. A second start must be
	// refused while that pipeline is in flight.
	b.start(true)
	select {
	case <-recorder.calls:
		t.Fatal("second start spawned a pipeline while the first was inflight")
	case <-time.After(100 * time.Millisecond):
	}
	transcriber.open()
	transcriber.reset() // arm the blocker for run two
	// Once delivery finished, the next press records again.
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		b.mu.Lock()
		free := !b.inflight && !b.recording
		b.mu.Unlock()
		if free {
			break
		}
	}
	b.start(true)
	select {
	case <-recorder.calls:
	case <-time.After(time.Second):
		t.Fatal("third start should record after the first pipeline finished")
	}
	transcriber.open()
	time.Sleep(50 * time.Millisecond)
}

// countingSender records every Send so exactly-once is an assertion, not a
// hope: one recording must deliver one transcript, never two.
type countingSender struct {
	mu    sync.Mutex
	calls []string
}

func (s *countingSender) Send(_ context.Context, text string, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, text)
	return nil
}

func (s *countingSender) Target() string { return "test:0.0" }

func (s *countingSender) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// One finished recording delivers its transcript exactly once — the pane must
// not see the same text pasted twice, however the pipeline is prodded.
func TestOneRecordingDeliversExactlyOnce(t *testing.T) {
	recorder := &okRecorder{calls: make(chan struct{}, 1)}
	sender := &countingSender{}
	done := make(chan struct{}, 1)
	b := &Bridge{
		config: Config{Log: io.Discard, State: func(v string) {
			if v == "pasted" {
				select {
				case done <- struct{}{}:
				default:
				}
			}
		}},
		recorder:   recorder,
		transcribe: transcribeFunc(func(context.Context, string) (string, error) { return "один раз", nil }),
		sender:     sender,
		audible:    func(string) (bool, string) { return true, "" },
	}

	b.start(true)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not finish")
	}
	if got := sender.sent(); len(got) != 1 || got[0] != "один раз" {
		t.Fatalf("Send calls = %v, want exactly [один раз]", got)
	}
	// The pipeline is done; a fresh press is a new session and may send again.
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		b.mu.Lock()
		free := !b.inflight && !b.recording
		b.mu.Unlock()
		if free {
			return
		}
	}
	t.Fatal("inflight never cleared after a delivered transcript")
}

// A provider error must surface as the error state and must NOT deliver
// anything — a half transcript pasted would look like a hallucination.
func TestTranscribeErrorDeliversNothing(t *testing.T) {
	recorder := &okRecorder{calls: make(chan struct{}, 1)}
	sender := &countingSender{}
	states := make(chan string, 8)
	b := &Bridge{
		config:     Config{Log: io.Discard, State: func(v string) { states <- v }},
		recorder:   recorder,
		transcribe: transcribeFunc(func(context.Context, string) (string, error) { return "", errors.New("provider 500") }),
		sender:     sender,
		audible:    func(string) (bool, string) { return true, "" },
	}

	b.start(true)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-states:
			if got == "error" {
				if sent := sender.sent(); len(sent) != 0 {
					t.Fatalf("transcribe error still delivered %v", sent)
				}
				return
			}
		case <-deadline:
			t.Fatal("error state never published after a provider failure")
		}
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
