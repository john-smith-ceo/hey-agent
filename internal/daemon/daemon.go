// Package daemon is the hey-agent voice daemon (SPEC-HEY-AGENT.md, contract
// v2): one foreground process that owns the microphone pipeline and the
// voice output, driven through a unix socket.
//
// Layout of the package:
//   - daemon.go: the Daemon itself — socket server, command handlers,
//     speak queueing, and the glue between the bridge and the FSM.
//   - fsm.go: the state machine (SPEC §4).
//   - gate.go: the bind/unbind hotkey wrapper the daemon puts in front of the
//     platform listener.
//   - socket.go: socket path, listen with stale-socket handling, client Call.
//   - busy.go: the mic-busy file check before Speaking (SPEC §4).
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/bridge"
	"github.com/john-smith-ceo/hey-agent/internal/hotkey"
)

const (
	// cmdTimeout is the spec's answer limit (§2): every socket command must
	// reply within two seconds or fail.
	cmdTimeout = 2 * time.Second
	// connDeadline bounds a whole client connection — a hair past cmdTimeout
	// so a timed-out handler still gets its error line out.
	connDeadline = 3 * time.Second
	// maxRequestLen caps one request line; commands carry text, not bulk data.
	maxRequestLen = 1 << 20
	// busyPollInterval is how often a waiting Speak re-checks a fresh busy
	// file. Two hundred milliseconds is imperceptible for speech and cheap.
	busyPollInterval = 200 * time.Millisecond
)

// Speaker is the voice-output port: internal/voiceout plugs in here. Speak
// plays one phrase and returns when done; Hush aborts whatever is playing.
// nil in Config → speak/hush answer "voice output not configured".
type Speaker interface {
	Speak(ctx context.Context, req SpeakRequest) error
	Hush()
}

// Bridge is the input pipeline the daemon owns — today *bridge.Bridge; tests
// substitute a fake so no tmux/X11/audio is needed.
type Bridge interface {
	Run(ctx context.Context) error
	Target() string
}

// BridgeDeps are the wires the daemon hands to the input pipeline. The
// listener is the daemon's bind/unbind gate; OnState reports tagged bridge
// states; OnEvent sees every hotkey event before the bridge handles it.
type BridgeDeps struct {
	Listener hotkey.Listener
	OnState  func(seq uint64, state string)
	OnEvent  func(hotkey.Event)
}

// Config assembles the daemon. The pipeline fields mirror bridge.Config — the
// daemon builds the bridge from the same values `run --attached` would get.
type Config struct {
	SocketPath string // empty → DefaultSocketPath()
	Version    string

	// Ports — both optional.
	Speaker Speaker // nil → speak/hush report "voice output not configured"

	// Sender replaces the fixed Target pane with a per-recording delivery
	// port — internal/target.NewSender plugs in here. nil → Target is used
	// exactly like the pre-resolver listen mode did.
	Sender bridge.Sender

	// Pipeline — same meaning as bridge.Config.
	Mode          bridge.Mode
	Silence       time.Duration
	Device        string
	GainDB        float64
	APIKey        string
	Language      string
	BaseURL       string
	Model         string
	Key           hotkey.Key
	Target        string // fixed pane; only needed when Sender is nil
	BusyFile      string // also checked before Speaking (SPEC §4)
	OnRecord      string
	Submit        bool
	SubmitDelay   time.Duration
	RuntimeConfig string // empty → DefaultRuntimeConfigPath()
	Log           io.Writer

	// NewListener overrides hotkey.New — the gate calls it once per bind.
	NewListener func(hotkey.Key) (hotkey.Listener, error)

	// NewBridge replaces the default bridge.New wiring — tests inject a fake
	// pipeline so no tmux/X11/audio is needed.
	NewBridge func(BridgeDeps) (Bridge, error)

	// ExtraCommands adds socket commands on top of the built-in set — used by
	// tests (a sleeping handler proves the ≤2 s answer limit).
	ExtraCommands map[string]func(Request) Response
}

