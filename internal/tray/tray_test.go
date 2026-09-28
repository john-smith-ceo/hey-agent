package tray

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// fakeBus records everything the tray does on the bus so the whole D-Bus
// choreography — probing the watcher, exporting objects, registering,
// emitting signals — can be verified without a session bus. Signal delivery
// goes through sigCh, which the test fills once Run registers it.
type fakeBus struct {
	mu         sync.Mutex
	owners     map[string]bool // well-known name → owned, for NameHasOwner
	calls      []fakeCall
	exports    []fakeExport
	emitted    []fakeEmit
	names      []string
	matches    []string
	callErr    map[string]error // method → injected failure
	closed     bool
	sigCh      chan<- *dbus.Signal
	registered []string // every RegisterStatusNotifierItem argument
}

type fakeCall struct {
	dest, path, method string
	args               []any
}

type fakeExport struct {
	path  dbus.ObjectPath
	iface string
}

type fakeEmit struct {
	path   dbus.ObjectPath
	name   string
	values []any
}

func newFakeBus() *fakeBus {
	return &fakeBus{
		owners:  map[string]bool{"org.kde.StatusNotifierWatcher": true},
		callErr: map[string]error{},
	}
}

func (f *fakeBus) call(dest string, path dbus.ObjectPath, method string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{dest, string(path), method, args})
	if method == ifaceWatcher+".RegisterStatusNotifierItem" && len(args) > 0 {
		f.registered = append(f.registered, fmt.Sprint(args[0]))
	}
	return f.callErr[method]
}

func (f *fakeBus) callStore(dest string, path dbus.ObjectPath, method string, into any, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{dest, string(path), method, args})
	if err := f.callErr[method]; err != nil {
		return err
	}
	if method == busDaemonName+".NameHasOwner" && len(args) > 0 {
		if out, ok := into.(*bool); ok {
			*out = f.owners[fmt.Sprint(args[0])]
		}
	}
	return nil
}

func (f *fakeBus) export(v any, path dbus.ObjectPath, iface string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exports = append(f.exports, fakeExport{path, iface})
	return nil
}

func (f *fakeBus) emit(path dbus.ObjectPath, name string, values ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emitted = append(f.emitted, fakeEmit{path, name, values})
	return nil
}

func (f *fakeBus) requestName(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.names = append(f.names, name)
	return nil
}

func (f *fakeBus) addMatch(match string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.matches = append(f.matches, match)
	return nil
}

func (f *fakeBus) signal(ch chan<- *dbus.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigCh = ch
}

func (f *fakeBus) removeSignal(ch chan<- *dbus.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigCh = nil
}

func (f *fakeBus) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

// emittedNames returns the signal names recorded so far, in order.
func (f *fakeBus) emittedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.emitted))
	for i, e := range f.emitted {
		out[i] = e.name
	}
	return out
}

func (f *fakeBus) hasEmit(name string) bool {
	for _, n := range f.emittedNames() {
		if n == name {
			return true
		}
	}
	return false
}

