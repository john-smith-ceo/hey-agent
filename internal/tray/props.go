package tray

import (
	"fmt"
	"sync"

	"github.com/godbus/dbus/v5"
)

// propStore is a minimal org.freedesktop.DBus.Properties implementation:
// a guarded two-level map of interface → property name → value, served over
// Get/GetAll/Set. We hand-roll it instead of using godbus' prop package for
// one reason: prop needs a real *dbus.Conn, which would defeat the testable
// bus seam — this store needs nothing but the mutex.
//
// All properties are read-only from the outside. Tray hosts never write:
// state flows daemon → host. NewStatus/NewIcon/ItemsPropertiesUpdated are
// the signals hosts actually watch, so updates emitted from Set are not
// needed at all.
type propStore struct {
	mu    sync.RWMutex
	props map[string]map[string]dbus.Variant
}

func newPropStore(init map[string]map[string]dbus.Variant) *propStore {
	if init == nil {
		init = map[string]map[string]dbus.Variant{}
	}
	return &propStore{props: init}
}

// Get implements org.freedesktop.DBus.Properties.Get.
func (s *propStore) Get(iface, name string) (dbus.Variant, *dbus.Error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.props[iface][name]; ok {
		return v, nil
	}
	return dbus.Variant{}, dbus.MakeFailedError(fmt.Errorf("tray: unknown property %s.%s", iface, name))
}

// GetAll implements org.freedesktop.DBus.Properties.GetAll. An unknown
// interface yields an empty map rather than an error — that is what glib's
// implementation does, and some hosts depend on it.
func (s *propStore) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]dbus.Variant, len(s.props[iface]))
	for k, v := range s.props[iface] {
		out[k] = v
	}
	return out, nil
}

// Set implements org.freedesktop.DBus.Properties.Set — and refuses. Every
// property the tray exposes is read-only for peers.
func (s *propStore) Set(iface, name string, v dbus.Variant) *dbus.Error {
	return dbus.MakeFailedError(fmt.Errorf("tray: %s.%s is read-only", iface, name))
}

// update is the internal write path used by SetState/SetToggles. Callers
// must already hold Tray.mu so a revision bump and its property updates
// stay one atomic step.
func (s *propStore) update(iface, name string, v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.props[iface] == nil {
		s.props[iface] = map[string]dbus.Variant{}
	}
	s.props[iface][name] = dbus.MakeVariant(v)
}