// Daemon is the voice daemon: socket server + FSM + owned pipeline.
type Daemon struct {
	cfg    Config
	fsm    *FSM
	gate   *hotkeyGate
	bridge Bridge

	// Tunables as fields so tests can shrink them after New.
	handlerTimeout time.Duration
	connTimeout    time.Duration
	busyPoll       time.Duration

	// pipeMu guards pipeGen, the newest recording session the bridge told us
	// about. Late emissions from an older session get dropped, so a previous
	// recording's "pasted" cannot mark a newer recording idle.
	pipeMu  sync.Mutex
	pipeGen uint64

	mu        sync.Mutex
	runCtx    context.Context
	shutdown  context.CancelFunc
	startedAt time.Time

	// speakMu guards the speak queue and the running phrase's cancel. The
	// queue is deliberately depth 1 (SPEC §4): a newer phrase replaces the
	// waiting one, because a stale voice-over is worse than a skipped one.
	speakMu     sync.Mutex
	pending     *SpeakRequest
	speakCancel context.CancelFunc

	// voiceOn is the tray's "Voice output" checkbox: off means speak/hush
	// answer as if no speaker were configured, without dropping the port.
	// Persisted through the runtime config file so a restart keeps it.
	voiceOn atomic.Bool
}

// New wires the daemon: gate, FSM, bridge. It fails fast when there is
// nowhere to type — a daemon without a target is only half a product.
func New(cfg Config) (*Daemon, error) {
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = DefaultSocketPath()
	}
	if cfg.RuntimeConfig == "" {
		cfg.RuntimeConfig = DefaultRuntimeConfigPath()
	}
	if cfg.Mode == "" {
		cfg.Mode = bridge.Tap
	}
	if cfg.Silence <= 0 {
		cfg.Silence = 5 * time.Second
	}
	// A resolver-backed sender lifts the fixed-target requirement — it
	// resolves the pane per recording (SPEC §3).
	if cfg.Target == "" && cfg.Sender == nil {
		return nil, errors.New("no input target: pass --target or a resolver-backed sender")
	}
	d := &Daemon{
		cfg:            cfg,
		fsm:            newFSM(),
		handlerTimeout: cmdTimeout,
		connTimeout:    connDeadline,
		busyPoll:       busyPollInterval,
	}
	newListener := cfg.NewListener
	if newListener == nil {
		newListener = hotkey.New
	}
	d.gate = newHotkeyGate(func() (hotkey.Listener, error) { return newListener(cfg.Key) })
	// Voice defaults to on when a speaker is configured; a saved runtime
	// config can turn it off again across restarts.
	d.voiceOn.Store(cfg.Speaker != nil)
	if saved := d.effectiveConfig(); saved.Voice != nil {
		d.voiceOn.Store(*saved.Voice && cfg.Speaker != nil)
	}
	deps := BridgeDeps{Listener: d.gate, OnState: d.onBridgeState, OnEvent: d.onHotkeyEvent}
	newBridge := cfg.NewBridge
	if newBridge == nil {
		newBridge = func(deps BridgeDeps) (Bridge, error) { return defaultBridge(cfg, deps) }
	}
	pipe, err := newBridge(deps)
	if err != nil {
		return nil, err
	}
	d.bridge = pipe
	return d, nil
}

// defaultBridge builds the real input pipeline from the daemon config. With
// cfg.Sender set, the bridge skips its fixed tmux sender entirely — target
// resolution is the sender's job, checked again right before each paste.
func defaultBridge(cfg Config, deps BridgeDeps) (Bridge, error) {
	if cfg.Target == "" && cfg.Sender == nil {
		return nil, errors.New("the bridge needs a target pane or a resolver-backed sender")
	}
	return bridge.New(bridge.Config{
		Mode:          cfg.Mode,
		Silence:       cfg.Silence,
		Device:        cfg.Device,
		GainDB:        cfg.GainDB,
		APIKey:        cfg.APIKey,
		Language:      cfg.Language,
		BaseURL:       cfg.BaseURL,
		Model:         cfg.Model,
		Log:           cfg.Log,
		TmuxTarget:    cfg.Target,
		Key:           cfg.Key,
		BusyFile:      cfg.BusyFile,
		OnRecord:      cfg.OnRecord,
		Submit:        cfg.Submit,
		SubmitDelay:   cfg.SubmitDelay,
		RuntimeConfig: cfg.RuntimeConfig,
		Listener:      deps.Listener,
		OnEvent:       deps.OnEvent,
		StateSeq:      deps.OnState,
		Sender:        cfg.Sender,
	})
}

