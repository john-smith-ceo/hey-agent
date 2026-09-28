package tray

import (
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// The menu skeleton is fixed by SPEC §6: header, separator, bind item, two
// checkmarks, separator, hush, quit — in exactly that order.
func TestMenuModel_Shape(t *testing.T) {
	items := MenuModel(false, false, false, StateIdle)
	if len(items) != 12 {
		t.Fatalf("MenuModel returned %d items, want 12", len(items))
	}
	wantIDs := []int32{itemStatusID, itemSepTopID, itemBindID, itemSubmitID, itemVoiceID,
		itemSepMidID, itemAPIKeyID, itemVoiceSetID, itemAboutID, itemSepBotID, itemHushID, itemQuitID}
	for i, id := range wantIDs {
		if items[i].ID != id {
			t.Errorf("item %d: ID %d, want %d", i, items[i].ID, id)
		}
	}
	if items[0].Label != "hey-agent — idle" {
		t.Errorf("status label = %q, want %q", items[0].Label, "hey-agent — idle")
	}
	if items[0].Enabled {
		t.Error("status header must be disabled — it is a label, not a button")
	}
	for _, i := range []int{1, 5, 9} {
		if items[i].Type != "separator" {
			t.Errorf("item %d must be a separator", i)
		}
	}
	last := items[len(items)-1]
	if last.Label != "Quit hey-agent" || last.action != actQuit {
		t.Errorf("last item = %+v, want Quit hey-agent", last)
	}
	for _, i := range []int{6, 7, 8} {
		if !strings.HasSuffix(items[i].Label, "…") {
			t.Errorf("dialog item %d label %q must end with …", i, items[i].Label)
		}
	}
}

// The bind item is a single entry whose label follows the bound flag.
func TestMenuModel_BindLabel(t *testing.T) {
	items := MenuModel(false, false, false, StateIdle)
	if items[2].Label != "Bind hotkey" {
		t.Errorf("unbound label = %q, want %q", items[2].Label, "Bind hotkey")
	}
	items = MenuModel(true, false, false, StateIdle)
	if items[2].Label != "Unbind hotkey" {
		t.Errorf("bound label = %q, want %q", items[2].Label, "Unbind hotkey")
	}
}

// The two checkmarks mirror the submit/voice flags as toggle-state 0/1.
func TestMenuModel_Checkmarks(t *testing.T) {
	items := MenuModel(false, true, true, StateSpeaking)
	submit, voice := items[3], items[4]
	if submit.ToggleType != "checkmark" || submit.ToggleState != 1 {
		t.Errorf("submit item = %+v, want checkmark state 1", submit)
	}
	if voice.ToggleType != "checkmark" || voice.ToggleState != 1 {
		t.Errorf("voice item = %+v, want checkmark state 1", voice)
	}
	items = MenuModel(false, false, false, StateSpeaking)
	if items[3].ToggleState != 0 || items[4].ToggleState != 0 {
		t.Error("unchecked toggles must have ToggleState 0")
	}
}

// The state string is shown in the header; unknown states degrade to idle.
func TestMenuModel_StateInHeader(t *testing.T) {
	items := MenuModel(false, false, false, StateRecording)
	if items[0].Label != "hey-agent — recording" {
		t.Errorf("header = %q, want %q", items[0].Label, "hey-agent — recording")
	}
	items = MenuModel(false, false, false, State("garbage"))
	if items[0].Label != "hey-agent — idle" {
		t.Errorf("unknown-state header = %q, want idle fallback", items[0].Label)
	}
}

// itemProps must produce the dbusmenu wire properties a host reads.
func TestItemProps_WireFormat(t *testing.T) {
	items := MenuModel(true, true, false, StateError)
	props := itemProps(items[3]) // submit checkmark
	if props["toggle-type"].Value() != "checkmark" {
		t.Errorf("toggle-type = %v, want checkmark", props["toggle-type"].Value())
	}
	if props["toggle-state"].Value() != int32(1) {
		t.Errorf("toggle-state = %v, want int32(1)", props["toggle-state"].Value())
	}
	if props["label"].Value() != "Submit automatically" {
		t.Errorf("label = %v", props["label"].Value())
	}
	sep := itemProps(items[1])
	if sep["type"].Value() != "separator" || len(sep) != 3 {
		t.Errorf("separator props = %v, want minimal {type,enabled,visible}", sep)
	}
}

// pickProps honors the client's property filter; empty filter = all.
func TestPickProps(t *testing.T) {
	props := map[string]dbus.Variant{
		"label":   dbus.MakeVariant("x"),
		"enabled": dbus.MakeVariant(true),
	}
	got := pickProps(props, []string{"label", "missing"})
	if len(got) != 1 || got["label"].Value() != "x" {
		t.Errorf("pickProps filter = %v", got)
	}
	if got := pickProps(props, nil); len(got) != 2 {
		t.Errorf("pickProps nil filter = %v, want all", got)
	}
}
