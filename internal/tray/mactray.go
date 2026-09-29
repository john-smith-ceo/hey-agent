//go:build darwin

// MacTray is the macOS (NSStatusItem / menu bar) backend. It mirrors the
// Tray surface — New-like constructor, Run, Close, SetState, SetToggles —
// so cmd/hey-agent drives it through the same statusTray interface.
//
// Where the Linux Tray speaks StatusNotifierItem over session D-Bus, this
// one uses fyne.io/systray, which wraps Cocoa's NSStatusItem. Cocoa demands
// the main thread: Run must be called from the process's main goroutine —
// the daemon therefore runs on a spawned goroutine on darwin.
package tray

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"sync"

	"fyne.io/systray"
)

// MacTray renders the same menu and state icon in the macOS menu bar.
type MacTray struct {
	cfg Config

	mu      sync.Mutex
	state   State
	toggles Toggles

	itStatus *systray.MenuItem
	itBind   *systray.MenuItem
	itSubmit *systray.MenuItem
	itVoice  *systray.MenuItem

	running bool
	done    chan struct{}
	quit    sync.Once
}

// NewMac mirrors New: on macOS there is no equivalent of "no tray host"
// (NSStatusItem is always available in a GUI session), so error is always
// nil and the signature only keeps the call sites symmetric.
func NewMac(cfg Config) (*MacTray, error) {
	if cfg.Title == "" {
		cfg.Title = appTitle
	}
	return &MacTray{cfg: cfg, state: StateIdle, done: make(chan struct{})}, nil
}

// Run occupies the main thread with the Cocoa run loop and returns when the
// context ends or Quit/Close fires.
func (t *MacTray) Run(ctx context.Context) error {
	systray.Run(func() {
		t.buildMenu()
		t.pushIcon()
		go func() {
			<-ctx.Done()
			t.Close()
		}()
	}, func() {
		close(t.done)
	})
	<-t.done
	return nil
}

// Close tears the icon down; safe to call twice.
func (t *MacTray) Close() error {
	t.quit.Do(systray.Quit)
	return nil
}

// SetState mirrors Tray.SetState: swaps the icon and the status menu row.
func (t *MacTray) SetState(s State) {
	if !s.known() {
		s = StateIdle
	}
	t.mu.Lock()
	t.state = s
	t.mu.Unlock()
	t.pushIcon()
	if t.itStatus != nil {
		t.itStatus.SetTitle(t.cfg.Title + " — " + string(s))
	}
}

// SetToggles mirrors Tray.SetToggles: bind label plus the two checkmarks.
func (t *MacTray) SetToggles(tg Toggles) {
	t.mu.Lock()
	t.toggles = tg
	t.mu.Unlock()
	if t.itBind != nil {
		label := "Bind hotkey"
		if tg.Bound {
			label = "Unbind hotkey"
		}
		t.itBind.SetTitle(label)
	}
	if t.itSubmit != nil {
		if tg.Submit {
			t.itSubmit.Check()
		} else {
			t.itSubmit.Uncheck()
		}
	}
	if t.itVoice != nil {
		if tg.Voice {
			t.itVoice.Check()
		} else {
			t.itVoice.Uncheck()
		}
	}
}

func (t *MacTray) buildMenu() {
	a := t.cfg.Actions
	call := func(f func()) func() {
		if f == nil {
			return func() {}
		}
		return f
	}
	fire := func(it *systray.MenuItem, f func()) {
		go func() {
			for range it.ClickedCh {
				call(f)()
			}
		}()
	}

	systray.SetTooltip(t.cfg.Title)
	t.itStatus = systray.AddMenuItem(t.cfg.Title+" — "+string(t.state), "current state")
	t.itStatus.Disable()
	t.itBind = systray.AddMenuItem("Bind hotkey", "grab/release the recording hotkey")
	t.itSubmit = systray.AddMenuItemCheckbox("Submit automatically", "press Enter after transcription", t.toggles.Submit)
	t.itVoice = systray.AddMenuItemCheckbox("Voice output", "speak replies", t.toggles.Voice)
	systray.AddSeparator()
	itKey := systray.AddMenuItem("Set API key…", "provider key dialog")
	itVoiceSet := systray.AddMenuItem("Voice settings…", "voice and speed dialog")
	itAbout := systray.AddMenuItem("About hey-agent…", "")
	systray.AddSeparator()
	itHush := systray.AddMenuItem("Hush", "stop speaking now")
	itQuit := systray.AddMenuItem("Quit hey-agent", "stop the daemon")

	fire(t.itBind, a.ToggleBind)
	fire(t.itSubmit, a.ToggleSubmit)
	fire(t.itVoice, a.ToggleVoice)
	fire(itKey, a.SetAPIKey)
	fire(itVoiceSet, a.VoiceSettings)
	fire(itAbout, a.About)
	fire(itHush, a.Hush)
	fire(itQuit, a.Quit)
}

// pushIcon encodes the current state's dot as PNG and hands it to the
// status item. We use the regular (colored) slot, not the template one:
// the state color is the information, a monochrome mask would lose it.
func (t *MacTray) pushIcon() {
	systray.SetIcon(pngForState(t.state))
}

// pngForState renders IconForState's best 24px-ish pixmap into PNG bytes.
// The pixmap wire format is A,R,G,B per pixel; image.NRGBA wants R,G,B,A.
func pngForState(s State) []byte {
	ic := IconForState(s)
	var pm Pixmap
	for _, p := range ic.Pixmaps {
		if p.Width <= 24 {
			pm = p
		}
	}
	if pm.Width == 0 && len(ic.Pixmaps) > 0 {
		pm = ic.Pixmaps[0]
	}
	if pm.Width == 0 {
		return nil
	}
	img := image.NewNRGBA(image.Rect(0, 0, pm.Width, pm.Height))
	for i := 0; i < pm.Width*pm.Height; i++ {
		img.Pix[i*4+0] = pm.ARGB[i*4+1]
		img.Pix[i*4+1] = pm.ARGB[i*4+2]
		img.Pix[i*4+2] = pm.ARGB[i*4+3]
		img.Pix[i*4+3] = pm.ARGB[i*4+0]
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}
