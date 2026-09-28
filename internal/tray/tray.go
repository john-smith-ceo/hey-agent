// Package tray shows the hey-agent daemon as a Linux system tray icon.
//
// The icon is a StatusNotifierItem (SNI) — the tray contract every modern
// Linux host speaks (KDE Plasma, GNOME's AppIndicator extension, waybar,
// xfce-panel, swaync...). It lives entirely on the session D-Bus, so the
// daemon needs no X11, no cgo and no GTK: plain Go via godbus is enough.
//
// Two objects are exported on the session bus:
//
//   - /StatusNotifierItem       (org.kde.StatusNotifierItem) — status,
//     icon pixmaps, tooltip, and a pointer to the menu object.
//   - /StatusNotifierItem/menu  (com.canonical.dbusmenu) — the dropdown menu
//     the host renders natively.
//
// A tray host announces itself by owning the well-known bus name
// org.kde.StatusNotifierWatcher (the spec kept the org.kde.* namespace even
// though it is a freedesktop standard). Registration is one method call on
// that watcher; after that the host pulls our properties and listens to our
// New*/LayoutUpdated signals.
//
// Headless machines simply have no watcher: New returns ErrNoTray and the
// daemon keeps working, it just has no icon.
package tray

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	appTitle = "hey-agent"

	// sniPath is the object path the StatusNotifierItem lives at — the
	// conventional fixed path hosts expect when only a bus name is given.
	sniPath dbus.ObjectPath = "/StatusNotifierItem"
	// menuPath holds the com.canonical.dbusmenu object for the icon's menu.
	menuPath dbus.ObjectPath = "/StatusNotifierItem/menu"

	// watcherPath is the fixed object path of the StatusNotifierWatcher.
	watcherPath dbus.ObjectPath = "/StatusNotifierWatcher"

	ifaceSNI     = "org.kde.StatusNotifierItem"
	ifaceMenu    = "com.canonical.dbusmenu"
	ifaceProps   = "org.freedesktop.DBus.Properties"
	ifaceWatcher = "org.kde.StatusNotifierWatcher"

	busDaemonName = "org.freedesktop.DBus"
	busDaemonPath = "/org/freedesktop/DBus"
)

// watcherNames are the bus names a tray host owns to announce "I can show
// StatusNotifierItems". Virtually every implementation claims the org.kde
// name; the org.freedesktop one is a defensive fallback for the few minimal
// watchers that use it.
var watcherNames = []string{
	"org.kde.StatusNotifierWatcher",
	"org.freedesktop.StatusNotifierWatcher",
}

// ErrNoTray means "no tray host can be reached" — either there is no
// session bus at all, or nothing on it owns a StatusNotifierWatcher name.
// It is a normal condition on headless servers: check it with errors.Is and
// run the daemon without an icon.
var ErrNoTray = errors.New("tray: no status notifier available")

// State is the daemon's audio FSM state mirrored onto the tray icon.
type State string

const (
	StateIdle         State = "idle"
	StateRecording    State = "recording"
	StateTranscribing State = "transcribing"
	StateSpeaking     State = "speaking"
	StateError        State = "error"
)

// known reports whether s is one of the defined states.
func (s State) known() bool {
	switch s {
	case StateIdle, StateRecording, StateTranscribing, StateSpeaking, StateError:
		return true
	}
	return false
}

// Toggles holds the three user-visible switches the menu shows: whether the
// hotkey is grabbed, and the two checkmarks. The daemon owns the truth;
// the tray only displays whatever it is told via New/SetToggles.
type Toggles struct {
	Bound  bool // hotkey bound → "Unbind hotkey"; free → "Bind hotkey"
	Submit bool // "Submit automatically" checkmark
	Voice  bool // "Voice output" checkmark
}

