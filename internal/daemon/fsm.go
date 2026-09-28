package daemon

import (
	"fmt"
	"sync"
	"time"
)

// State is the daemon's audio state machine (SPEC §4). One machine holds both
// lanes — input (recording → transcribing → delivering) and output (speaking)
// — so every actor sees a single truth about who owns the microphone.
type State int

const (
	Idle State = iota
	Recording
	Transcribing
	Delivering
	Speaking
	Error
)

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case Recording:
		return "recording"
	case Transcribing:
		return "transcribing"
	case Delivering:
		return "delivering"
	case Speaking:
		return "speaking"
	case Error:
		return "error"
	}
	return "unknown"
}

// allowed encodes the transitions of SPEC §4. Recording ⊥ Speaking is
// enforced here: Recording has no edge to Speaking (a speak during recording
// is queued instead), and Speaking → Recording exists because a hotkey press
// while speaking hushes the phrase and starts recording.
var allowed = map[State]map[State]bool{
	Idle:         {Recording: true, Speaking: true, Error: true},
	Recording:    {Transcribing: true, Idle: true, Error: true},
	Transcribing: {Delivering: true, Idle: true, Error: true},
	Delivering:   {Idle: true, Error: true},
	Speaking:     {Idle: true, Recording: true, Error: true},
	// Error is sticky for `status` only: the next hotkey press must work from
	// Error exactly as from Idle, so Recording and Speaking leave it directly.
	Error: {Idle: true, Recording: true, Speaking: true},
}

// defaultErrorTTL is how long an error stays visible in `status` before the
// machine returns to Idle on its own — same cadence the bridge uses for its
// status line.
const defaultErrorTTL = 4 * time.Second

// FSM is a mutex-guarded state machine with subscribers. It never blocks its
// callers: a slow subscriber loses intermediate states rather than stalling
// the pipeline, and always sees the latest state on State().
type FSM struct {
	mu       sync.Mutex
	state    State
	message  string // error detail, only meaningful while state == Error
	errorTTL time.Duration
	timer    *time.Timer
	subs     map[chan State]struct{}
}

func newFSM() *FSM {
	return &FSM{state: Idle, errorTTL: defaultErrorTTL, subs: map[chan State]struct{}{}}
}

func (f *FSM) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// Message returns the detail of the current Error state.
func (f *FSM) Message() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.message
}

// Transition moves to `to` when the transition table allows it. A move to the
// current state is a no-op, so repeated reports of the same bridge state stay
// harmless.
func (f *FSM) Transition(to State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == to {
		return nil
	}
	if !allowed[f.state][to] {
		return fmt.Errorf("invalid transition %s → %s", f.state, to)
	}
	f.setLocked(to, "")
	return nil
}

// TransitionStrict is Transition that also rejects landing on the current
// state. The speak starter uses it: "already Speaking" must queue the phrase,
// not silently run a second one in parallel.
func (f *FSM) TransitionStrict(to State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == to {
		return fmt.Errorf("already in state %s", to)
	}
	if !allowed[f.state][to] {
		return fmt.Errorf("invalid transition %s → %s", f.state, to)
	}
	f.setLocked(to, "")
	return nil
}

// TransitionFrom is a compare-and-swap: it moves to `to` only while the
// machine is still in `from`. Actors that must not stomp a state they did not
// create — a finishing speak worker, the error timer — go through this.
func (f *FSM) TransitionFrom(from, to State) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != from || !allowed[from][to] {
		return false
	}
	f.setLocked(to, "")
	return true
}

// SetError moves to Error and keeps the cause for `status`.
func (f *FSM) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == Error || allowed[f.state][Error] {
		f.setLocked(Error, err.Error())
	}
}

// SetErrorFrom is SetError that no-ops unless the machine is still in `from`.
func (f *FSM) SetErrorFrom(from State, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state == from && allowed[from][Error] {
		f.setLocked(Error, err.Error())
	}
}

// Subscribe returns a channel that receives every state the machine enters,
// starting with the current one. Delivers are non-blocking — a slow reader may
// miss intermediate states but never blocks the pipeline.
func (f *FSM) Subscribe() (<-chan State, func()) {
	ch := make(chan State, 16)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	current := f.state
	f.mu.Unlock()
	ch <- current
	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			f.mu.Lock()
			delete(f.subs, ch)
			f.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

func (f *FSM) setLocked(to State, message string) {
	f.state, f.message = to, message
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
	}
	if to == Error {
		// Auto-return to Idle so a transient failure does not pin the state
		// machine — and so a queued phrase does not wait forever behind an
		// error the user already saw.
		f.timer = time.AfterFunc(f.errorTTL, func() { f.TransitionFrom(Error, Idle) })
	}
	for ch := range f.subs {
		select {
		case ch <- to:
		default:
		}
	}
}
