package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/hotkey"
	"github.com/john-smith-ceo/hey-agent/internal/record"
	"github.com/john-smith-ceo/hey-agent/internal/tmux"
	"github.com/john-smith-ceo/hey-agent/internal/transcribe"
)

type Mode string

const (
	Tap  Mode = "tap"
	Push Mode = "push"
)

type Config struct {
	Mode       Mode
	Silence    time.Duration
	Device     string
	APIKey     string
	BaseURL    string
	Model      string
	Log        io.Writer
	State      func(string)
	TmuxTarget string
	Key        hotkey.Key

	// BusyFile is touched while the microphone is recording. Anything that
	// speaks out loud can watch it and hold its tongue: a voice-over talking
	// into the same room ends up inside the recording.
	BusyFile string
	// OnRecord runs once when recording starts — to silence whatever is
	// already speaking, which a file alone cannot do.
	OnRecord string

	// Submit presses Enter after the transcription lands. It is deliberately
	// independent of Mode: how a recording is started and what happens to its
	// result are separate questions, and the most useful combination — hold the
	// key, release it, watch it go — needs both to be free.
	Submit bool
	// SubmitDelay overrides the pause before Enter for a slow application.
	SubmitDelay time.Duration
	// RuntimeConfig is an atomically-written JSON file reloaded between recordings.
	RuntimeConfig string

	// Listener, when set, replaces the platform hotkey listener built from
	// Key. The daemon injects its bind/unbind gate here so the grab can be
	// released without rebuilding the pipeline; nil means hotkey.New as before.
	Listener hotkey.Listener
	// OnEvent runs synchronously before each hotkey event is handled. The
	// daemon uses it to hush voice output before a recording starts; nil means
	// no hook.
	OnEvent func(hotkey.Event)
	// StateSeq, when set, receives the same state strings as State but tagged
	// with the recording session that produced them. A consumer (the daemon
	// FSM) can then drop news from an abandoned session: a previous
	// recording's late "pasted" must not mark a newer recording idle.
	StateSeq func(seq uint64, state string)

	// Sender replaces the fixed tmux sender when set — the daemon hands over
	// the resolver-backed sender here. nil → TmuxTarget is used.
	Sender Sender
}

type Bridge struct {
	config     Config
	hotkey     hotkey.Listener
	recorder   record.Recorder
	transcribe transcribe.Client
	sender     Sender

	settingsMu sync.RWMutex
	mode       Mode
	silence    time.Duration
	submit     bool

	mu        sync.Mutex
	recording bool
	// inflight stays set until a finished recording has been transcribed and
	// delivered. Recording is already false by then, so without this flag a
	// quick second press could start a new recording while the previous
	// transcription is still being pasted — two overlapping sessions then
	// deliver into the pane, which reads as duplicated text.
	inflight bool
	cancel   context.CancelFunc
	session  uint64
}

// Sender is the delivery port: one resolved tmux pane per Send. Exported so
// the daemon can inject the resolver-backed sender (internal/target) which
// re-checks the pane before every delivery (SPEC §3 fail-closed).
type Sender interface {
	Send(context.Context, string, bool) error
	Target() string
}

// sessionStarter is an optional sender capability: BeginRecording runs before
// the microphone opens, so a fail-closed resolver error stops the recording
// instead of discovering "nowhere to paste" after the user already spoke.
type sessionStarter interface {
	BeginRecording(context.Context) error
}

func New(config Config) (*Bridge, error) {
	if config.Log == nil {
		config.Log = io.Discard
	}
	if config.Mode != Tap && config.Mode != Push {
		return nil, fmt.Errorf("unsupported mode %q", config.Mode)
	}
	if config.Silence <= 0 {
		return nil, errors.New("silence timeout must be positive")
	}
	if config.Mode == Push && !config.Key.SupportsPush() {
		return nil, fmt.Errorf("%s is used while typing, so hold-to-talk cannot be told apart from ordinary use; run it with --mode tap", config.Key.Name)
	}
	listener := config.Listener
	if listener == nil {
		platformListener, err := hotkey.New(config.Key)
		if err != nil {
			return nil, err
		}
		listener = platformListener
	}
	sender := config.Sender
	if sender == nil {
		fixed, err := tmux.New(config.TmuxTarget)
		if err != nil {
			return nil, err
		}
		if config.SubmitDelay > 0 {
			fixed.SubmitDelay = config.SubmitDelay
		}
		if err := fixed.Check(context.Background()); err != nil {
			return nil, err
		}
		sender = fixed
	}
	return &Bridge{
		config:   config,
		hotkey:   listener,
		recorder: record.NewFFmpeg(config.Device, config.Silence),
		transcribe: transcribe.NewProvider(transcribe.Config{
			APIKey:  config.APIKey,
			BaseURL: config.BaseURL,
			Model:   config.Model,
		}),
		sender:  sender,
		mode:    config.Mode,
		silence: config.Silence,
		submit:  config.Submit,
	}, nil
}

