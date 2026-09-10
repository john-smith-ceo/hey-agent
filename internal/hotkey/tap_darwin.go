//go:build darwin

package hotkey

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdbool.h>
#include <stdint.h>

extern void goKeyEvent(int target, int down);
static CFMachPortRef globalKeyTap = NULL;
static int64_t targetKeycode = 61;
static uint64_t targetMask = kCGEventFlagMaskAlternate;
static int swallowTarget = 1;
static const int64_t cancelKeycode = 53; // Escape

static void configureKeyTap(int64_t keycode, uint64_t mask, int swallow) {
	targetKeycode = keycode;
	targetMask = mask;
	swallowTarget = swallow;
}

static CGEventRef keyTap(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *refcon) {
	if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
		if (globalKeyTap != NULL) CGEventTapEnable(globalKeyTap, true);
		return event;
	}
	// An ordinary key going down never belongs to the hotkey, but it does tell
	// the solo detector that a held modifier was doing its normal job. Escape
	// is the one exception: it reports a cancel while staying an ordinary key
	// for the application under it.
	if (type == kCGEventKeyDown) {
		if (CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode) == cancelKeycode) {
			goKeyEvent(2, 1);
		} else {
			goKeyEvent(0, 1);
		}
		return event;
	}
	if (type != kCGEventFlagsChanged) {
		return event;
	}
	if (CGEventGetIntegerValueField(event, kCGKeyboardEventKeycode) != targetKeycode) {
		goKeyEvent(0, 1);
		return event;
	}
	CGEventFlags flags = CGEventGetFlags(event);
	goKeyEvent(1, (flags & targetMask) != 0);
	// Swallowing keeps a free key from reaching applications, the way the
	// original Right Option tap did. A typing modifier must pass through or
	// capitals would stop working.
	return swallowTarget ? NULL : event;
}

static CFMachPortRef createKeyTap(void) {
	CGEventMask mask = CGEventMaskBit(kCGEventFlagsChanged) | CGEventMaskBit(kCGEventKeyDown);
	CFMachPortRef tap = CGEventTapCreate(kCGSessionEventTap, kCGHeadInsertEventTap, kCGEventTapOptionDefault,
		mask, keyTap, NULL);
	if (tap == NULL) return NULL;
	globalKeyTap = tap;
	return tap;
}

static int keyTapIsNull(CFMachPortRef tap) { return tap == NULL; }

static void runKeyTap(CFMachPortRef tap) {
	CFRunLoopSourceRef source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0);
	CFRunLoopAddSource(CFRunLoopGetCurrent(), source, kCFRunLoopCommonModes);
	CGEventTapEnable(tap, true);
	CFRunLoopRun();
	CFRelease(source);
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type darwinTap struct{ key Key }

func GlobalSupported() bool { return true }

// New returns a listener for the configured key.
func New(key Key) (Listener, error) {
	if !key.AvailableOnDarwin() {
		return nil, fmt.Errorf("no Apple keyboard has a %s key; run: hey-agent keys", key.Name)
	}
	return darwinTap{key: key}, nil
}

var (
	eventsMu sync.Mutex
	events   chan raw
)

func (t darwinTap) Start(ctx context.Context) (<-chan Event, error) {
	swallow := C.int(0)
	if t.key.Category == Free {
		swallow = 1
	}
	C.configureKeyTap(C.int64_t(t.key.darwinCode), C.uint64_t(t.key.darwinMask), swallow)
	tap := C.createKeyTap()
	if C.keyTapIsNull(tap) != 0 {
		return nil, errors.New("cannot install the event tap; grant Accessibility permission and retry")
	}
	ch := make(chan raw, 16)
	eventsMu.Lock()
	events = ch
	eventsMu.Unlock()
	go func() {
		<-ctx.Done()
		eventsMu.Lock()
		if events == ch {
			close(ch)
			events = nil
		}
		eventsMu.Unlock()
		// Stopping the CFRunLoop from another thread needs a runloop reference; process exit on Ctrl+C is safe.
	}()
	go func() { C.runKeyTap(tap) }()
	return gate(ctx, ch, t.key, SoloTimeout), nil
}

//export goKeyEvent
func goKeyEvent(target C.int, down C.int) {
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if events == nil {
		return
	}
	select {
	case events <- raw{target: target == 1, down: down != 0, cancel: target == 2}:
	default:
	}
}