// Actions are the daemon callbacks wired to menu items. Any nil func is
// treated as a no-op, so a partial Actions is safe.
//
// Callbacks run in their own goroutine — they may block or call back into
// the Tray (e.g. Quit → Close) without deadlocking the D-Bus dispatcher.
type Actions struct {
	ToggleBind    func() // "Bind/Unbind hotkey" clicked
	ToggleSubmit  func() // "Submit automatically" checkmark clicked
	ToggleVoice   func() // "Voice output" checkmark clicked
	SetAPIKey     func() // "Set API key…" clicked — opens a dialog
	VoiceSettings func() // "Voice settings…" clicked — opens a dialog
	About         func() // "About…" clicked — opens a dialog
	Hush          func() // "Hush" clicked
	Quit          func() // "Quit hey-agent" clicked
}

// Config configures a Tray. All fields have defaults; Actions and Toggles
// describe the initial menu.
type Config struct {
	// ID is the StatusNotifierItem Id property; defaults to "hey-agent".
	ID string
	// Title is the item title and menu header; defaults to "hey-agent".
	Title string
	// Toggles are the initial checkbox/bind states.
	Toggles Toggles
	// Actions are menu item callbacks.
	Actions Actions
	// Log receives non-fatal warnings (failed signal emissions, watcher
	// hiccups). Defaults to io.Discard.
	Log io.Writer
}

// Tray is a live StatusNotifierItem + dbusmenu on the session bus.
//
// Lifecycle: New exports everything and registers with the watcher, Run
// blocks until ctx is cancelled, Close tears down the bus connection.
// SetState/SetToggles may be called any time after New, including before
// Run — the properties are already live, signals just wait for listeners.
type Tray struct {
	cfg     Config
	b       bus
	watcher string // bus name of the watcher we registered with
	props   *propStore
	mprops  *propStore
	menuSrv *menuServer
	item    *sniItem

	running atomic.Bool // guards against a second concurrent Run

	mu       sync.Mutex // guards the fields below
	state    State
	toggles  Toggles
	revision uint32
	closed   bool
}

// New connects to the session bus, finds the StatusNotifierWatcher, exports
// the item and its menu, and registers with the watcher.
//
// It returns ErrNoTray (wrapped with context) when there is no bus or no
// watcher — the daemon should treat that as "run headless", not a failure.
func New(cfg Config) (*Tray, error) {
	b, err := sessionBusDial()
	if err != nil {
		return nil, fmt.Errorf("%w: connect session bus: %v", ErrNoTray, err)
	}
	t, err := newWithBus(cfg, b)
	if err != nil {
		_ = b.close()
		return nil, err
	}
	return t, nil
}

// Available reports whether a session bus with a StatusNotifierWatcher is
// reachable — a cheap probe for the daemon to decide between tray and
// headless mode without committing to a New.
func Available() bool {
	b, err := sessionBusDial()
	if err != nil {
		return false
	}
	defer func() { _ = b.close() }()
	return findWatcher(b) != ""
}

// findWatcher returns the first watcher name that currently has an owner.
func findWatcher(b bus) string {
	for _, name := range watcherNames {
		var owned bool
		if err := b.callStore(busDaemonName, busDaemonPath,
			busDaemonName+".NameHasOwner", &owned, name); err == nil && owned {
			return name
		}
	}
	return ""
}

// newWithBus is New's guts against an injected bus — the seam that lets
// tests exercise the whole D-Bus choreography with a fake.
func newWithBus(cfg Config, b bus) (*Tray, error) {
	if cfg.ID == "" {
		cfg.ID = appTitle
	}
	if cfg.Title == "" {
		cfg.Title = appTitle
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}

	watcher := findWatcher(b)
	if watcher == "" {
		return nil, fmt.Errorf("%w: no StatusNotifierWatcher on session bus", ErrNoTray)
	}

	t := &Tray{
		cfg:      cfg,
		b:        b,
		watcher:  watcher,
		state:    StateIdle,
		toggles:  cfg.Toggles,
		revision: 1,
		props:    newPropStore(nil),
		mprops:   newPropStore(nil),
	}
	t.menuSrv = &menuServer{t: t}
	t.item = &sniItem{}

	t.initProps()

	// Export order matters: everything must be on the bus before we
	// register, because the watcher queries our properties the moment it
	// sees the registration call.
	if err := t.exportAll(); err != nil {
		return nil, fmt.Errorf("tray: export objects: %w", err)
	}

	// A well-known name is not strictly needed (the watcher keys us by the
	// sender of the register call), but it makes `busctl list` readable and
	// costs one call. Failure is not fatal.
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	if err := b.requestName(name); err != nil {
		fmt.Fprintf(cfg.Log, "tray: request name %s: %v\n", name, err)
	}

	if err := t.register(); err != nil {
		return nil, fmt.Errorf("tray: register with watcher: %w", err)
	}
	return t, nil
}