func (b *Bridge) Run(parent context.Context) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	events, err := b.hotkey.Start(ctx)
	if err != nil {
		return err
	}
	if b.config.RuntimeConfig != "" {
		go b.watchRuntimeConfig(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			b.stop(false)
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return errors.New("global hotkey listener stopped")
			}
			b.handle(event)
		}
	}
}

func (b *Bridge) handle(event hotkey.Event) {
	// The daemon's hook goes first: a press that hushes speech must run before
	// start() opens the microphone, or the dying phrase lands in the recording.
	if b.config.OnEvent != nil {
		b.config.OnEvent(event)
	}
	// Escape — отмена: запись гаснет без транскрибации, независимо от режима.
	if event.Cancel {
		b.stop(false)
		return
	}
	switch b.currentMode() {
	case Push:
		if event.Down {
			b.start(false)
		} else {
			b.stop(true)
		}
	case Tap:
		if event.Down {
			b.mu.Lock()
			running := b.recording
			b.mu.Unlock()
			if running {
				b.stop(true)
			} else {
				b.start(true)
			}
		}
	}
}

func (b *Bridge) start(autoStop bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recording || b.inflight {
		return
	}
	// Fail-closed resolve BEFORE the microphone opens: when the target cannot
	// be determined the press is dropped with a visible error — recording
	// speech first and discovering "nowhere to paste" afterwards is worse.
	if starter, ok := b.sender.(sessionStarter); ok {
		if err := starter.BeginRecording(context.Background()); err != nil {
			// This press never became a recording, but it still gets its own
			// seq: tagging the error with the previous session would let a
			// seq-aware consumer drop it as stale.
			b.session++
			b.fail(b.session, "no recording target:", err)
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	recorder, submit := b.currentRecordingSettings()
	b.session++
	session := b.session
	b.recording, b.inflight, b.cancel = true, true, cancel
	fmt.Fprintln(b.config.Log, "recording started")
	b.stateFor(session, "recording")
	b.markBusy()
	go func() {
		defer func() {
			b.mu.Lock()
			b.inflight = false
			b.mu.Unlock()
		}()
		startedAt := time.Now()
		file, err := recorder.Record(ctx, autoStop)
		recorded := time.Since(startedAt)
		b.clearBusy()
		b.mu.Lock()
		discard := b.session != session
		if b.session == session {
			b.recording, b.cancel = false, nil
		}
		b.mu.Unlock()
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				b.fail(session, "recording failed:", err)
			}
			return
		}
		defer os.Remove(file)
		if discard {
			b.stateFor(session, "idle")
			return
		}
		fmt.Fprintln(b.config.Log, "transcribing")
		b.stateFor(session, "transcribing")
		transcribeStart := time.Now()
		text, err := b.transcribe.Transcribe(context.Background(), file)
		transcribed := time.Since(transcribeStart)
		if err != nil {
			b.fail(session, "transcription failed:", err)
			return
		}
		b.stateFor(session, "delivering")
		deliverStart := time.Now()
		if err := b.sender.Send(context.Background(), text, submit); err != nil {
			b.fail(session, "tmux delivery failed:", err)
			return
		}
		delivered := time.Since(deliverStart)
		// Where the wait actually goes is a question that gets asked often and
		// answered by feel; these three numbers answer it with facts.
		fmt.Fprintf(b.config.Log, "timing: record %s, transcribe %s, deliver %s, chars %d\n",
			round(recorded), round(transcribed), round(delivered), len(text))
		if submit {
			fmt.Fprintf(b.config.Log, "transcription delivered and submitted to tmux %s\n", b.sender.Target())
		} else {
			fmt.Fprintf(b.config.Log, "transcription delivered to tmux %s; review it and press Enter yourself\n", b.sender.Target())
		}
		b.stateFor(session, "pasted")
		time.AfterFunc(1500*time.Millisecond, func() { b.stateFor(session, "idle") })
	}()
}