// Run brings the daemon up: claims the socket, starts the pipeline, grabs the
// hotkey, and serves until the context ends or a `stop` command lands.
func (d *Daemon) Run(ctx context.Context) error {
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	runCtx, shutdown := context.WithCancel(ctx)
	d.mu.Lock()
	d.runCtx, d.shutdown, d.startedAt = runCtx, shutdown, time.Now()
	d.mu.Unlock()
	defer shutdown()

	ln, err := listenSocket(d.cfg.SocketPath)
	if err != nil {
		return err
	}
	// Clean shutdown removes the socket file so the next start does not have
	// to recognize a corpse.
	defer func() {
		_ = ln.Close()
		_ = os.Remove(d.cfg.SocketPath)
	}()

	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- d.bridge.Run(runCtx) }()

	// Grab the hotkey as soon as the bridge has opened the gate. A failed grab
	// is a warning, not fatal: the socket still answers, status shows
	// bound:false, and `hey-agent bind` retries.
	select {
	case <-d.gate.Started():
		if err := d.gate.Bind(); err != nil {
			fmt.Fprintf(d.cfg.Log, "hotkey grab failed; retry with `hey-agent bind`: %v\n", err)
		}
	case err := <-bridgeDone:
		// The pipeline died before it even opened the gate — nothing to serve.
		return err
	}

	go d.queuePump(runCtx)

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		d.acceptLoop(ln)
	}()

	fmt.Fprintf(d.cfg.Log, "hey-agent daemon listening on %s\n", d.cfg.SocketPath)

	var runErr error
	select {
	case <-runCtx.Done():
	case err := <-bridgeDone:
		// The pipeline died on its own (e.g. the hotkey listener vanished) —
		// a deaf daemon is useless, so shut down and report it.
		if err != nil && !errors.Is(err, context.Canceled) {
			runErr = err
		}
	}

	shutdown()
	_ = ln.Close()
	<-acceptDone
	select {
	case <-bridgeDone:
	case <-time.After(2 * time.Second):
		fmt.Fprintln(d.cfg.Log, "daemon: pipeline did not stop in 2s, exiting anyway")
	}
	return runErr
}

// requestShutdown is what the `stop` command calls.
func (d *Daemon) requestShutdown() {
	d.mu.Lock()
	shutdown := d.shutdown
	d.mu.Unlock()
	if shutdown != nil {
		shutdown()
	}
}

func (d *Daemon) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed → shutdown in progress
		}
		go d.serveConn(conn)
	}
}

