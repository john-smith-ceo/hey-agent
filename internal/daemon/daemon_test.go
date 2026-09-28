package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/bridge"
	"github.com/john-smith-ceo/hey-agent/internal/hotkey"
)

// --- test doubles -----------------------------------------------------------

// fakeListener is a controllable hotkey.Listener: each Start opens a fresh
// event channel the test can push into. The channel is never closed — the
// gate's pump stops via ctx alone, so a close here would only race with send.
type fakeListener struct {
	mu       sync.Mutex
	events   chan hotkey.Event
	starts   int
	startErr error
}

func (f *fakeListener) Start(ctx context.Context) (<-chan hotkey.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	f.starts++
	f.events = make(chan hotkey.Event, 8)
	return f.events, nil
}

// send pushes an event into the channel of the most recent Start. Returns
// false when the listener was never started — an event pushed while unbound
// lands in a channel nobody reads, which is exactly what a released grab does.
func (f *fakeListener) send(ev hotkey.Event) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.events == nil {
		return false
	}
	select {
	case f.events <- ev:
		return true
	default:
		return false
	}
}

func (f *fakeListener) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts
}

// fakeBridge is the input pipeline without tmux/X11/audio: it opens the
// listener gate exactly like the real bridge and forwards hotkey events to
// the OnEvent hook, so tests exercise the gate end to end.
type fakeBridge struct {
	target string
	deps   BridgeDeps
	seen   chan hotkey.Event
}

func (f *fakeBridge) Run(ctx context.Context) error {
	events, err := f.deps.Listener.Start(ctx)
	if err != nil {
		return err
	}
	go func() {
		for ev := range events {
			if f.deps.OnEvent != nil {
				f.deps.OnEvent(ev)
			}
			select {
			case f.seen <- ev:
			default:
			}
		}
	}()
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeBridge) Target() string { return f.target }

// bridgeState feeds the daemon a tagged bridge state, like the real pipeline.
func (f *fakeBridge) bridgeState(seq uint64, state string) {
	if f.deps.OnState != nil {
		f.deps.OnState(seq, state)
	}
}

// mockSpeaker records Speak calls and lets a test hold Speak open.
type mockSpeaker struct {
	calls    chan SpeakRequest
	hushes   chan struct{}
	release  chan struct{} // when non-nil, Speak blocks until closed or ctx dies
	speakErr error
}

func (m *mockSpeaker) Speak(ctx context.Context, req SpeakRequest) error {
	m.calls <- req
	if m.release != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-m.release:
		}
	}
	return m.speakErr
}

func (m *mockSpeaker) Hush() {
	select {
	case m.hushes <- struct{}{}:
	default:
	}
}

// --- helpers ----------------------------------------------------------------

type testRig struct {
	d        *Daemon
	bridge   *fakeBridge
	listener *fakeListener
	speaker  *mockSpeaker
	socket   string
	done     chan error
}

// startDaemon wires a daemon with fakes, runs it, and waits until the socket
// answers. mutateConfig/tweak adjust Config and the Daemon before Run.
func startDaemon(t *testing.T, mutateConfig func(*Config), tweak func(*Daemon)) *testRig {
	t.Helper()
	dir := t.TempDir()
	rig := &testRig{
		bridge:   &fakeBridge{target: "%1", seen: make(chan hotkey.Event, 16)},
		listener: &fakeListener{},
		speaker:  &mockSpeaker{calls: make(chan SpeakRequest, 4), hushes: make(chan struct{}, 4)},
		socket:   filepath.Join(dir, "hey-agent.sock"),
		done:     make(chan error, 1),
	}
	cfg := Config{
		SocketPath: rig.socket,
		Version:    "test",
		Target:     "%1",
		Log:        io.Discard,
		Speaker:    rig.speaker,
		NewListener: func(hotkey.Key) (hotkey.Listener, error) {
			return rig.listener, nil
		},
		NewBridge: func(deps BridgeDeps) (Bridge, error) {
			rig.bridge.deps = deps
			return rig.bridge, nil
		},
	}
	if mutateConfig != nil {
		mutateConfig(&cfg)
	}
	rig.socket = cfg.SocketPath // a mutator may relocate the socket
	d, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if tweak != nil {
		tweak(d)
	}
	rig.d = d
	go func() { rig.done <- d.Run(context.Background()) }()
	waitFor(t, func() bool {
		reply, err := Call(context.Background(), rig.socket, Request{Cmd: "status"})
		return err == nil && reply.OK
	}, "daemon socket did not come up")
	t.Cleanup(func() {
		Call(context.Background(), rig.socket, Request{Cmd: "stop"})
		select {
		case <-rig.done:
		case <-time.After(3 * time.Second):
		}
	})
	return rig
}

