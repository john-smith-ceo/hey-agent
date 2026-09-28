// Package setupui gives the tray real settings dialogs. A StatusNotifierItem
// menu is a flat list — the protocol has no text fields — so the daemon
// shells out to zenity, the GTK dialog tool that ships with every GTK
// desktop, and writes the answers into the dotenv file via envfile.
//
// Everything here degrades honestly: no zenity or no DISPLAY means the action
// reports through notify-send (or the daemon log) instead of dying.
package setupui

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/john-smith-ceo/hey-agent/internal/envfile"
)

// dialogTimeout caps how long a forgotten dialog may linger; zenity sits in
// the user's face, not on a retry loop, so it is generous.
const dialogTimeout = 10 * time.Minute

// UI drives the settings dialogs. EnvPath is the dotenv file the daemon's
// supervisor loads — the dialog and the unit must agree on it or saved keys
// never reach the process.
type UI struct {
	EnvPath string
	// Run executes a dialog command and returns stdout. Tests substitute a
	// fake; nil means exec.Command.
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// Restart, when non-nil, is invoked after a successful save so the daemon
	// re-reads the env file (typically `systemctl --user restart`).
	Restart func() error
}

func (u *UI) run(ctx context.Context, name string, args ...string) (string, error) {
	if u.Run != nil {
		return u.Run(ctx, name, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, dialogTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			if msg := strings.TrimSpace(string(ee.Stderr)); msg != "" {
				return string(out), fmt.Errorf("%s: %s (%w)", name, msg, err)
			}
		}
		return string(out), err
	}
	return string(out), nil
}

// zenity reports "cancelled" by exiting 1 with empty stdout. Only an
// ExitError can be a cancel — a binary that never ran (not installed, no
// DISPLAY for exec to matter) is a real failure and must surface.
func cancelled(out string, err error) bool {
	var ee *exec.ExitError
	return err != nil && errors.As(err, &ee) && strings.TrimSpace(out) == ""
}

// SetAPIKey opens a masked entry for the provider key and stores it as
// HEY_AGENT_API_KEY. On success the daemon restarts so the key is live.
func (u *UI) SetAPIKey(ctx context.Context) {
	out, err := u.run(ctx, "zenity",
		"--entry", "--hide-text",
		"--title", "hey-agent — API key",
		"--text", "Ключ провайдера (OpenAI или совместимый endpoint):",
		"--width", "480")
	if cancelled(out, err) {
		return
	}
	if err != nil {
		u.Notify(ctx, "hey-agent", "Диалог недоступен: "+err.Error())
		return
	}
	key := strings.TrimSpace(out)
	if err := envfile.ValidateValue(key); err != nil || key == "" {
		u.Notify(ctx, "hey-agent", "Пустой или некорректный ключ — не сохранено.")
		return
	}
	if err := envfile.Update(u.EnvPath, map[string]string{"HEY_AGENT_API_KEY": key}); err != nil {
		u.Notify(ctx, "hey-agent", "Не удалось записать "+u.EnvPath+": "+err.Error())
		return
	}
	u.saved(ctx, "Ключ сохранён в "+u.EnvPath)
}

// VoiceSettings edits the OpenAI-compatible TTS fields: voice, model and
// base URL. Blank fields keep their current values. Piper stays a profile
// thing — it needs a model path, which a three-field dialog cannot express.
func (u *UI) VoiceSettings(ctx context.Context) {
	current, _ := envfile.Load(u.EnvPath)
	out, err := u.run(ctx, "zenity", "--forms",
		"--title", "hey-agent — голосовой вывод",
		"--text", "Пустое поле = оставить как есть.\nДля локального piper используйте профиль (~/.config/hey-agent/profiles/).",
		"--add-entry", "Голос (alloy, onyx, nova…)",
		"--add-entry", "Модель TTS",
		"--add-entry", "Base URL провайдера",
		"--separator", "\x01",
		"--width", "480")
	if cancelled(out, err) {
		return
	}
	if err != nil {
		u.Notify(ctx, "hey-agent", "Диалог недоступен: "+err.Error())
		return
	}
	fields := strings.Split(strings.TrimRight(out, "\n"), "\x01")
	updates := map[string]string{}
	if len(fields) > 0 && strings.TrimSpace(fields[0]) != "" {
		updates["HEY_AGENT_TTS_VOICE"] = strings.TrimSpace(fields[0])
	}
	if len(fields) > 1 && strings.TrimSpace(fields[1]) != "" {
		updates["HEY_AGENT_TTS_MODEL"] = strings.TrimSpace(fields[1])
	}
	if len(fields) > 2 && strings.TrimSpace(fields[2]) != "" {
		updates["HEY_AGENT_TTS_BASE_URL"] = strings.TrimSpace(fields[2])
	}
	for _, v := range updates {
		if err := envfile.ValidateValue(v); err != nil {
			u.Notify(ctx, "hey-agent", "Некорректное значение — не сохранено.")
			return
		}
	}
	if len(updates) == 0 {
		u.Notify(ctx, "hey-agent", "Ничего не изменено.")
		return
	}
	if err := envfile.Update(u.EnvPath, updates); err != nil {
		u.Notify(ctx, "hey-agent", "Не удалось записать "+u.EnvPath+": "+err.Error())
		return
	}
	_ = current // reserved for prefilled fields if zenity gains that need
	u.saved(ctx, "Настройки голоса сохранены в "+u.EnvPath)
}

// About shows a small info dialog with the lines the daemon gave us.
func (u *UI) About(ctx context.Context, text string) {
	if _, err := u.run(ctx, "zenity", "--info", "--title", "hey-agent",
		"--text", text, "--width", "420"); err != nil {
		u.Notify(ctx, "hey-agent", "Диалог недоступен: "+err.Error())
	}
}

// Notify prefers notify-send (a real desktop toast) and falls back to a
// zenity info box when the notification daemon is absent.
func (u *UI) Notify(ctx context.Context, title, text string) {
	if _, err := u.run(ctx, "notify-send", "-a", "hey-agent", title, text); err == nil {
		return
	}
	_, _ = u.run(ctx, "zenity", "--info", "--title", title, "--text", text, "--width", "380")
}

// saved reports the write, then asks the daemon to restart so the new env is
// live. When no Restart hook exists (daemon not under systemd) the user is
// told to restart by hand instead of pretending the change took effect.
func (u *UI) saved(ctx context.Context, msg string) {
	if u.Restart == nil {
		u.Notify(ctx, "hey-agent", msg+". Перезапустите демона, чтобы подхватить.")
		return
	}
	u.Notify(ctx, "hey-agent", msg+". Перезапускаю демона…")
	if err := u.Restart(); err != nil {
		u.Notify(ctx, "hey-agent", "Сохранено, но рестарт не вышел: "+err.Error())
	}
}