type runtimeSettings struct {
	Mode    string `json:"mode"`
	Silence string `json:"silence"`
	Submit  bool   `json:"submit"`
}

func (b *Bridge) currentMode() Mode {
	b.settingsMu.RLock()
	defer b.settingsMu.RUnlock()
	return b.mode
}

func (b *Bridge) currentRecordingSettings() (record.Recorder, bool) {
	b.settingsMu.RLock()
	defer b.settingsMu.RUnlock()
	return b.recorder, b.submit
}

func (b *Bridge) watchRuntimeConfig(ctx context.Context) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last runtimeSettings
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data, err := os.ReadFile(b.config.RuntimeConfig)
			if err != nil {
				continue
			}
			var settings runtimeSettings
			if err := json.Unmarshal(data, &settings); err != nil || settings == last {
				continue
			}
			if settings.Mode != string(Tap) && settings.Mode != string(Push) {
				continue
			}
			if settings.Mode == string(Push) && !b.config.Key.SupportsPush() {
				continue
			}
			silence, err := time.ParseDuration(settings.Silence)
			if err != nil || silence <= 0 {
				continue
			}
			b.mu.Lock()
			recording := b.recording
			b.mu.Unlock()
			if recording {
				continue
			}
			b.settingsMu.Lock()
			b.mode = Mode(settings.Mode)
			b.silence = silence
			b.submit = settings.Submit
			b.recorder = record.NewFFmpeg(b.config.Device, silence)
			b.settingsMu.Unlock()
			last = settings
			fmt.Fprintf(b.config.Log, "runtime settings: mode=%s silence=%s submit=%t\n", settings.Mode, settings.Silence, settings.Submit)
		}
	}
}

func (b *Bridge) stop(transcribe bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.recording || b.cancel == nil {
		return
	}
	fmt.Fprintln(b.config.Log, "recording stopped")
	if transcribe {
		b.state("transcribing")
	} else {
		b.state("idle")
	}
	b.cancel()
	if !transcribe {
		b.session++
		b.recording, b.cancel = false, nil
	}
}

// fail reports a failure and then lets the indicator settle back to idle. A
// status line borrowed from the user must not keep a red dot forever.
func (b *Bridge) fail(session uint64, message string, err error) {
	fmt.Fprintln(b.config.Log, message, err)
	b.stateFor(session, "error")
	time.AfterFunc(4*time.Second, func() { b.stateFor(session, "idle") })
}

// markBusy raises the microphone flag and hushes anything already speaking.
// The flag carries no content: watchers read its presence and its age, and a
// recording never runs long enough to look abandoned.
func (b *Bridge) markBusy() {
	if b.config.BusyFile != "" {
		if err := os.MkdirAll(filepath.Dir(b.config.BusyFile), 0o755); err == nil {
			if file, err := os.Create(b.config.BusyFile); err == nil {
				_ = file.Close()
			}
		}
	}
	if b.config.OnRecord == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "sh", "-c", b.config.OnRecord).CombinedOutput(); err != nil {
		fmt.Fprintf(b.config.Log, "on-record command failed: %v: %s\n", err, strings.TrimSpace(string(output)))
	}
}

func (b *Bridge) clearBusy() {
	if b.config.BusyFile == "" {
		return
	}
	if err := os.Remove(b.config.BusyFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(b.config.Log, "clear microphone flag:", err)
	}
}

func round(d time.Duration) time.Duration { return d.Round(10 * time.Millisecond) }

func (b *Bridge) state(value string) {
	if b.config.State != nil {
		b.config.State(value)
	}
}

// stateFor reports a state that belongs to one recording session. States that
// are commands rather than pipeline output — the "idle"/"transcribing" emitted
// by stop() — stay untagged: they apply whichever session is current.
func (b *Bridge) stateFor(seq uint64, value string) {
	if b.config.StateSeq != nil {
		b.config.StateSeq(seq, value)
	}
	b.state(value)
}

// Target reports the pane this bridge delivers to; the daemon answers
// `status` with it.
func (b *Bridge) Target() string { return b.sender.Target() }
