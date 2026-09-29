//go:build darwin

// macOS tray wiring: same menu contract as the Linux zenity dialogs, but
// the settings windows are osascript dialogs — macOS has no zenity.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/john-smith-ceo/hey-agent/internal/daemon"
	"github.com/john-smith-ceo/hey-agent/internal/envfile"
	"github.com/john-smith-ceo/hey-agent/internal/tray"
)

func init() {
	trayFactory = newDaemonTrayMac
}

// newDaemonTrayMac builds the NSStatusItem tray. Callbacks go through the
// daemon's own command handlers exactly like the Linux path does.
func newDaemonTrayMac(d *daemon.Daemon, envPath, socketPath string) (statusTray, error) {
	ui := macUI{envPath: envPath}
	return tray.NewMac(tray.Config{
		Actions: tray.Actions{
			ToggleBind: func() {
				cmd := "bind"
				if d.Bound() {
					cmd = "unbind"
				}
				d.Handle(daemon.Request{Cmd: cmd})
			},
			ToggleSubmit:  func() { toggleConfig(d, "submit") },
			ToggleVoice:   func() { toggleConfig(d, "voice_enabled") },
			SetAPIKey:     func() { ui.setAPIKey() },
			VoiceSettings: func() { ui.voiceSettings() },
			About:         func() { ui.about(aboutText(d, envPath, socketPath)) },
			Hush:          func() { d.Handle(daemon.Request{Cmd: "hush"}) },
			Quit:          func() { d.Handle(daemon.Request{Cmd: "stop"}) },
		},
		Log: os.Stderr,
	})
}

// macUI is the osascript counterpart of setupui.UI: three dialogs that end
// with envfile.Update plus a launchd kick so the daemon reloads the file.
type macUI struct {
	envPath string
}

func (u macUI) setAPIKey() {
	out, err := osa(`display dialog "OpenAI-compatible API key:" default answer "" with hidden answer with title "hey-agent" buttons {"Cancel","Save"} default button "Save"`)
	if err != nil {
		return // cancelled
	}
	key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "text returned:"))
	if err := envfile.ValidateValue(key); err != nil || key == "" {
		u.notify("hey-agent", "Ключ пустой или содержит недопустимые символы")
		return
	}
	u.save(map[string]string{"HEY_AGENT_API_KEY": key}, "Ключ сохранён в "+u.envPath)
}

var ttsVoices = []string{
	"alloy", "ash", "ballad", "cedar", "coral", "echo", "fable",
	"marin", "nova", "onyx", "sage", "shimmer", "verse",
}

func (u macUI) voiceSettings() {
	cur, _ := envfile.Load(u.envPath)
	voice := cur["HEY_AGENT_TTS_VOICE"]
	out, err := osa(fmt.Sprintf(
		`choose from list {%s} with title "hey-agent" with prompt "Голос (текущий: %s):"`,
		quotedList(ttsVoices), orDefault(voice, "default")))
	if err != nil || strings.TrimSpace(out) == "false" {
		return
	}
	picked := strings.TrimSpace(out)
	if err := envfile.ValidateValue(picked); err != nil {
		u.notify("hey-agent", "Недопустимое значение голоса")
		return
	}
	u.save(map[string]string{"HEY_AGENT_TTS_VOICE": picked},
		"Голос сохранён в "+u.envPath)
}

func (u macUI) about(text string) {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
	_, _ = osa(fmt.Sprintf(`display dialog "%s" with title "About hey-agent" buttons {"OK"} default button "OK"`, esc))
}

func (u macUI) notify(title, text string) {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
	_, _ = osa(fmt.Sprintf(`display notification "%s" with title "%s"`, esc, title))
}

// save writes the env file and restarts the launchd job — the macOS twin
// of setupui's `systemctl --user restart`.
func (u macUI) save(kv map[string]string, doneMsg string) {
	if err := envfile.Update(u.envPath, kv); err != nil {
		u.notify("hey-agent", "Не удалось записать "+u.envPath+": "+err.Error())
		return
	}
	_ = exec.Command("launchctl", "kickstart", "-k",
		fmt.Sprintf("gui/%d/com.hey-agent.daemon", os.Getuid())).Run()
	u.notify("hey-agent", doneMsg)
}

func osa(script string) (string, error) {
	out, err := exec.CommandContext(context.Background(), "osascript", "-e", script).Output()
	return string(out), err
}

func quotedList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = `"` + s + `"`
	}
	return strings.Join(q, ", ")
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
