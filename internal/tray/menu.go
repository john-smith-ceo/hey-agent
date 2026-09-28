package tray

import (
	"fmt"

	"github.com/godbus/dbus/v5"
)

// Menu item IDs. dbusmenu address items by a caller-chosen integer id —
// fixed ids keep the Event→action dispatch a plain lookup.
const (
	itemStatusID int32 = 1
	itemSepTopID int32 = 2
	itemBindID   int32 = 3
	itemSubmitID int32 = 4
	itemVoiceID  int32 = 5
	itemSepBotID int32 = 6
	itemHushID   int32 = 7
	itemQuitID   int32 = 8

	rootID int32 = 0 // the invisible root node every layout hangs off
)

// action names — MenuItem.action strings resolved to Config.Actions funcs
// on click.
const (
	actNone   = ""
	actBind   = "bind"
	actSubmit = "submit"
	actVoice  = "voice"
	actHush   = "hush"
	actQuit   = "quit"
)

// MenuItem is one row of the tray menu in plain-data form. MenuModel builds
// the whole list without touching D-Bus, which keeps the menu structure
// (labels, checkmarks, order) fully unit-testable.
type MenuItem struct {
	ID          int32
	Type        string // "standard" or "separator" (dbusmenu "type" property)
	Label       string
	Enabled     bool
	ToggleType  string // "" or "checkmark" — a checkmark item gets a toggle-state
	ToggleState int32  // 0 unchecked, 1 checked; ignored unless ToggleType set

	// action names the Config.Actions member to invoke on click.
	action string
}

// MenuModel returns the full menu for the given toggle states and daemon
// state, in display order. This is the single source of truth for what the
// tray menu looks like; the D-Bus side only serializes it.
//
// Layout (per SPEC-HEY-AGENT §6):
//
//	hey-agent — <state>          header, disabled
//	───────────────
//	Bind hotkey / Unbind hotkey  one item, label follows the bound flag
//	✓ Submit automatically       checkmark
//	✓ Voice output               checkmark
//	───────────────
//	Hush
//	Quit hey-agent
func MenuModel(bound, submit, voiceOn bool, state State) []MenuItem {
	return menuModel(appTitle, bound, submit, voiceOn, state)
}

// menuModel is MenuModel with the product title as a parameter, so a Config
// Title override reaches the menu too.
func menuModel(title string, bound, submit, voiceOn bool, state State) []MenuItem {
	if !state.known() {
		state = StateIdle
	}
	bindLabel := "Bind hotkey"
	if bound {
		bindLabel = "Unbind hotkey"
	}
	return []MenuItem{
		{ID: itemStatusID, Type: "standard", Label: fmt.Sprintf("%s — %s", title, state), Enabled: false},
		{ID: itemSepTopID, Type: "separator"},
		{ID: itemBindID, Type: "standard", Label: bindLabel, Enabled: true, action: actBind},
		{ID: itemSubmitID, Type: "standard", Label: "Submit automatically", Enabled: true,
			ToggleType: "checkmark", ToggleState: toggleState(submit), action: actSubmit},
		{ID: itemVoiceID, Type: "standard", Label: "Voice output", Enabled: true,
			ToggleType: "checkmark", ToggleState: toggleState(voiceOn), action: actVoice},
		{ID: itemSepBotID, Type: "separator"},
		{ID: itemHushID, Type: "standard", Label: "Hush", Enabled: true, action: actHush},
		{ID: itemQuitID, Type: "standard", Label: "Quit " + title, Enabled: true, action: actQuit},
	}
}

func toggleState(on bool) int32 {
	if on {
		return 1
	}
	return 0
}

// itemProps serializes a MenuItem into the dbusmenu property map (a{sv}).
// Property names are the dbusmenu spec strings — hosts render by them, not
// by our Go field names.
func itemProps(it MenuItem) map[string]dbus.Variant {
	props := map[string]dbus.Variant{
		"type":    dbus.MakeVariant(it.Type),
		"enabled": dbus.MakeVariant(it.Enabled),
		"visible": dbus.MakeVariant(true),
	}
	if it.Type != "separator" {
		props["label"] = dbus.MakeVariant(it.Label)
	}
	if it.ToggleType != "" {
		props["toggle-type"] = dbus.MakeVariant(it.ToggleType)
		props["toggle-state"] = dbus.MakeVariant(it.ToggleState)
	}
	return props
}

// pickProps filters a property map to the names the client asked for;
// an empty filter list means "all properties".
func pickProps(props map[string]dbus.Variant, names []string) map[string]dbus.Variant {
	if len(names) == 0 {
		return props
	}
	out := make(map[string]dbus.Variant, len(names))
	for _, name := range names {
		if v, ok := props[name]; ok {
			out[name] = v
		}
	}
	return out
}

// findItem locates one model row by dbusmenu id.
func findItem(items []MenuItem, id int32) (MenuItem, bool) {
	for _, it := range items {
		if it.ID == id {
			return it, true
		}
	}
	return MenuItem{}, false
}

// ---------------------------------------------------------------------------
// D-Bus wire shapes
//
// The dbusmenu spec uses anonymous structs everywhere; godbus maps Go
// structs onto D-Bus structs field-by-field, so these types are the wire
// format spelled out in Go.
// ---------------------------------------------------------------------------

