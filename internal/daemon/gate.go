package daemon

import (
	"context"
	"sync"

	"github.com/john-smith-ceo/hey-agent/internal/hotkey"
)

// hotkeyGate sits between the bridge and the platform hotkey listener so the
// daemon can release and re-grab the key (`unbind`/`bind`) without rebuilding
// the pipeline. The bridge opens it once and keeps a single channel for its
// whole run; the real listener is created, started and stopped underneath.
//
// Why a wrapper instead of toggling the bridge: unbind has to release the
// actual grab (on macOS a captured key is swallowed), and closing the bridge's
// channel would kill the whole run — the gate forwards events only while a
// real listener is live and keeps the bridge's channel open across rebinds.
type hotkeyGate struct {
	factory func() (hotkey.Listener, error)

	started   chan struct{} // closes when the bridge calls Start
	startOnce sync.Once

	mu          sync.Mutex
	outer       context.Context // the bridge's run context, captured in Start
	out         chan hotkey.Event
	wantBound   bool // the daemon wants the grab held
	binding     bool // a Bind is in flight — serializes concurrent calls
	bound       bool // a real listener is running right now
	cancelInner context.CancelFunc
}

func newHotkeyGate(factory func() (hotkey.Listener, error)) *hotkeyGate {
	return &hotkeyGate{factory: factory, started: make(chan struct{})}
}

// Started closes when the bridge opens the gate, so the daemon knows when the
// first Bind is meaningful instead of just recording intent.
func (g *hotkeyGate) Started() <-chan struct{} { return g.started }

// Start implements hotkey.Listener for the bridge.
func (g *hotkeyGate) Start(ctx context.Context) (<-chan hotkey.Event, error) {
	g.mu.Lock()
	g.outer = ctx
	g.out = make(chan hotkey.Event, 8)
	want := g.wantBound
	g.mu.Unlock()
	g.startOnce.Do(func() { close(g.started) })
	if want {
		// A `hey-agent bind` beat the bridge here: finish the grab now. A
		// failure is real at this point, so it fails the bridge run.
		if err := g.Bind(); err != nil {
			return nil, err
		}
	}
	return g.out, nil
}

// Bind grabs the hotkey. It is idempotent: while bound (or while another Bind
// is in flight) it returns nil immediately.
func (g *hotkeyGate) Bind() error {
	g.mu.Lock()
	if g.bound || g.binding {
		g.mu.Unlock()
		return nil
	}
	g.binding = true
	g.wantBound = true
	outer, out := g.outer, g.out
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		g.binding = false
		g.mu.Unlock()
	}()

	if outer == nil {
		// The bridge has not opened the gate yet; record the intent and let
		// Start finish the grab.
		return nil
	}
	listener, err := g.factory()
	if err != nil {
		g.unbindLocked()
		return err
	}
	// The inner context dies with the bridge's run context or with Unbind,
	// whichever comes first.
	ctx, cancel := context.WithCancel(outer)
	events, err := listener.Start(ctx)
	if err != nil {
		cancel()
		g.unbindLocked()
		return err
	}
	g.mu.Lock()
	if !g.wantBound {
		// An Unbind slipped in while the grab was being set up — drop the
		// just-started listener instead of leaving it running.
		g.mu.Unlock()
		cancel()
		return nil
	}
	g.bound, g.cancelInner = true, cancel
	g.mu.Unlock()
	go g.pump(ctx, events, out)
	return nil
}

// Unbind releases the hotkey grab. Safe to call repeatedly.
func (g *hotkeyGate) Unbind() {
	g.mu.Lock()
	cancel := g.cancelInner
	g.cancelInner = nil
	g.wantBound, g.bound = false, false
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Bound reports whether the grab is actually held — what `status` shows.
func (g *hotkeyGate) Bound() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.bound
}

func (g *hotkeyGate) unbindLocked() {
	g.mu.Lock()
	g.wantBound, g.bound = false, false
	g.mu.Unlock()
}

// pump forwards events from the real listener to the bridge's channel for the
// life of one inner listener context.
func (g *hotkeyGate) pump(ctx context.Context, in <-chan hotkey.Event, out chan hotkey.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-in:
			if !ok {
				if ctx.Err() != nil {
					// Our own cancel — unbind or daemon shutdown. The listener
					// channel always closes after its context dies, so a
					// cancelled context means this was a planned stop.
					return
				}
				// The platform listener ended without an unbind — that means a
				// dead input, not a released one. Closing the bridge's channel
				// fails the run loudly instead of leaving a deaf daemon.
				g.mu.Lock()
				if g.bound {
					g.bound, g.wantBound = false, false
					close(out)
				}
				g.mu.Unlock()
				return
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}
}