// serveConn handles one connection: newline-delimited requests in, one JSON
// line out per request, until EOF or the deadline.
func (d *Daemon) serveConn(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(io.LimitReader(conn, maxRequestLen))
	enc := json.NewEncoder(conn)
	for {
		_ = conn.SetDeadline(time.Now().Add(d.connTimeout))
		var req Request
		if err := dec.Decode(&req); err != nil {
			if !errors.Is(err, io.EOF) {
				// A malformed line still deserves a well-formed answer.
				_ = enc.Encode(errResponsef("invalid request: %v", err))
			}
			return
		}
		resp := d.dispatchWithin(req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

// dispatchWithin enforces the spec's ≤2 s answer limit even for a hung
// handler: the answer arrives, or the error does.
func (d *Daemon) dispatchWithin(req Request) Response {
	result := make(chan Response, 1)
	go func() { result <- d.dispatch(req) }()
	select {
	case resp := <-result:
		return resp
	case <-time.After(d.handlerTimeout):
		return errResponse("command timed out")
	}
}

func (d *Daemon) dispatch(req Request) Response {
	cmd := req.Cmd
	if cmd == "" {
		// voiceRequest compatibility: a bare speak payload carries no cmd.
		if strings.TrimSpace(req.Text) != "" {
			cmd = "speak"
		} else {
			return errResponse("missing cmd")
		}
	}
	if handler, ok := d.cfg.ExtraCommands[cmd]; ok {
		return handler(req)
	}
	switch cmd {
	case "status":
		return d.cmdStatus()
	case "bind":
		return d.cmdBind()
	case "unbind":
		return d.cmdUnbind()
	case "stop":
		return d.cmdStop()
	case "speak":
		return d.cmdSpeak(req)
	case "hush":
		return d.cmdHush()
	case "config":
		return d.cmdConfig(req)
	default:
		return errResponsef("unknown command %q", cmd)
	}
}

// --- commands ---------------------------------------------------------------

func (d *Daemon) cmdStatus() Response {
	state := d.fsm.State()
	resp := okResponse()
	resp["state"] = state.String()
	resp["bound"] = d.gate.Bound()
	resp["target"] = d.bridge.Target()
	// The tray paints its checkboxes from these, so they must reflect what
	// the next recording/speak would actually do — the runtime file, not
	// the flag the daemon started with.
	resp["submit"] = d.effectiveConfig().Submit
	resp["voice_enabled"] = d.cfg.Speaker != nil && d.voiceOn.Load()
	d.mu.Lock()
	started := d.startedAt
	d.mu.Unlock()
	resp["uptime_sec"] = int64(time.Since(started).Seconds())
	resp["version"] = d.cfg.Version
	if state == Error && d.fsm.Message() != "" {
		resp["detail"] = d.fsm.Message()
	}
	return resp
}

func (d *Daemon) cmdBind() Response {
	if err := d.gate.Bind(); err != nil {
		return errResponsef("bind: %v", err)
	}
	return okResponse()
}

func (d *Daemon) cmdUnbind() Response {
	d.gate.Unbind()
	return okResponse()
}

func (d *Daemon) cmdStop() Response {
	// The answer goes out first; the shutdown follows on a short delay so the
	// response line is flushed before the listener closes.
	go func() {
		time.Sleep(50 * time.Millisecond)
		d.requestShutdown()
	}()
	return okResponse()
}

func (d *Daemon) cmdSpeak(req Request) Response {
	if d.cfg.Speaker == nil {
		return errResponse("voice output not configured")
	}
	if !d.voiceOn.Load() {
		return errResponse("voice output disabled")
	}
	speak := req.speakRequest()
	if strings.TrimSpace(speak.Text) == "" {
		return errResponse("speak: empty text")
	}
	// When the pipeline is busy the phrase is queued rather than refused
	// (SPEC §4): queue depth 1, and a newer speak replaces a waiting one —
	// a stale voice-over is worse than a skipped one.
	if d.startSpeak(speak) {
		return okResponse()
	}
	d.speakMu.Lock()
	d.pending = &speak
	d.speakMu.Unlock()
	resp := okResponse()
	resp["queued"] = true
	return resp
}

func (d *Daemon) cmdHush() Response {
	if d.cfg.Speaker == nil {
		return errResponse("voice output not configured")
	}
	// Drop the queue too: "hush" means silence, and a phrase that was only
	// waiting should not pop up right after the user asked for quiet.
	d.speakMu.Lock()
	d.pending = nil
	d.speakMu.Unlock()
	d.hushCurrent()
	d.fsm.TransitionFrom(Speaking, Idle)
	return okResponse()
}

// runtimeConfig mirrors the JSON file the bridge polls between recordings —
// same schema as bridge.runtimeSettings plus fields the daemon persists for
// its next start (the running pipeline cannot swap them). Voice is a pointer
// so "file predates the toggle" differs from an explicit off.
type runtimeConfig struct {
	Mode    string `json:"mode"`
	Silence string `json:"silence"`
	Submit  bool   `json:"submit"`
	Voice   *bool  `json:"voice,omitempty"`
	Hotkey  string `json:"hotkey,omitempty"`
	Device  string `json:"device,omitempty"`
}

// effectiveConfig merges the runtime config file over the daemon defaults —
// the same seeding cmdConfig uses before applying a change. An unreadable or
// missing file means the defaults stand.
func (d *Daemon) effectiveConfig() runtimeConfig {
	cfg := runtimeConfig{
		Mode:    string(d.cfg.Mode),
		Silence: d.cfg.Silence.String(),
		Submit:  d.cfg.Submit,
		Hotkey:  d.cfg.Key.Name,
		Device:  d.cfg.Device,
	}
	data, err := os.ReadFile(d.cfg.RuntimeConfig)
	if err != nil {
		return cfg
	}
	var saved runtimeConfig
	if json.Unmarshal(data, &saved) != nil {
		return cfg
	}
	if saved.Mode != "" {
		cfg.Mode = saved.Mode
	}
	if saved.Silence != "" {
		cfg.Silence = saved.Silence
	}
	cfg.Submit = saved.Submit
	cfg.Voice = saved.Voice
	if saved.Hotkey != "" {
		cfg.Hotkey = saved.Hotkey
	}
	if saved.Device != "" {
		cfg.Device = saved.Device
	}
	return cfg
}

func (d *Daemon) cmdConfig(req Request) Response {
	// Validate before touching the file: a bad value must never land where
	// the bridge will read it.
	if req.Mode != "" && req.Mode != string(bridge.Tap) && req.Mode != string(bridge.Push) {
		return errResponsef("mode must be tap or push, got %q", req.Mode)
	}
	if req.Silence != "" {
		if dur, err := time.ParseDuration(req.Silence); err != nil || dur <= 0 {
			return errResponsef("silence must be a positive duration, for example 2s — got %q", req.Silence)
		}
	}
	hotkeyName := ""
	if req.Hotkey != "" {
		key, err := hotkey.Lookup(req.Hotkey)
		if err != nil {
			return errResponsef("hotkey: %v", err)
		}
		hotkeyName = key.Name
	}

	// Merge over the file, seeded from the daemon's own config so a first
	// write produces a complete, bridge-valid document.
	cfg := d.effectiveConfig()

	restartNote := false
	if req.Submit != nil {
		cfg.Submit = *req.Submit
	}
	if req.VoiceEnabled != nil {
		on := *req.VoiceEnabled
		cfg.Voice = &on
		// Applies immediately — no restart note, unlike hotkey/device.
		d.voiceOn.Store(on)
	}
	if req.Silence != "" {
		cfg.Silence = req.Silence
	}
	if req.Mode != "" {
		cfg.Mode = req.Mode
	}
	if hotkeyName != "" {
		cfg.Hotkey = hotkeyName
		restartNote = true
	}
	if req.Device != "" {
		cfg.Device = req.Device
		restartNote = true
	}
	if err := writeRuntimeConfig(d.cfg.RuntimeConfig, cfg); err != nil {
		return errResponsef("save config: %v", err)
	}
	resp := okResponse()
	resp["config"] = cfg
	if restartNote {
		resp["note"] = "hotkey and device apply on the next daemon start"
	}
	return resp
}

// writeRuntimeConfig is the same atomic 0600-file write the standalone
// `hey-agent config` uses: temp file in the same directory, then rename.
func writeRuntimeConfig(path string, cfg runtimeConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hey-agent-config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// --- bridge → FSM -----------------------------------------------------------

// onBridgeState maps the bridge's tagged state strings onto the FSM. The seq
// tag identifies the recording session that produced the state: an emission
// from an older session is stale and gets dropped, so a previous recording's
// late "pasted" cannot mark a newer recording idle.
func (d *Daemon) onBridgeState(seq uint64, value string) {
	d.pipeMu.Lock()
	defer d.pipeMu.Unlock()
	stale := seq != 0 && seq < d.pipeGen
	switch value {
	case "recording":
		// Safety net: if we were still speaking, SPEC §4 requires hush before
		// record. OnEvent normally did that already.
		if d.fsm.State() == Speaking {
			d.hushCurrent()
		}
		d.pipeGen = seq
		d.transition(Recording)
	case "transcribing":
		if stale {
			break
		}
		d.transition(Transcribing)
	case "delivering":
		if stale {
			break
		}
		d.transition(Delivering)
	case "pasted", "idle":
		if stale {
			break
		}
		d.pipeGen = 0
		// A leftover "idle" from the input lane must not cut a phrase off
		// mid-flight — Speaking ends only through the speak worker or hush.
		if d.fsm.State() != Speaking {
			d.transition(Idle)
		}
	case "error":
		if stale {
			break
		}
		d.pipeGen = 0
		// Input-side errors surface in status but do not murder speech.
		if d.fsm.State() != Speaking {
			d.fsm.SetError(errors.New("input pipeline error"))
		}
	default:
		fmt.Fprintf(d.cfg.Log, "unknown bridge state %q ignored\n", value)
	}
	if stale {
		fmt.Fprintf(d.cfg.Log, "dropped stale bridge state %q (seq %d, current %d)\n", value, seq, d.pipeGen)
	}
}

// transition logs FSM rejections instead of propagating them: the bridge must
// keep running even when a state emission arrives in an odd order.
func (d *Daemon) transition(to State) {
	if err := d.fsm.Transition(to); err != nil {
		fmt.Fprintf(d.cfg.Log, "bridge state ignored: %v\n", err)
	}
}

// onHotkeyEvent runs before the bridge handles an event. SPEC §4: a press
// while Speaking hushes the phrase and records — the press semantics are the
// same in tap and push mode, so checking Down covers both.
func (d *Daemon) onHotkeyEvent(event hotkey.Event) {
	if !event.Down {
		return
	}
	if d.fsm.TransitionFrom(Speaking, Recording) {
		d.hushCurrent()
	}
}

// --- speak machinery --------------------------------------------------------

// startSpeak tries to take the machine to Speaking and spawn the worker. It
// returns false when the pipeline is busy — including "already speaking", so
// the strict transition matters: a no-op would run a second phrase over the
// first.
func (d *Daemon) startSpeak(req SpeakRequest) bool {
	if err := d.fsm.TransitionStrict(Speaking); err != nil {
		return false
	}
	d.mu.Lock()
	base := d.runCtx
	d.mu.Unlock()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	d.speakMu.Lock()
	d.speakCancel = cancel
	d.speakMu.Unlock()
	go d.speakWorker(ctx, req)
	return true
}

// speakWorker runs one phrase: wait for a free microphone, then speak. Its
// FSM cleanup is conditional — a hush or a recording hotkey may already have
// moved the machine, and the worker must not stomp that.
func (d *Daemon) speakWorker(ctx context.Context, req SpeakRequest) {
	err := d.waitForMic(ctx)
	if err == nil {
		err = d.cfg.Speaker.Speak(ctx, req)
	}
	d.speakMu.Lock()
	d.speakCancel = nil
	d.speakMu.Unlock()
	switch {
	case err == nil:
		d.fsm.TransitionFrom(Speaking, Idle)
	case ctx.Err() != nil || errors.Is(err, context.Canceled):
		// Hushed or shutting down: whoever cancelled owns the state.
		d.fsm.TransitionFrom(Speaking, Idle)
	default:
		fmt.Fprintf(d.cfg.Log, "speak failed: %v\n", err)
		d.fsm.SetErrorFrom(Speaking, err)
	}
}

// hushCurrent kills whatever phrase is running — used by the `hush` command
// and by a hotkey press that interrupts speech.
func (d *Daemon) hushCurrent() {
	d.speakMu.Lock()
	cancel := d.speakCancel
	d.speakMu.Unlock()
	if d.cfg.Speaker != nil {
		d.cfg.Speaker.Hush()
	}
	if cancel != nil {
		cancel()
	}
}

// queuePump starts a queued phrase every time the machine returns to Idle.
// It checks the current state rather than the event value: a subscriber may
// drop intermediate events, but State() always tells the truth.
func (d *Daemon) queuePump(ctx context.Context) {
	changes, unsubscribe := d.fsm.Subscribe()
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-changes:
			if !ok {
				return
			}
			if d.fsm.State() != Idle {
				continue
			}
			d.speakMu.Lock()
			req := d.pending
			d.pending = nil
			d.speakMu.Unlock()
			if req != nil && !d.startSpeak(*req) {
				// The machine left Idle between the check and the transition
				// (a hotkey can land in that window) — put the phrase back,
				// unless a newer one queued meanwhile.
				d.speakMu.Lock()
				if d.pending == nil {
					d.pending = req
				}
				d.speakMu.Unlock()
			}
		}
	}
}

// State exposes the FSM for the status command and future subscribers (tray).
func (d *Daemon) State() State { return d.fsm.State() }

// Subscribe forwards FSM.Subscribe — the tray/status consumers plug in here.
func (d *Daemon) Subscribe() (<-chan State, func()) { return d.fsm.Subscribe() }

// Bound reports whether the hotkey grab is held — the tray reads it when
// choosing between the Bind and Unbind menu items.
func (d *Daemon) Bound() bool { return d.gate.Bound() }

// Handle dispatches one command in-process: the tray's menu actions reach the
// same handlers the socket serves, without a loopback connection.
func (d *Daemon) Handle(req Request) Response { return d.dispatch(req) }

// VoiceOn reports whether speak requests are accepted — the tray's "Voice
// output" checkmark mirrors it.
func (d *Daemon) VoiceOn() bool { return d.cfg.Speaker != nil && d.voiceOn.Load() }