// layoutItem is (ia{sv}av): item id, property map, children as variants.
// dbusmenu requires children as *variants* of this same struct so a client
// can recurse lazily.
type layoutItem struct {
	ID         int32
	Properties map[string]dbus.Variant
	Children   []dbus.Variant
}

// propUpdate is (ia{sv}): item id + its full property map. Used both as a
// GetGroupProperties result and inside ItemsPropertiesUpdated signals.
type propUpdate struct {
	ID         int32
	Properties map[string]dbus.Variant
}

// removedProps is (ias): item id + names of properties that went away. We
// never remove properties, so it is only ever emitted empty.
type removedProps struct {
	ID    int32
	Names []string
}

// menuEvent is (isvu): one entry of an EventGroup batch — item id, event
// kind ("clicked", "opened", ...), event payload, timestamp.
type menuEvent struct {
	ID        int32
	EventID   string
	Data      dbus.Variant
	Timestamp uint32
}

// menuServer exposes the model over com.canonical.dbusmenu. Tray hosts call
// GetLayout once to draw the menu and then keep it fresh from our
// LayoutUpdated / ItemsPropertiesUpdated signals.
type menuServer struct {
	t *Tray
}

// GetLayout returns the revision and the layout subtree under parentID.
// recursionDepth: -1 means "everything"; for our flat menu any depth ≥1
// reaches the leaf items, depth 0 returns just the node itself.
func (m *menuServer) GetLayout(parentID int32, recursionDepth int32, propertyNames []string) (uint32, layoutItem, *dbus.Error) {
	t := m.t
	t.mu.Lock()
	defer t.mu.Unlock()

	items := t.menu()
	if parentID == rootID {
		node := layoutItem{
			ID: rootID,
			// children-display=submenu tells the host "paint my children as
			// a dropdown" — without it the root row itself would be rendered.
			Properties: pickProps(map[string]dbus.Variant{
				"children-display": dbus.MakeVariant("submenu"),
				"enabled":          dbus.MakeVariant(true),
			}, propertyNames),
			Children: childLayouts(items, recursionDepth, propertyNames),
		}
		return t.revision, node, nil
	}
	it, ok := findItem(items, parentID)
	if !ok {
		return 0, layoutItem{}, dbus.MakeFailedError(fmt.Errorf("tray: no menu item %d", parentID))
	}
	return t.revision, layoutItem{
		ID:         it.ID,
		Properties: pickProps(itemProps(it), propertyNames),
		Children:   []dbus.Variant{},
	}, nil
}

// childLayouts wraps model rows as av children of the root layout.
func childLayouts(items []MenuItem, depth int32, propertyNames []string) []dbus.Variant {
	if depth == 0 {
		return nil
	}
	out := make([]dbus.Variant, 0, len(items))
	for _, it := range items {
		out = append(out, dbus.MakeVariant(layoutItem{
			ID:         it.ID,
			Properties: pickProps(itemProps(it), propertyNames),
			Children:   []dbus.Variant{},
		}))
	}
	return out
}

// GetGroupProperties returns property maps for a batch of item ids —
// clients use it to refresh several rows at once.
func (m *menuServer) GetGroupProperties(ids []int32, propertyNames []string) ([]propUpdate, *dbus.Error) {
	t := m.t
	t.mu.Lock()
	defer t.mu.Unlock()
	items := t.menu()
	out := make([]propUpdate, 0, len(ids))
	for _, id := range ids {
		if id == rootID {
			out = append(out, propUpdate{ID: rootID, Properties: map[string]dbus.Variant{
				"children-display": dbus.MakeVariant("submenu"),
			}})
			continue
		}
		if it, ok := findItem(items, id); ok {
			out = append(out, propUpdate{ID: it.ID, Properties: pickProps(itemProps(it), propertyNames)})
		}
	}
	return out, nil
}

// GetProperty reads a single property of a single item.
func (m *menuServer) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	t := m.t
	t.mu.Lock()
	defer t.mu.Unlock()
	items := t.menu()
	it, ok := findItem(items, id)
	if !ok {
		return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("tray: no menu item %d", id))
	}
	if v, ok := itemProps(it)[name]; ok {
		return v, nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("tray: item %d has no property %q", id, name))
}

// Event handles a single menu event. Only "clicked" carries a meaning for
// us: "opened"/"closed"/"hovered" arrive too but need no reaction — our
// menu is always kept up to date proactively.
func (m *menuServer) Event(id int32, eventID string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventID == "clicked" {
		m.t.dispatch(id)
	}
	return nil
}

// EventGroup is the batched form of Event.
func (m *menuServer) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	for _, e := range events {
		if e.EventID == "clicked" {
			m.t.dispatch(e.ID)
		}
	}
	return nil, nil
}

// AboutToShow asks whether the menu changed since the last GetLayout. We
// push updates eagerly via signals, so the layout is always fresh.
func (m *menuServer) AboutToShow(id int32) (bool, *dbus.Error) {
	return false, nil
}

// AboutToShowGroup is the batched form of AboutToShow.
func (m *menuServer) AboutToShowGroup(ids []int32) (updatesNeeded []int32, idErrors []int32, err *dbus.Error) {
	return nil, nil, nil
}
