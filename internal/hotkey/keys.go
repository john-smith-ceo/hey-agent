package hotkey

import (
	"fmt"
	"sort"
	"strings"
)

// Category tells whether a key takes part in ordinary typing. Free keys do
// nothing on their own, so a bare press is unambiguously a command. Typing
// keys — Shift, Control, Option/Alt on the left — are pressed constantly while
// writing text, so only a solo press counts and hold-to-talk is impossible.
type Category int

const (
	Free Category = iota
	Typing
)

// Key is one selectable hotkey. Names follow the X11 convention on both
// platforms so that a configuration file is portable; macOS aliases such as
// Option_R and Command_L resolve to the same entry.
type Key struct {
	Name     string
	Aliases  []string
	Category Category

	// x11Keysym locates the key in the X keyboard mapping.
	x11Keysym uint32
	// darwinCode is the macOS virtual keycode; darwinAbsent marks a key that
	// no Apple keyboard has.
	darwinCode uint16
	// darwinMask is the CGEventFlags bit that reports this key as held.
	darwinMask uint64
}

const darwinAbsent uint16 = 0xFFFF

// SupportsPush reports whether hold-to-talk is meaningful for this key.
func (k Key) SupportsPush() bool { return k.Category == Free }

// AvailableOnDarwin reports whether an Apple keyboard has this key at all.
func (k Key) AvailableOnDarwin() bool { return k.darwinCode != darwinAbsent }

var keys = []Key{
	{Name: "Alt_R", Aliases: []string{"Option_R", "RightAlt", "RightOption"}, Category: Free,
		x11Keysym: 0xffea, darwinCode: 61, darwinMask: 0x00080000},
	{Name: "Super_R", Aliases: []string{"Command_R", "RightSuper", "RightCommand"}, Category: Free,
		x11Keysym: 0xffec, darwinCode: 54, darwinMask: 0x00100000},
	{Name: "Menu", Aliases: []string{"ContextMenu"}, Category: Free,
		x11Keysym: 0xff67, darwinCode: darwinAbsent},
	{Name: "Scroll_Lock", Aliases: nil, Category: Free,
		x11Keysym: 0xff14, darwinCode: darwinAbsent},
	{Name: "Pause", Aliases: nil, Category: Free,
		x11Keysym: 0xff13, darwinCode: darwinAbsent},
	{Name: "Caps_Lock", Aliases: nil, Category: Free,
		x11Keysym: 0xffe5, darwinCode: 57, darwinMask: 0x00010000},

	{Name: "Shift_L", Aliases: []string{"LeftShift"}, Category: Typing,
		x11Keysym: 0xffe1, darwinCode: 56, darwinMask: 0x00020000},
	{Name: "Shift_R", Aliases: []string{"RightShift"}, Category: Typing,
		x11Keysym: 0xffe2, darwinCode: 60, darwinMask: 0x00020000},
	{Name: "Control_L", Aliases: []string{"LeftControl", "Ctrl_L"}, Category: Typing,
		x11Keysym: 0xffe3, darwinCode: 59, darwinMask: 0x00040000},
	{Name: "Control_R", Aliases: []string{"RightControl", "Ctrl_R"}, Category: Typing,
		x11Keysym: 0xffe4, darwinCode: 62, darwinMask: 0x00040000},
	{Name: "Alt_L", Aliases: []string{"Option_L", "LeftAlt", "LeftOption"}, Category: Typing,
		x11Keysym: 0xffe9, darwinCode: 58, darwinMask: 0x00080000},
	{Name: "Super_L", Aliases: []string{"Command_L", "LeftSuper", "LeftCommand"}, Category: Typing,
		x11Keysym: 0xffeb, darwinCode: 55, darwinMask: 0x00100000},
}

// Default is the key used when none is configured. Right Alt (Right Option on
// macOS) does nothing on its own, which is exactly what a hotkey needs.
const Default = "Alt_R"

// Lookup resolves a key name or alias, ignoring case.
func Lookup(name string) (Key, error) {
	wanted := strings.ToLower(strings.TrimSpace(name))
	if wanted == "" {
		wanted = strings.ToLower(Default)
	}
	for _, key := range keys {
		if strings.ToLower(key.Name) == wanted {
			return key, nil
		}
		for _, alias := range key.Aliases {
			if strings.ToLower(alias) == wanted {
				return key, nil
			}
		}
	}
	return Key{}, fmt.Errorf("unknown key %q; run: hey-agent keys", name)
}

// Names lists every selectable key, free ones first.
func Names() []Key {
	listed := append([]Key(nil), keys...)
	sort.SliceStable(listed, func(i, j int) bool { return listed[i].Category < listed[j].Category })
	return listed
}