// signalChannel waits until Run has registered its signal channel.
func (f *fakeBus) signalChannel(t *testing.T) chan<- *dbus.Signal {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		ch := f.sigCh
		f.mu.Unlock()
		if ch != nil {
			return ch
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Run never registered a signal channel")
	return nil
}

// registeredCount waits for the registration call count to reach n.
func (f *fakeBus) registeredCount(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		got := len(f.registered)
		f.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("registered %d times, want %d", len(f.registered), n)
}

func newTray(t *testing.T, cfg Config) (*Tray, *fakeBus) {
	t.Helper()
	fb := newFakeBus()
	tr, err := newWithBus(cfg, fb)
	if err != nil {
		t.Fatalf("newWithBus: %v", err)
	}
	return tr, fb
}

// --- New / availability ---------------------------------------------------

func TestNew_ExportsAndRegisters(t *testing.T) {
	tr, fb := newTray(t, Config{})
	defer tr.Close()

	// Both objects and both property interfaces must be on the bus before
	// registration — the watcher queries them the moment we register.
	wantExports := map[string]bool{
		"/StatusNotifierItem org.kde.StatusNotifierItem":               false,
		"/StatusNotifierItem org.freedesktop.DBus.Properties":          false,
		"/StatusNotifierItem org.freedesktop.DBus.Introspectable":      false,
		"/StatusNotifierItem/menu com.canonical.dbusmenu":              false,
		"/StatusNotifierItem/menu org.freedesktop.DBus.Properties":     false,
		"/StatusNotifierItem/menu org.freedesktop.DBus.Introspectable": false,
	}
	for _, e := range fb.exports {
		key := fmt.Sprintf("%s %s", e.path, e.iface)
		if _, ok := wantExports[key]; ok {
			wantExports[key] = true
		}
	}
	for key, seen := range wantExports {
		if !seen {
			t.Errorf("missing export %s", key)
		}
	}
	if len(fb.registered) != 1 || fb.registered[0] != "/StatusNotifierItem" {
		t.Errorf("registered = %v, want [/StatusNotifierItem]", fb.registered)
	}
	if len(fb.names) != 1 || !strings.HasPrefix(fb.names[0], "org.kde.StatusNotifierItem-") {
		t.Errorf("names = %v, want org.kde.StatusNotifierItem-<pid>-1", fb.names)
	}
	if got, derr := tr.props.Get(ifaceSNI, "Id"); derr != nil || got.Value() != "hey-agent" {
		t.Errorf("Id property = %v, %v", got.Value(), derr)
	}
	if got, _ := tr.props.Get(ifaceSNI, "ItemIsMenu"); got.Value() != true {
		t.Error("ItemIsMenu must be true — clicks open the menu")
	}
	if got, _ := tr.props.Get(ifaceSNI, "Menu"); got.Value() != menuPath {
		t.Errorf("Menu property = %v, want %s", got.Value(), menuPath)
	}
	if got, _ := tr.props.Get(ifaceSNI, "IconPixmap"); got.Value() == nil {
		t.Error("IconPixmap must carry pixels, not an empty value")
	}
}

func TestNew_NoWatcherReturnsErrNoTray(t *testing.T) {
	fb := newFakeBus()
	fb.owners = map[string]bool{}
	_, err := newWithBus(Config{}, fb)
	if !errors.Is(err, ErrNoTray) {
		t.Fatalf("err = %v, want ErrNoTray", err)
	}
}

func TestNew_RegisterFailurePropagates(t *testing.T) {
	fb := newFakeBus()
	fb.callErr[ifaceWatcher+".RegisterStatusNotifierItem"] = errors.New("watcher refused")
	_, err := newWithBus(Config{}, fb)
	if err == nil || errors.Is(err, ErrNoTray) {
		t.Fatalf("err = %v, want wrapped register failure", err)
	}
}

// New against the real session bus: green either way — headless CI yields
// ErrNoTray, a developer desktop yields a registered tray.
func TestNew_RealBusBestEffort(t *testing.T) {
	if b, err := sessionBusDial(); err == nil {
		_ = b.close()
		t.Skip("session bus available — headless path not exercised")
	}
	tr, err := New(Config{})
	if !errors.Is(err, ErrNoTray) {
		t.Fatalf("New without session bus: err = %v, want ErrNoTray", err)
	}
	if tr != nil {
		t.Fatal("New returned a tray and an error")
	}
}

// --- SetState / SetToggles ------------------------------------------------

func TestSetState_EmitsIconAndStatus(t *testing.T) {
	tr, fb := newTray(t, Config{})
	defer tr.Close()

	tr.SetState(StateRecording)
	for _, want := range []string{
		ifaceSNI + ".NewIcon",
		ifaceSNI + ".NewToolTip",
		ifaceMenu + ".ItemsPropertiesUpdated",
		ifaceMenu + ".LayoutUpdated",
	} {
		if !fb.hasEmit(want) {
			t.Errorf("after SetState(recording): missing %s in %v", want, fb.emittedNames())
		}
	}
	// idle→recording keeps Status "Active", so no NewStatus yet.
	if fb.hasEmit(ifaceSNI + ".NewStatus") {
		t.Error("NewStatus must only fire when the Status value changes")
	}

	tr.SetState(StateError)
	if !fb.hasEmit(ifaceSNI + ".NewStatus") {
		t.Error("error state must emit NewStatus")
	}
	if got, _ := tr.props.Get(ifaceSNI, "Status"); got.Value() != "NeedsAttention" {
		t.Errorf("Status = %v, want NeedsAttention", got.Value())
	}
}

func TestSetState_UnknownFallsBack(t *testing.T) {
	tr, _ := newTray(t, Config{})
	defer tr.Close()
	tr.SetState(State("nonsense"))
	if got, _ := tr.props.Get(ifaceSNI, "Status"); got.Value() != "Active" {
		t.Errorf("Status after unknown state = %v, want Active", got.Value())
	}
	if tr.state != StateIdle {
		t.Errorf("internal state = %q, want idle", tr.state)
	}
}

func TestSetToggles_RefreshesCheckmarks(t *testing.T) {
	tr, fb := newTray(t, Config{})
	defer tr.Close()

	tr.SetToggles(Toggles{Bound: true, Submit: true})
	if !fb.hasEmit(ifaceMenu + ".ItemsPropertiesUpdated") {
		t.Fatal("SetToggles must emit ItemsPropertiesUpdated")
	}
	items := tr.menu()
	bind, _ := findItem(items, itemBindID)
	if bind.Label != "Unbind hotkey" {
		t.Errorf("bind label = %q, want Unbind hotkey", bind.Label)
	}
	submit, _ := findItem(items, itemSubmitID)
	if submit.ToggleState != 1 {
		t.Errorf("submit ToggleState = %d, want 1", submit.ToggleState)
	}
	voice, _ := findItem(items, itemVoiceID)
	if voice.ToggleState != 0 {
		t.Errorf("voice ToggleState = %d, want 0", voice.ToggleState)
	}
}

// --- dbusmenu server ------------------------------------------------------

func TestGetLayout_ReturnsMenu(t *testing.T) {
	tr, _ := newTray(t, Config{})
	defer tr.Close()

	rev, layout, derr := tr.menuSrv.GetLayout(rootID, -1, nil)
	if derr != nil {
		t.Fatalf("GetLayout: %v", derr)
	}
	if rev == 0 {
		t.Error("revision must start at 1")
	}
	if layout.Properties["children-display"].Value() != "submenu" {
		t.Error("root must carry children-display=submenu")
	}
	if len(layout.Children) != 12 {
		t.Fatalf("root has %d children, want 12", len(layout.Children))
	}
	// Depth 0 asks for the node only.
	_, shallow, _ := tr.menuSrv.GetLayout(rootID, 0, nil)
	if len(shallow.Children) != 0 {
		t.Error("recursionDepth=0 must return no children")
	}
	// A single item resolves by id, and the property filter applies.
	_, item, derr := tr.menuSrv.GetLayout(itemQuitID, -1, []string{"label"})
	if derr != nil {
		t.Fatalf("GetLayout(itemQuitID): %v", derr)
	}
	if len(item.Properties) != 1 || item.Properties["label"].Value() != "Quit hey-agent" {
		t.Errorf("filtered item props = %v", item.Properties)
	}
	if _, _, derr := tr.menuSrv.GetLayout(99, -1, nil); derr == nil {
		t.Error("GetLayout(99) must fail")
	}
}

func TestMenuEvent_DispatchesActions(t *testing.T) {
	fired := make(chan string, 4)
	tr, _ := newTray(t, Config{Actions: Actions{
		ToggleBind:   func() { fired <- "bind" },
		ToggleSubmit: func() { fired <- "submit" },
		ToggleVoice:  func() { fired <- "voice" },
		Hush:         func() { fired <- "hush" },
		Quit:         func() { fired <- "quit" },
	}})
	defer tr.Close()

	for id, want := range map[int32]string{
		itemBindID: "bind", itemSubmitID: "submit", itemVoiceID: "voice",
		itemHushID: "hush", itemQuitID: "quit",
	} {
		if derr := tr.menuSrv.Event(id, "clicked", dbus.Variant{}, 0); derr != nil {
			t.Fatalf("Event(%d): %v", id, derr)
		}
		select {
		case got := <-fired:
			if got != want {
				t.Errorf("click on %d fired %q, want %q", id, got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("click on %d fired nothing", id)
		}
	}
	// Non-click events and unknown ids must not panic or fire.
	tr.menuSrv.Event(itemHushID, "hovered", dbus.Variant{}, 0)
	tr.menuSrv.Event(77, "clicked", dbus.Variant{}, 0)
	select {
	case got := <-fired:
		t.Errorf("unexpected action %q", got)
	case <-time.After(50 * time.Millisecond):
	}
}

// --- Run / lifecycle -------------------------------------------------------

// A restarted watcher comes back with a fresh bus name; Run must notice the
// NameOwnerChanged and register again or the icon silently vanishes.
func TestRun_ReregistersOnWatcherRestart(t *testing.T) {
	tr, fb := newTray(t, Config{})
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()

	ch := fb.signalChannel(t)
	fb.mu.Lock()
	if len(fb.matches) != 1 {
		t.Fatalf("matches = %v, want one NameOwnerChanged match", fb.matches)
	}
	fb.mu.Unlock()

	ch <- &dbus.Signal{
		Name: "org.freedesktop.DBus.NameOwnerChanged",
		Body: []any{"org.kde.StatusNotifierWatcher", "", ":1.99"},
	}
	fb.registeredCount(t, 2)

	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

func TestRun_OnlyOnce(t *testing.T) {
	tr, fb := newTray(t, Config{})
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()

	// Wait until the first Run is inside its loop — otherwise this call
	// could win the running flag itself and block forever.
	fb.signalChannel(t)
	if err := tr.Run(ctx); err == nil {
		t.Error("second Run must fail")
	}
	cancel()
	<-done
}

func TestClose_IdempotentAndInert(t *testing.T) {
	tr, fb := newTray(t, Config{})
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	fb.mu.Lock()
	closed := fb.closed
	fb.mu.Unlock()
	if !closed {
		t.Error("Close must close the bus")
	}
	before := len(fb.emittedNames())
	tr.SetState(StateError)
	tr.SetToggles(Toggles{Submit: true})
	if got := len(fb.emittedNames()); got != before {
		t.Errorf("emits after Close: %d → %d", before, got)
	}
}

// --- propStore ------------------------------------------------------------

func TestPropStore_ReadOnly(t *testing.T) {
	store := newPropStore(map[string]map[string]dbus.Variant{
		"iface": {"Label": dbus.MakeVariant("x")},
	})
	if v, err := store.Get("iface", "Label"); err != nil || v.Value() != "x" {
		t.Errorf("Get = %v, %v", v.Value(), err)
	}
	if _, err := store.Get("iface", "Nope"); err == nil {
		t.Error("Get of unknown property must fail")
	}
	if all, _ := store.GetAll("ghost"); len(all) != 0 {
		t.Errorf("GetAll(unknown) = %v, want empty map", all)
	}
	if err := store.Set("iface", "Label", dbus.MakeVariant("y")); err == nil {
		t.Error("Set must be refused — tray properties are read-only")
	}
}