// initProps fills both property stores with the initial state.
func (t *Tray) initProps() {
	icon := IconForState(t.state)
	t.props.update(ifaceSNI, "Category", "ApplicationStatus")
	t.props.update(ifaceSNI, "Id", t.cfg.ID)
	t.props.update(ifaceSNI, "Title", t.cfg.Title)
	t.props.update(ifaceSNI, "Status", statusForState(t.state))
	t.props.update(ifaceSNI, "WindowId", int32(0))
	// IconName is empty on purpose: we ship pixels, not theme names. A name
	// that resolves to nothing shows as a blank square on strict hosts;
	// an empty name plus IconPixmap renders everywhere.
	t.props.update(ifaceSNI, "IconName", "")
	t.props.update(ifaceSNI, "IconPixmap", icon.sniPixmaps())
	t.props.update(ifaceSNI, "IconThemePath", "")
	t.props.update(ifaceSNI, "OverlayIconName", "")
	t.props.update(ifaceSNI, "OverlayIconPixmap", []sniPixmap{})
	t.props.update(ifaceSNI, "AttentionIconName", "")
	t.props.update(ifaceSNI, "AttentionIconPixmap", []sniPixmap{})
	t.props.update(ifaceSNI, "AttentionMovieName", "")
	// ItemIsMenu=true: there is no "main window" to activate, so the only
	// sensible click action is the menu itself.
	t.props.update(ifaceSNI, "ItemIsMenu", true)
	t.props.update(ifaceSNI, "Menu", menuPath)
	t.props.update(ifaceSNI, "ToolTip", t.toolTip())

	t.mprops.update(ifaceMenu, "Version", t.revision)
	t.mprops.update(ifaceMenu, "TextDirection", "ltr")
	t.mprops.update(ifaceMenu, "Status", "normal")
	t.mprops.update(ifaceMenu, "IconThemePath", []string{})
}

// toolTip builds the (sa(iiay)ss) ToolTip value for the current state.
func (t *Tray) toolTip() sniToolTip {
	return sniToolTip{
		Pixmaps:  IconForState(t.state).sniPixmaps(),
		Title:    t.cfg.Title,
		SubTitle: string(t.state),
	}
}

// exportAll publishes every object and interface on the bus.
func (t *Tray) exportAll() error {
	for _, job := range []struct {
		path  dbus.ObjectPath
		iface string
		v     any
	}{
		{sniPath, ifaceSNI, t.item},
		{sniPath, ifaceProps, t.props},
		{menuPath, ifaceMenu, t.menuSrv},
		{menuPath, ifaceProps, t.mprops},
	} {
		if err := t.b.export(job.v, job.path, job.iface); err != nil {
			return fmt.Errorf("%s %s: %w", job.path, job.iface, err)
		}
	}
	// Introspection is optional per spec, but it makes `busctl introspect`
	// output useful when debugging a missing icon.
	sniNode := &introspect.Node{
		Name:       string(sniPath),
		Interfaces: []introspect.Interface{introspect.IntrospectData, propsInterface, sniInterface},
	}
	menuNode := &introspect.Node{
		Name:       string(menuPath),
		Interfaces: []introspect.Interface{introspect.IntrospectData, propsInterface, menuInterface},
	}
	if err := t.b.export(introspect.NewIntrospectable(sniNode), sniPath,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		return fmt.Errorf("%s introspectable: %w", sniPath, err)
	}
	if err := t.b.export(introspect.NewIntrospectable(menuNode), menuPath,
		"org.freedesktop.DBus.Introspectable"); err != nil {
		return fmt.Errorf("%s introspectable: %w", menuPath, err)
	}
	return nil
}

