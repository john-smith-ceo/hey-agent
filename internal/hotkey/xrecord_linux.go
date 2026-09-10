//go:build linux

package hotkey

/*
#cgo LDFLAGS: -lX11 -lXtst
#include <stdlib.h>
#include <string.h>
#include <X11/Xlib.h>
#include <X11/extensions/record.h>

extern void goX11Event(int target, int down);

static Display *controlDisplay = NULL;
static Display *dataDisplay = NULL;
static XRecordContext recordContext = 0;
static int targetKeycode = 0;
static int cancelKeycode = 0;

// XRecord observes the event stream without consuming it, so unlike the macOS
// tap the key still reaches applications. That is acceptable for a key that
// does nothing on its own, and required for a typing modifier.
static void recordCallback(XPointer closure, XRecordInterceptData *data) {
	if (data->category == XRecordFromServer && data->data != NULL && data->data_len > 0) {
		const unsigned char *event = (const unsigned char *)data->data;
		int type = event[0] & 0x7F;
		int keycode = event[1];
		if (type == KeyPress || type == KeyRelease) {
			if (keycode == targetKeycode) {
				goX11Event(1, type == KeyPress);
			} else if (keycode == cancelKeycode && type == KeyPress) {
				goX11Event(2, 1);
			} else if (type == KeyPress) {
				goX11Event(0, 1);
			}
		}
	}
	XRecordFreeData(data);
}

static int lookupKeycode(unsigned long keysym) {
	Display *display = XOpenDisplay(NULL);
	if (display == NULL) return -1;
	int keycode = (int)XKeysymToKeycode(display, (KeySym)keysym);
	XCloseDisplay(display);
	return keycode;
}

// startRecord returns 0 on success and a small code identifying the step that
// failed otherwise, so the Go side can explain the failure precisely.
static int startRecord(int keycode, int escapeKeycode) {
	targetKeycode = keycode;
	cancelKeycode = escapeKeycode;
	controlDisplay = XOpenDisplay(NULL);
	dataDisplay = XOpenDisplay(NULL);
	if (controlDisplay == NULL || dataDisplay == NULL) return 1;
	int major = 0, minor = 0;
	if (!XRecordQueryVersion(controlDisplay, &major, &minor)) return 2;
	XRecordRange *range = XRecordAllocRange();
	if (range == NULL) return 3;
	memset(range, 0, sizeof(*range));
	range->device_events.first = KeyPress;
	range->device_events.last = KeyRelease;
	XRecordClientSpec clients = XRecordAllClients;
	recordContext = XRecordCreateContext(controlDisplay, 0, &clients, 1, &range, 1);
	XFree(range);
	if (recordContext == 0) return 4;
	XSync(controlDisplay, False);
	return 0;
}

static void runRecord(void) {
	XRecordEnableContext(dataDisplay, recordContext, recordCallback, NULL);
}

static void stopRecord(void) {
	if (controlDisplay != NULL && recordContext != 0) {
		XRecordDisableContext(controlDisplay, recordContext);
		XSync(controlDisplay, False);
	}
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
)

type x11Record struct {
	key           Key
	keycode       int
	cancelKeycode int
}

func GlobalSupported() bool { return os.Getenv("DISPLAY") != "" }

// New returns a listener for the configured key.
func New(key Key) (Listener, error) {
	if !GlobalSupported() {
		return nil, errors.New("no X11 display; hey-agent needs an X11 session (Wayland is not supported yet)")
	}
	keycode := int(C.lookupKeycode(C.ulong(key.x11Keysym)))
	if keycode <= 0 {
		return nil, fmt.Errorf("this keyboard layout has no %s key", key.Name)
	}
	escapeKeycode := int(C.lookupKeycode(C.ulong(0xff1b)))
	if escapeKeycode <= 0 {
		return nil, errors.New("this keyboard layout has no Escape key")
	}
	return &x11Record{key: key, keycode: keycode, cancelKeycode: escapeKeycode}, nil
}

var (
	eventsMu sync.Mutex
	events   chan raw
)

func (r *x11Record) Start(ctx context.Context) (<-chan Event, error) {
	switch code := int(C.startRecord(C.int(r.keycode), C.int(r.cancelKeycode))); code {
	case 0:
	case 1:
		return nil, errors.New("cannot open the X display")
	case 2:
		return nil, errors.New("the X server lacks the RECORD extension")
	default:
		return nil, fmt.Errorf("cannot install the X record context (step %d)", code)
	}
	ch := make(chan raw, 16)
	eventsMu.Lock()
	events = ch
	eventsMu.Unlock()
	go func() {
		<-ctx.Done()
		C.stopRecord()
		eventsMu.Lock()
		if events == ch {
			close(ch)
			events = nil
		}
		eventsMu.Unlock()
	}()
	go func() { C.runRecord() }()
	return gate(ctx, ch, r.key, SoloTimeout), nil
}

//export goX11Event
func goX11Event(target C.int, down C.int) {
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
