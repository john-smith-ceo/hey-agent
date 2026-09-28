package tray

import (
	"github.com/godbus/dbus/v5"
)

// bus is the small slice of D-Bus the tray actually needs. *dbus.Conn cannot
// be faked (it is a concrete type), so the tray talks to this interface
// instead: production code gets sessionBus below, tests get a recording fake.
// Every method is a thin rename of the Conn/BusObject call it wraps.
type bus interface {
	// call invokes a method and discards the reply body.
	call(dest string, path dbus.ObjectPath, method string, args ...any) error
	// callStore invokes a method and stores the reply body into "into".
	callStore(dest string, path dbus.ObjectPath, method string, into any, args ...any) error
	// export publishes v's exported methods under iface at path.
	export(v any, path dbus.ObjectPath, iface string) error
	// emit sends a signal from path; name is the full "iface.Member" string.
	emit(path dbus.ObjectPath, name string, values ...any) error
	// requestName claims a well-known bus name; reply is ignored, we only
	// care that the request reached the bus.
	requestName(name string) error
	// addMatch subscribes to signals matching the raw match rule, e.g.
	// "type='signal',member='NameOwnerChanged'".
	addMatch(match string) error
	// signal registers ch to receive matched signals.
	signal(ch chan<- *dbus.Signal)
	// removeSignal unregisters ch.
	removeSignal(ch chan<- *dbus.Signal)
	// close drops the connection.
	close() error
}

// sessionBus adapts a private session-bus connection to the bus interface.
// A private connection (ConnectSessionBus, not the shared SessionBus pool)
// means Close only ever tears down the tray's own socket.
type sessionBus struct {
	conn *dbus.Conn
}

func sessionBusDial() (bus, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, err
	}
	return &sessionBus{conn: conn}, nil
}

func (s *sessionBus) call(dest string, path dbus.ObjectPath, method string, args ...any) error {
	return s.conn.Object(dest, path).Call(method, 0, args...).Err
}

func (s *sessionBus) callStore(dest string, path dbus.ObjectPath, method string, into any, args ...any) error {
	return s.conn.Object(dest, path).Call(method, 0, args...).Store(into)
}

func (s *sessionBus) export(v any, path dbus.ObjectPath, iface string) error {
	return s.conn.Export(v, path, iface)
}

func (s *sessionBus) emit(path dbus.ObjectPath, name string, values ...any) error {
	return s.conn.Emit(path, name, values...)
}

func (s *sessionBus) requestName(name string) error {
	// DoNotQueue: if the name is somehow taken, fail fast rather than sit in
	// the queue invisible while a stale twin owns the tray slot.
	_, err := s.conn.RequestName(name, dbus.NameFlagDoNotQueue)
	return err
}

func (s *sessionBus) addMatch(match string) error {
	return s.call(busDaemonName, busDaemonPath, busDaemonName+".AddMatch", match)
}

func (s *sessionBus) signal(ch chan<- *dbus.Signal) {
	s.conn.Signal(ch)
}

func (s *sessionBus) removeSignal(ch chan<- *dbus.Signal) {
	s.conn.RemoveSignal(ch)
}

func (s *sessionBus) close() error {
	return s.conn.Close()
}