// register asks the watcher to adopt our item. The argument is our object
// path — KDE accepts either a bus name or a path; a path sidesteps any
// assumption about which names we own.
func (t *Tray) register() error {
	return t.b.call(t.watcher, watcherPath,
		ifaceWatcher+".RegisterStatusNotifierItem", string(sniPath))
}

// Run blocks until ctx is cancelled. godbus dispatches incoming method
// calls on its own goroutines, so the only work left for this loop is
// watching the watcher itself: panel applets and tray daemons restart, and
// a restarted watcher will never see us unless we register again.
func (t *Tray) Run(ctx context.Context) error {
	if !t.running.CompareAndSwap(false, true) {
		return errors.New("tray: Run is already in progress")
	}
	defer t.running.Store(false)

	match := "type='signal',sender='org.freedesktop.DBus'," +
		"interface='org.freedesktop.DBus',member='NameOwnerChanged'," +
		"path='/org/freedesktop/DBus',arg0='" + t.watcher + "'"
	if err := t.b.addMatch(match); err != nil {
		// Losing restart resilience is unfortunate, not fatal.
		fmt.Fprintf(t.cfg.Log, "tray: cannot watch for watcher restarts: %v\n", err)
	}
	signals := make(chan *dbus.Signal, 8)
	t.b.signal(signals)
	defer t.b.removeSignal(signals)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sig, ok := <-signals:
			if !ok {
				return errors.New("tray: signal channel closed")
			}
			if sig == nil {
				continue
			}
			if t.watcherCameBack(sig) {
				if err := t.register(); err != nil {
					fmt.Fprintf(t.cfg.Log, "tray: re-register with watcher: %v\n", err)
				}
			}
		}
	}
}

// watcherCameBack reports whether a NameOwnerChanged signal announces a new
// owner for our watcher's name. The signal body is
// (name, oldOwner, newOwner): a non-empty newOwner means the host (re)appeared.
func (t *Tray) watcherCameBack(sig *dbus.Signal) bool {
	if sig.Name != busDaemonName+".NameOwnerChanged" || len(sig.Body) != 3 {
		return false
	}
	name, _ := sig.Body[0].(string)
	newOwner, _ := sig.Body[2].(string)
	return name == t.watcher && newOwner != ""
}

// SetState switches the icon, the SNI Status property, the tooltip and the
// menu status line to a new daemon state, then emits the signals hosts
// listen to (NewStatus, NewIcon, NewToolTip, ItemsPropertiesUpdated,
// LayoutUpdated). Unknown states fall back to the idle visuals.
func (t *Tray) SetState(s State) {
	if !s.known() {
		// A state we cannot draw must never blank the icon — grey dot is
		// the least confusing fallback.
		s = StateIdle
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	oldStatus := statusForState(t.state)
	t.state = s
	status := statusForState(s)
	icon := IconForState(s)
	t.props.update(ifaceSNI, "Status", status)
	t.props.update(ifaceSNI, "IconPixmap", icon.sniPixmaps())
	t.props.update(ifaceSNI, "ToolTip", t.toolTip())
	rev := t.bumpRevisionLocked()
	statusUpdate := propUpdate{ID: itemStatusID, Properties: t.itemPropsByID(itemStatusID)}
	t.mu.Unlock()

	if status != oldStatus {
		t.emit(sniPath, ifaceSNI+".NewStatus", status)
	}
	t.emit(sniPath, ifaceSNI+".NewIcon")
	t.emit(sniPath, ifaceSNI+".NewToolTip")
	t.emitMenuRefresh([]propUpdate{statusUpdate}, rev)
}

// SetToggles refreshes the "Bind/Unbind" label and the two checkmarks and
// notifies hosts that those menu items changed.
func (t *Tray) SetToggles(tg Toggles) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.toggles = tg
	rev := t.bumpRevisionLocked()
	updates := make([]propUpdate, 0, 3)
	for _, id := range []int32{itemBindID, itemSubmitID, itemVoiceID} {
		updates = append(updates, propUpdate{ID: id, Properties: t.itemPropsByID(id)})
	}
	t.mu.Unlock()
	t.emitMenuRefresh(updates, rev)
}