func (r *testRig) call(t *testing.T, req Request) Reply {
	t.Helper()
	reply, err := Call(context.Background(), r.socket, req)
	if err != nil {
		t.Fatalf("call %q: %v", req.Cmd, err)
	}
	return reply
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

// --- socket server ----------------------------------------------------------

func TestStatusReportsStateBoundTargetVersion(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	// The socket answers before the first bind finishes — poll for the grab.
	waitFor(t, func() bool { return rig.d.gate.Bound() }, "initial bind never happened")
	reply := rig.call(t, Request{Cmd: "status"})
	if !reply.OK {
		t.Fatalf("status: %s", reply.Error)
	}
	if reply.State != "idle" {
		t.Fatalf("state = %q, want idle", reply.State)
	}
	if !reply.Bound {
		t.Fatal("bound = false, want true — the daemon grabs the hotkey at start")
	}
	if reply.Target != "%1" || reply.Version != "test" {
		t.Fatalf("target/version = %q/%q", reply.Target, reply.Version)
	}
	if reply.UptimeSec < 0 {
		t.Fatalf("uptime_sec = %d", reply.UptimeSec)
	}
}

func TestSocketFileIsOwnerOnly(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	info, err := os.Stat(rig.socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
}

func TestBindUnbindToggleBoundFlag(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	waitFor(t, func() bool { return rig.d.gate.Bound() }, "initial bind never happened")
	if reply := rig.call(t, Request{Cmd: "unbind"}); !reply.OK {
		t.Fatalf("unbind: %s", reply.Error)
	}
	waitFor(t, func() bool { return !rig.d.gate.Bound() }, "unbind did not release the grab")
	reply := rig.call(t, Request{Cmd: "status"})
	if reply.Bound {
		t.Fatal("bound still true after unbind")
	}
	// Unbound, events from the old listener must not reach the pipeline.
	if rig.listener.send(hotkey.Event{Down: true}) {
		select {
		case <-rig.bridge.seen:
			t.Fatal("event leaked through an unbound gate")
		case <-time.After(50 * time.Millisecond):
		}
	}
	if reply := rig.call(t, Request{Cmd: "bind"}); !reply.OK {
		t.Fatalf("bind: %s", reply.Error)
	}
	waitFor(t, func() bool { return rig.d.gate.Bound() }, "bind did not re-grab")
	if !rig.listener.send(hotkey.Event{Down: true}) {
		t.Fatal("could not push event into re-bound listener")
	}
	select {
	case <-rig.bridge.seen:
	case <-time.After(time.Second):
		t.Fatal("re-bound event never reached the pipeline")
	}
	if rig.listener.startCount() < 2 {
		t.Fatalf("listener started %d times, want ≥2", rig.listener.startCount())
	}
}

func TestStopAnswersThenShutsDown(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	reply := rig.call(t, Request{Cmd: "stop"})
	if !reply.OK {
		t.Fatalf("stop: %s", reply.Error)
	}
	select {
	case err := <-rig.done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not stop after the stop command")
	}
	waitFor(t, func() bool {
		_, err := os.Stat(rig.socket)
		return os.IsNotExist(err)
	}, "socket file was not removed on shutdown")
	if _, err := Call(context.Background(), rig.socket, Request{Cmd: "status"}); err == nil {
		t.Fatal("socket still answers after stop")
	}
}

func TestAlreadyRunningIsRefused(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	second, err := New(Config{
		SocketPath: rig.socket,
		Target:     "%1",
		NewBridge: func(BridgeDeps) (Bridge, error) {
			return &fakeBridge{target: "%1"}, nil
		},
		NewListener: func(hotkey.Key) (hotkey.Listener, error) {
			return &fakeListener{}, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := second.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("second daemon err = %v, want 'already running'", err)
	}
}

func TestStaleSocketIsReclaimed(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "hey-agent.sock")
	// A dead leftover: the file exists but nobody listens.
	if err := os.WriteFile(sock, []byte("dead"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig := startDaemon(t, func(c *Config) { c.SocketPath = sock }, nil)
	reply := rig.call(t, Request{Cmd: "status"})
	if !reply.OK {
		t.Fatalf("status after stale reclaim: %s", reply.Error)
	}
}

func TestUnknownCommandAndMissingCmd(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	reply := rig.call(t, Request{Cmd: "bogus"})
	if reply.OK || !strings.Contains(reply.Error, "bogus") {
		t.Fatalf("unknown command reply = %+v", reply)
	}
	reply = rig.call(t, Request{})
	if reply.OK || !strings.Contains(reply.Error, "missing cmd") {
		t.Fatalf("missing cmd reply = %+v", reply)
	}
}

func TestInvalidJSONGetsErrorLine(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	conn, err := net.DialTimeout("unix", rig.socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		t.Fatalf("no error line for bad JSON: %v", err)
	}
	if reply.OK || reply.Error == "" {
		t.Fatalf("bad JSON reply = %+v", reply)
	}
}

func TestCommandTimeoutAnswersWithError(t *testing.T) {
	rig := startDaemon(t, func(c *Config) {
		c.ExtraCommands = map[string]func(Request) Response{
			"sleep": func(Request) Response {
				time.Sleep(500 * time.Millisecond)
				return okResponse()
			},
		}
	}, func(d *Daemon) { d.handlerTimeout = 60 * time.Millisecond })
	reply := rig.call(t, Request{Cmd: "sleep"})
	if reply.OK || !strings.Contains(reply.Error, "timed out") {
		t.Fatalf("sleep command reply = %+v, want timeout error", reply)
	}
}

func TestCallToDeadDaemonFails(t *testing.T) {
	_, err := Call(context.Background(), filepath.Join(t.TempDir(), "nope.sock"), Request{Cmd: "status"})
	if err == nil {
		t.Fatal("call to a missing socket must fail")
	}
}

// --- speak / hush -----------------------------------------------------------

func TestSpeakPassesRequestToSpeaker(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	reply := rig.call(t, Request{
		Cmd: "speak", Text: "hello", Voice: "cedar",
		Instructions: "warm", SpeedUp: 10,
	})
	if !reply.OK || reply.Queued {
		t.Fatalf("speak reply = %+v", reply)
	}
	select {
	case got := <-rig.speaker.calls:
		if got.Text != "hello" || got.Voice != "cedar" || got.Instructions != "warm" || got.SpeedUp != 10 {
			t.Fatalf("SpeakRequest = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Speak was not called")
	}
	waitFor(t, func() bool { return rig.d.State() == Idle }, "state did not return to idle after speak")
}

func TestSpeakEmptyTextRejected(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	reply := rig.call(t, Request{Cmd: "speak", Text: "  "})
	if reply.OK || !strings.Contains(reply.Error, "empty") {
		t.Fatalf("empty speak reply = %+v", reply)
	}
}

func TestSpeakWithoutSpeaker(t *testing.T) {
	rig := startDaemon(t, func(c *Config) { c.Speaker = nil }, nil)
	for _, cmd := range []string{"speak", "hush"} {
		req := Request{Cmd: cmd}
		if cmd == "speak" {
			req.Text = "hi"
		}
		reply := rig.call(t, req)
		if reply.OK || reply.Error != "voice output not configured" {
			t.Fatalf("%s without speaker = %+v", cmd, reply)
		}
	}
}

func TestSpeakQueuedWhileRecording(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	rig.bridge.bridgeState(1, "recording")
	waitFor(t, func() bool { return rig.d.State() == Recording }, "not recording")
	reply := rig.call(t, Request{Cmd: "speak", Text: "wait for it"})
	if !reply.OK || !reply.Queued {
		t.Fatalf("speak during recording = %+v, want ok+queued", reply)
	}
	select {
	case got := <-rig.speaker.calls:
		t.Fatalf("phrase played during recording: %+v", got)
	case <-time.After(80 * time.Millisecond):
	}
	// The pipeline finishes: the queued phrase must fire on its own.
	rig.bridge.bridgeState(1, "transcribing")
	rig.bridge.bridgeState(1, "delivering")
	rig.bridge.bridgeState(1, "pasted")
	select {
	case got := <-rig.speaker.calls:
		if got.Text != "wait for it" {
			t.Fatalf("queued text = %q", got.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("queued phrase never played after idle")
	}
}

func TestNewerSpeakReplacesQueuedOne(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	rig.bridge.bridgeState(1, "recording")
	waitFor(t, func() bool { return rig.d.State() == Recording }, "not recording")
	rig.call(t, Request{Cmd: "speak", Text: "first"})
	rig.call(t, Request{Cmd: "speak", Text: "second"})
	rig.bridge.bridgeState(1, "pasted")
	select {
	case got := <-rig.speaker.calls:
		if got.Text != "second" {
			t.Fatalf("queued phrase = %q, want the newer one", got.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("queued phrase never played")
	}
	select {
	case got := <-rig.speaker.calls:
		t.Fatalf("stale phrase also played: %q", got.Text)
	case <-time.After(80 * time.Millisecond):
	}
}

func TestHushCallsSpeakerAndReturnsIdle(t *testing.T) {
	speaker := &mockSpeaker{
		calls:   make(chan SpeakRequest, 4),
		hushes:  make(chan struct{}, 4),
		release: make(chan struct{}), // Speak stays open until released or cancelled
	}
	rig := startDaemon(t, func(c *Config) { c.Speaker = speaker }, nil)
	rig.call(t, Request{Cmd: "speak", Text: "long story"})
	<-speaker.calls
	waitFor(t, func() bool { return rig.d.State() == Speaking }, "not speaking")
	reply := rig.call(t, Request{Cmd: "hush"})
	if !reply.OK {
		t.Fatalf("hush: %s", reply.Error)
	}
	select {
	case <-speaker.hushes:
	case <-time.After(time.Second):
		t.Fatal("Speaker.Hush was not called")
	}
	waitFor(t, func() bool { return rig.d.State() == Idle }, "state did not return to idle after hush")
}

func TestHotkeyDuringSpeakingHushesAndRecords(t *testing.T) {
	speaker := &mockSpeaker{
		calls:   make(chan SpeakRequest, 4),
		hushes:  make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	rig := startDaemon(t, func(c *Config) { c.Speaker = speaker }, nil)
	rig.call(t, Request{Cmd: "speak", Text: "interrupt me"})
	<-speaker.calls
	waitFor(t, func() bool { return rig.d.State() == Speaking }, "not speaking")
	// A real press: through the gate, through the fake bridge, into OnEvent.
	if !rig.listener.send(hotkey.Event{Down: true}) {
		t.Fatal("could not inject hotkey event")
	}
	select {
	case <-speaker.hushes:
	case <-time.After(time.Second):
		t.Fatal("hotkey during speaking did not hush")
	}
	waitFor(t, func() bool { return rig.d.State() == Recording }, "state did not move to recording")
}

func TestSpeakWhileSpeakingQueuesAndSerializes(t *testing.T) {
	speaker := &mockSpeaker{
		calls:   make(chan SpeakRequest, 4),
		hushes:  make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	rig := startDaemon(t, func(c *Config) { c.Speaker = speaker }, nil)
	rig.call(t, Request{Cmd: "speak", Text: "first"})
	<-speaker.calls
	waitFor(t, func() bool { return rig.d.State() == Speaking }, "not speaking")
	// A second phrase while one is playing queues, it must not overlap.
	reply := rig.call(t, Request{Cmd: "speak", Text: "second"})
	if !reply.OK || !reply.Queued {
		t.Fatalf("speak during speaking = %+v, want ok+queued", reply)
	}
	select {
	case got := <-speaker.calls:
		t.Fatalf("phrases overlapped: %+v", got)
	case <-time.After(60 * time.Millisecond):
	}
	close(speaker.release) // first phrase ends; the queued one plays
	select {
	case got := <-speaker.calls:
		if got.Text != "second" {
			t.Fatalf("serialized phrase = %q, want second", got.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("queued phrase did not play after the first ended")
	}
}

func TestQueuedSpeakDroppedByHush(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	rig.bridge.bridgeState(1, "recording")
	waitFor(t, func() bool { return rig.d.State() == Recording }, "not recording")
	rig.call(t, Request{Cmd: "speak", Text: "never heard"})
	reply := rig.call(t, Request{Cmd: "hush"})
	if !reply.OK {
		t.Fatalf("hush: %s", reply.Error)
	}
	rig.bridge.bridgeState(1, "pasted")
	select {
	case got := <-rig.speaker.calls:
		t.Fatalf("hushed phrase still played: %q", got.Text)
	case <-time.After(80 * time.Millisecond):
	}
}

// --- busy file --------------------------------------------------------------

func TestFreshBusyFileDelaysSpeak(t *testing.T) {
	busy := filepath.Join(t.TempDir(), "mic-busy")
	if err := os.WriteFile(busy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rig := startDaemon(t, func(c *Config) { c.BusyFile = busy }, func(d *Daemon) {
		d.busyPoll = 20 * time.Millisecond
	})
	rig.call(t, Request{Cmd: "speak", Text: "held back"})
	select {
	case <-rig.speaker.calls:
		t.Fatal("Speak ran while a fresh busy file existed")
	case <-time.After(150 * time.Millisecond):
	}
	if err := os.Remove(busy); err != nil {
		t.Fatal(err)
	}
	select {
	case <-rig.speaker.calls:
	case <-time.After(time.Second):
		t.Fatal("Speak did not run after the busy file was removed")
	}
}

func TestStaleBusyFileIsRemovedAndIgnored(t *testing.T) {
	busy := filepath.Join(t.TempDir(), "mic-busy")
	if err := os.WriteFile(busy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-(busyFileMaxAge + time.Minute))
	if err := os.Chtimes(busy, old, old); err != nil {
		t.Fatal(err)
	}
	rig := startDaemon(t, func(c *Config) { c.BusyFile = busy }, nil)
	rig.call(t, Request{Cmd: "speak", Text: "go ahead"})
	select {
	case <-rig.speaker.calls:
	case <-time.After(time.Second):
		t.Fatal("stale busy file blocked Speak")
	}
	if _, err := os.Stat(busy); !os.IsNotExist(err) {
		t.Fatal("stale busy file was not removed")
	}
}

// --- config -----------------------------------------------------------------

func TestConfigMergesIntoRuntimeFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "runtime.json")
	rig := startDaemon(t, func(c *Config) {
		c.Mode = bridge.Tap
		c.Silence = 5 * time.Second
		c.RuntimeConfig = cfgPath
	}, nil)
	yes := true
	reply := rig.call(t, Request{Cmd: "config", Submit: &yes, Silence: "2s"})
	if !reply.OK {
		t.Fatalf("config: %s", reply.Error)
	}
	var eff runtimeConfig
	if err := json.Unmarshal(reply.Config, &eff); err != nil {
		t.Fatalf("effective config did not decode: %v", err)
	}
	if !eff.Submit || eff.Silence != "2s" || eff.Mode != "tap" {
		t.Fatalf("effective config = %+v", eff)
	}
	// The file must hold a complete, bridge-valid document.
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk runtimeConfig
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if !onDisk.Submit || onDisk.Silence != "2s" || onDisk.Mode != "tap" {
		t.Fatalf("file config = %+v", onDisk)
	}
	// A second change merges instead of wiping the first.
	no := false
	reply = rig.call(t, Request{Cmd: "config", Submit: &no})
	if !reply.OK {
		t.Fatalf("config #2: %s", reply.Error)
	}
	if err := json.Unmarshal(reply.Config, &eff); err != nil {
		t.Fatal(err)
	}
	if eff.Submit || eff.Silence != "2s" {
		t.Fatalf("merge lost fields: %+v", eff)
	}
}

func TestConfigRejectsBadValues(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	yes := true
	for _, req := range []Request{
		{Cmd: "config", Silence: "not-a-duration"},
		{Cmd: "config", Silence: "-2s"},
		{Cmd: "config", Mode: "weird"},
		{Cmd: "config", Hotkey: "Not_A_Key", Submit: &yes},
	} {
		if reply := rig.call(t, req); reply.OK {
			t.Fatalf("config %+v unexpectedly accepted", req)
		}
	}
}

// --- FSM --------------------------------------------------------------------

func TestFSMRejectsIllegalTransitions(t *testing.T) {
	fsm := newFSM()
	for _, illegal := range []State{Transcribing, Delivering} {
		if err := fsm.Transition(illegal); err == nil {
			t.Fatalf("Idle → %s must be rejected", illegal)
		}
		if fsm.State() != Idle {
			t.Fatalf("rejected transition moved the machine to %s", fsm.State())
		}
	}
	// Recording ⊥ Speaking: no direct edge either way.
	fsm2 := newFSM()
	if err := fsm2.Transition(Recording); err != nil {
		t.Fatal(err)
	}
	if err := fsm2.Transition(Speaking); err == nil {
		t.Fatal("Recording → Speaking must be rejected")
	}
}

func TestFSMHappyPathAndIdempotent(t *testing.T) {
	fsm := newFSM()
	for _, to := range []State{Recording, Transcribing, Delivering, Idle, Speaking, Idle} {
		if err := fsm.Transition(to); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	if err := fsm.Transition(Idle); err != nil {
		t.Fatalf("same-state transition must be a no-op: %v", err)
	}
}

func TestFSMErrorAutoReturnsToIdle(t *testing.T) {
	fsm := newFSM()
	fsm.errorTTL = 30 * time.Millisecond
	fsm.SetError(errors.New("boom"))
	if fsm.State() != Error || fsm.Message() != "boom" {
		t.Fatalf("error state/message = %s/%q", fsm.State(), fsm.Message())
	}
	waitFor(t, func() bool { return fsm.State() == Idle }, "Error did not auto-return to Idle")
}

func TestFSMErrorAcceptsNextRecording(t *testing.T) {
	fsm := newFSM()
	fsm.errorTTL = time.Hour // keep Error pinned for the duration of the test
	fsm.SetError(errors.New("boom"))
	if err := fsm.Transition(Recording); err != nil {
		t.Fatalf("hotkey must work from Error as from Idle: %v", err)
	}
}

func TestFSMTransitionFromIsConditional(t *testing.T) {
	fsm := newFSM()
	if fsm.TransitionFrom(Recording, Idle) {
		t.Fatal("TransitionFrom moved a machine that was never recording")
	}
	if err := fsm.Transition(Speaking); err != nil {
		t.Fatal(err)
	}
	if !fsm.TransitionFrom(Speaking, Recording) {
		t.Fatal("Speaking → Recording must be allowed (hotkey hush)")
	}
	if fsm.TransitionFrom(Speaking, Idle) {
		t.Fatal("late speak worker stomped the recording state")
	}
}

func TestFSMSubscribeSeesCurrentAndChanges(t *testing.T) {
	fsm := newFSM()
	ch, unsubscribe := fsm.Subscribe()
	defer unsubscribe()
	if got := <-ch; got != Idle {
		t.Fatalf("initial state = %s", got)
	}
	if err := fsm.Transition(Recording); err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got != Recording {
		t.Fatalf("subscriber saw %s, want recording", got)
	}
}

// --- daemon ↔ bridge state mapping ------------------------------------------

func TestBridgeStatesDriveFSM(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	steps := []struct {
		state string
		want  State
	}{
		{"recording", Recording},
		{"transcribing", Transcribing},
		{"delivering", Delivering},
		{"pasted", Idle},
	}
	for i, step := range steps {
		rig.bridge.bridgeState(1, step.state)
		waitFor(t, func() bool { return rig.d.State() == step.want },
			fmt.Sprintf("bridge %q did not move FSM to %s", step.state, step.want))
		_ = i
	}
	// A bridge failure shows up as error and settles back to idle on its own.
	rig.d.fsm.errorTTL = 50 * time.Millisecond
	rig.bridge.bridgeState(2, "recording")
	waitFor(t, func() bool { return rig.d.State() == Recording }, "second recording never started")
	rig.bridge.bridgeState(2, "error")
	waitFor(t, func() bool { return rig.d.State() == Error }, "bridge error did not surface")
	waitFor(t, func() bool { return rig.d.State() == Idle }, "error did not settle to idle")
}

func TestStaleSessionStatesAreDropped(t *testing.T) {
	rig := startDaemon(t, nil, nil)
	rig.bridge.bridgeState(1, "recording")
	waitFor(t, func() bool { return rig.d.State() == Recording }, "not recording")
	rig.bridge.bridgeState(2, "recording")
	// Session 1's late "pasted" must not idle out session 2.
	rig.bridge.bridgeState(1, "pasted")
	time.Sleep(50 * time.Millisecond)
	if got := rig.d.State(); got != Recording {
		t.Fatalf("stale pasted moved FSM to %s", got)
	}
}

func TestIdleWhileSpeakingIsIgnored(t *testing.T) {
	speaker := &mockSpeaker{
		calls:   make(chan SpeakRequest, 4),
		hushes:  make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	rig := startDaemon(t, func(c *Config) { c.Speaker = speaker }, nil)
	rig.call(t, Request{Cmd: "speak", Text: "do not cut me"})
	<-speaker.calls
	waitFor(t, func() bool { return rig.d.State() == Speaking }, "not speaking")
	// A stray settle "idle" from an earlier recording must not hush speech.
	rig.bridge.bridgeState(0, "idle")
	time.Sleep(50 * time.Millisecond)
	if got := rig.d.State(); got != Speaking {
		t.Fatalf("stray idle moved FSM to %s during speaking", got)
	}
}

// --- New() validation --------------------------------------------------------

func TestNewRequiresTargetOrSender(t *testing.T) {
	_, err := New(Config{SocketPath: filepath.Join(t.TempDir(), "s.sock")})
	if err == nil || !strings.Contains(err.Error(), "target") {
		t.Fatalf("New without target/sender = %v", err)
	}
}

func TestNewAcceptsSenderWithoutTarget(t *testing.T) {
	d, err := New(Config{
		SocketPath: filepath.Join(t.TempDir(), "s.sock"),
		Sender:     &fakeSender{target: "%9"},
		NewBridge: func(BridgeDeps) (Bridge, error) {
			return &fakeBridge{target: ""}, nil
		},
		NewListener: func(hotkey.Key) (hotkey.Listener, error) { return &fakeListener{}, nil },
	})
	if err != nil {
		t.Fatalf("New with sender: %v", err)
	}
	_ = d
}

type fakeSender struct{ target string }

func (f *fakeSender) Send(context.Context, string, bool) error { return nil }
func (f *fakeSender) Target() string                           { return f.target }
