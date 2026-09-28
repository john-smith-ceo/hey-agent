package tray

import (
	"github.com/godbus/dbus/v5/introspect"
)

// Introspection is not required by the SNI protocol — hosts discover us via
// GetAll — but it makes `busctl introspect` output self-explanatory when
// debugging why an icon does not show up.

// args is a small helper to keep the interface declarations readable.
func args(pairs ...string) []introspect.Arg {
	out := make([]introspect.Arg, 0, len(pairs)/2)
	for i := 0; i+2 < len(pairs); i += 3 {
		out = append(out, introspect.Arg{Name: pairs[i], Type: pairs[i+1], Direction: pairs[i+2]})
	}
	return out
}

// sniInterface describes org.kde.StatusNotifierItem as we implement it.
var sniInterface = introspect.Interface{
	Name: ifaceSNI,
	Methods: []introspect.Method{
		{Name: "ContextMenu", Args: args("x", "i", "in", "y", "i", "in")},
		{Name: "Activate", Args: args("x", "i", "in", "y", "i", "in")},
		{Name: "SecondaryActivate", Args: args("x", "i", "in", "y", "i", "in")},
		{Name: "Scroll", Args: args("delta", "i", "in", "orientation", "s", "in")},
	},
	Signals: []introspect.Signal{
		{Name: "NewTitle"},
		{Name: "NewIcon"},
		{Name: "NewToolTip"},
		{Name: "NewStatus", Args: args("status", "s", "out")},
		{Name: "NewIconThemePath", Args: args("icon_theme_path", "s", "out")},
	},
	Properties: []introspect.Property{
		{Name: "Category", Type: "s", Access: "read"},
		{Name: "Id", Type: "s", Access: "read"},
		{Name: "Title", Type: "s", Access: "read"},
		{Name: "Status", Type: "s", Access: "read"},
		{Name: "WindowId", Type: "i", Access: "read"},
		{Name: "IconName", Type: "s", Access: "read"},
		{Name: "IconPixmap", Type: "a(iiay)", Access: "read"},
		{Name: "IconThemePath", Type: "s", Access: "read"},
		{Name: "OverlayIconName", Type: "s", Access: "read"},
		{Name: "OverlayIconPixmap", Type: "a(iiay)", Access: "read"},
		{Name: "AttentionIconName", Type: "s", Access: "read"},
		{Name: "AttentionIconPixmap", Type: "a(iiay)", Access: "read"},
		{Name: "AttentionMovieName", Type: "s", Access: "read"},
		{Name: "ItemIsMenu", Type: "b", Access: "read"},
		{Name: "Menu", Type: "o", Access: "read"},
		{Name: "ToolTip", Type: "(sa(iiay)ss)", Access: "read"},
	},
}

// menuInterface describes com.canonical.dbusmenu as we implement it.
var menuInterface = introspect.Interface{
	Name: ifaceMenu,
	Methods: []introspect.Method{
		{Name: "GetLayout", Args: args(
			"parentId", "i", "in",
			"recursionDepth", "i", "in",
			"propertyNames", "as", "in",
			"revision", "u", "out",
			"layout", "(ia{sv}av)", "out")},
		{Name: "GetGroupProperties", Args: args(
			"ids", "ai", "in",
			"propertyNames", "as", "in",
			"properties", "a(ia{sv})", "out")},
		{Name: "GetProperty", Args: args(
			"id", "i", "in",
			"name", "s", "in",
			"value", "v", "out")},
		{Name: "Event", Args: args(
			"id", "i", "in",
			"eventId", "s", "in",
			"data", "v", "in",
			"timestamp", "u", "in")},
		{Name: "EventGroup", Args: args(
			"events", "a(isvu)", "in",
			"idErrors", "ai", "out")},
		{Name: "AboutToShow", Args: args(
			"id", "i", "in",
			"needUpdate", "b", "out")},
		{Name: "AboutToShowGroup", Args: args(
			"ids", "ai", "in",
			"updatesNeeded", "ai", "out",
			"idErrors", "ai", "out")},
	},
	Signals: []introspect.Signal{
		{Name: "LayoutUpdated", Args: args("revision", "u", "out", "parent", "i", "out")},
		{Name: "ItemsPropertiesUpdated", Args: args(
			"updatedProps", "a(ia{sv})", "out",
			"removedProps", "a(ias)", "out")},
		{Name: "ItemActivationRequested", Args: args("id", "i", "out", "timestamp", "u", "out")},
	},
	Properties: []introspect.Property{
		{Name: "Version", Type: "u", Access: "read"},
		{Name: "TextDirection", Type: "s", Access: "read"},
		{Name: "Status", Type: "s", Access: "read"},
		{Name: "IconThemePath", Type: "as", Access: "read"},
	},
}

// propsInterface describes org.freedesktop.DBus.Properties, the stock
// interface both objects also serve.
var propsInterface = introspect.Interface{
	Name: ifaceProps,
	Methods: []introspect.Method{
		{Name: "Get", Args: args(
			"interface", "s", "in",
			"name", "s", "in",
			"value", "v", "out")},
		{Name: "Set", Args: args(
			"interface", "s", "in",
			"name", "s", "in",
			"value", "v", "in")},
		{Name: "GetAll", Args: args(
			"interface", "s", "in",
			"props", "a{sv}", "out")},
	},
	Signals: []introspect.Signal{
		{Name: "PropertiesChanged", Args: args(
			"interface", "s", "out",
			"changed_properties", "a{sv}", "out",
			"invalidated_properties", "as", "out")},
	},
}