// menu builds the current menu model. Caller must hold t.mu.
func (t *Tray) menu() []MenuItem {
	return menuModel(t.cfg.Title, t.toggles.Bound, t.toggles.Submit, t.toggles.Voice, t.state)
}

// itemPropsByID returns the serialized properties of one menu row.
// Caller must hold t.mu.
func (t *Tray) itemPropsByID(id int32) map[string]dbus.Variant {
	it, ok := findItem(t.menu(), id)
	if !ok {
		return map[string]dbus.Variant{}
	}
	return itemProps(it)
}

// bumpRevisionLocked advances the dbusmenu layout revision — the number
// hosts compare against their cached copy — and mirrors it into the menu's
// Version property, which per spec is that same revision.
// Caller must hold t.mu.
func (t *Tray) bumpRevisionLocked() uint32 {
	t.revision++
	t.mprops.update(ifaceMenu, "Version", t.revision)
	return t.revision
}

// emitMenuRefresh sends the two menu-change signals: ItemsPropertiesUpdated
// for fine-grained refreshes and LayoutUpdated as the blunt instrument some
// hosts rely on instead.
func (t *Tray) emitMenuRefresh(updates []propUpdate, revision uint32) {
	t.emit(menuPath, ifaceMenu+".ItemsPropertiesUpdated", updates, []removedProps{})
	t.emit(menuPath, ifaceMenu+".LayoutUpdated", revision, int32(rootID))
}

// emit sends one signal; failures are logged, never fatal — a tray host
// that cannot hear us anymore will notice on its own.
func (t *Tray) emit(path dbus.ObjectPath, name string, values ...any) {
	if err := t.b.emit(path, name, values...); err != nil {
		fmt.Fprintf(t.cfg.Log, "tray: emit %s: %v\n", name, err)
	}
}

// dispatch maps a clicked menu item id to its Actions callback and runs it
// in a new goroutine: callbacks may block (Quit doing a graceful shutdown)
// or call back into the Tray (Quit → Close) — either would deadlock the
// D-Bus dispatcher if run inline.
func (t *Tray) dispatch(id int32) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	it, ok := findItem(t.menu(), id)
	var fn func()
	if ok {
		switch it.action {
		case actBind:
			fn = t.cfg.Actions.ToggleBind
		case actSubmit:
			fn = t.cfg.Actions.ToggleSubmit
		case actVoice:
			fn = t.cfg.Actions.ToggleVoice
		case actAPIKey:
			fn = t.cfg.Actions.SetAPIKey
		case actVoiceUI:
			fn = t.cfg.Actions.VoiceSettings
		case actAbout:
			fn = t.cfg.Actions.About
		case actHush:
			fn = t.cfg.Actions.Hush
		case actQuit:
			fn = t.cfg.Actions.Quit
		}
	}
	t.mu.Unlock()
	if fn != nil {
		go fn()
	}
}

// Close drops the bus connection and makes the tray inert. Idempotent.
func (t *Tray) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	t.mu.Unlock()
	return t.b.close()
}

// sniItem carries the org.kde.StatusNotifierItem *methods*. All of them are
// no-ops: ItemIsMenu=true tells the host every click just opens the menu,
// and there is no window to activate or second action to run. The methods
// still must exist — some hosts call Activate unconditionally.
type sniItem struct{}

// ContextMenu is invoked on right-click. ItemIsMenu=true already routes the
// click to the menu, so this exists only to answer politely.
func (*sniItem) ContextMenu(x, y int32) *dbus.Error { return nil }

// Activate is invoked on left-click — see ContextMenu.
func (*sniItem) Activate(x, y int32) *dbus.Error { return nil }

// SecondaryActivate is invoked on middle-click — see ContextMenu.
func (*sniItem) SecondaryActivate(x, y int32) *dbus.Error { return nil }

// Scroll is invoked on wheel events over the icon — we have nothing to
// scroll, so the event is acknowledged and dropped.
func (*sniItem) Scroll(delta int32, orientation string) *dbus.Error { return nil }
