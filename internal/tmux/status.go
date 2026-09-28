package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// While hey-agent borrows the status line, the session's own state is parked in
// two user options. They live in tmux rather than in memory so that the value
// survives the listener being killed and can still be restored afterwards.
//
// Two options rather than one, because an empty status-right and an unset one
// look identical but behave differently: an unset option inherits the global
// value, an empty one overrides it with nothing. Restoring the wrong one would
// silently wipe the clock and the pane title.
const (
	wasOption  = "@hey-agent-status-was"
	baseOption = "@hey-agent-status-base"
	lenWas     = "@hey-agent-length-was"
	lenBase    = "@hey-agent-length-base"
)

// borrowedLength is generous on purpose. tmux truncates status-right at
// status-right-length, which defaults to 40 characters — short enough to cut
// off an appended indicator entirely, which is exactly what happened.
const borrowedLength = "200"

// Status owns the status line of one tmux session. In a session hey-agent
// created it takes the whole line. In a session that already belonged to the
// user it only appends its indicator to the existing status-right and puts the
// original back on the way out: overwriting somebody's own status line is the
// same mistake as launching them a second Codex.
type Status struct {
	session  string
	app      string
	mode     string
	attached bool
	// socket names an alternate tmux server (`tmux -L name`). Empty talks to
	// the default server — the daemon's status pump passes the stand socket
	// here so an E2E run never touches the user's real tmux.
	socket string
}

func NewStatus(session, app, mode string, attached bool) (*Status, error) {
	return NewStatusSocket(session, app, mode, attached, "")
}

// NewStatusSocket is NewStatus pointed at a named tmux server.
func NewStatusSocket(session, app, mode string, attached bool, socket string) (*Status, error) {
	if strings.TrimSpace(session) == "" {
		return nil, fmt.Errorf("tmux session is empty")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux not found")
	}
	if strings.TrimSpace(app) == "" {
		app = "pane"
	}
	return &Status{session: session, app: cleanStatusText(app), mode: mode, attached: attached, socket: socket}, nil
}

func (s *Status) Configure(ctx context.Context) error {
	if s.attached {
		// Save once: a second listener must not overwrite the saved value with
		// a line that already contains an indicator.
		was, err := s.get(ctx, wasOption)
		if err != nil {
			return err
		}
		if was == "" {
			set, err := sessionOptionIsSet(ctx, s.socket, s.session, "status-right")
			if err != nil {
				return err
			}
			// The base is what the user actually sees, which is the session
			// value when there is one and the global value otherwise.
			base, err := effective(ctx, s.socket, s.session, "status-right", set)
			if err != nil {
				return err
			}
			state := "unset"
			if set {
				state = "set"
			}
			if err := s.set(ctx, wasOption, state); err != nil {
				return err
			}
			if err := s.set(ctx, baseOption, base); err != nil {
				return err
			}
			lengthSet, err := sessionOptionIsSet(ctx, s.socket, s.session, "status-right-length")
			if err != nil {
				return err
			}
			length, err := effective(ctx, s.socket, s.session, "status-right-length", lengthSet)
			if err != nil {
				return err
			}
			lengthState := "unset"
			if lengthSet {
				lengthState = "set"
			}
			if err := s.set(ctx, lenWas, lengthState); err != nil {
				return err
			}
			if err := s.set(ctx, lenBase, length); err != nil {
				return err
			}
			if err := s.set(ctx, "status-right-length", borrowedLength); err != nil {
				return err
			}
		}
		// Configure is called by the daemon itself at startup, so both paths
		// are honestly idle — "off" is reserved for "the socket did not
		// answer".
		return s.Set(ctx, Statuses{In: "idle", Out: "idle"})
	}
	options := [][2]string{
		{"status", "on"},
		{"status-style", "bg=#BCD7EF,fg=#17324D"},
		{"status-left-length", "500"},
		{"status-right", ""},
		{"window-status-format", ""},
		{"window-status-current-format", ""},
		{"status-justify", "left"},
	}
	for _, option := range options {
		if err := s.set(ctx, option[0], option[1]); err != nil {
			return err
		}
	}
	return s.Set(ctx, Statuses{In: "idle", Out: "idle"})
}

// Set renders the two readiness segments instead of tmux's default window
// list, which would expose the internal voice listener window.
func (s *Status) Set(ctx context.Context, statuses Statuses) error {
	if s.attached {
		base, err := s.get(ctx, baseOption)
		if err != nil {
			return err
		}
		message := "#[default]" + Indicator(statuses) + "hey-agent " + s.label()
		if strings.TrimSpace(base) != "" {
			message += " #[default]| " + base
		}
		return s.set(ctx, "status-right", message)
	}
	message := "<speech-to-text " + indicatorWith(statuses, "#[fg=#315B82]") + s.label() + ">"
	left := "#[fg=#17324D,bold]hey-agent#[fg=#17324D]: " + s.app
	left += " #[fg=#315B82]" + message
	return s.set(ctx, "status-left", left)
}

// Restore puts the borrowed status line back. It is safe to call more than
// once and on a session that was never touched.
func (s *Status) Restore(ctx context.Context) error {
	if !s.attached {
		return nil
	}
	_, err := RestoreSocket(ctx, s.socket, s.session)
	return err
}

// Restore is also reachable without a Status, so that `hey-agent stop` can put
// the line back after killing a listener that had no chance to clean up. It
// reports whether there was anything to restore, which lets stop tell "cleaned
// up after a dead listener" from "nothing was running here".
func Restore(ctx context.Context, session string) (bool, error) {
	return RestoreSocket(ctx, "", session)
}

// RestoreSocket is Restore pointed at a named tmux server (`tmux -L socket`).
func RestoreSocket(ctx context.Context, socket, session string) (bool, error) {
	was, err := get(ctx, socket, session, wasOption)
	if err != nil || was == "" {
		return false, err
	}
	if was == "set" {
		base, err := get(ctx, socket, session, baseOption)
		if err != nil {
			return false, err
		}
		if err := setOption(ctx, socket, session, "status-right", base); err != nil {
			return false, err
		}
	} else if err := unsetOption(ctx, socket, session, "status-right"); err != nil {
		return false, err
	}
	if lengthWas, err := get(ctx, socket, session, lenWas); err == nil && lengthWas != "" {
		if lengthWas == "set" {
			length, err := get(ctx, socket, session, lenBase)
			if err != nil {
				return false, err
			}
			if err := setOption(ctx, socket, session, "status-right-length", length); err != nil {
				return false, err
			}
		} else if err := unsetOption(ctx, socket, session, "status-right-length"); err != nil {
			return false, err
		}
		if err := unsetOption(ctx, socket, session, lenWas); err != nil {
			return false, err
		}
		if err := unsetOption(ctx, socket, session, lenBase); err != nil {
			return false, err
		}
	}
	if err := unsetOption(ctx, socket, session, wasOption); err != nil {
		return false, err
	}
	return true, unsetOption(ctx, socket, session, baseOption)
}

// tmuxArgv prepends `-L socket` when the call targets a named server.
func tmuxArgv(socket string, args ...string) []string {
	if socket != "" {
		return append([]string{"-L", socket}, args...)
	}
	return args
}

// sessionOptionIsSet reports whether the option is set on the session itself
// rather than inherited from the global value.
func sessionOptionIsSet(ctx context.Context, socket, session, option string) (bool, error) {
	output, err := exec.CommandContext(ctx, "tmux", tmuxArgv(socket, "show-options", "-t", sessionTarget(session))...).Output()
	if err != nil {
		return false, fmt.Errorf("read tmux options: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, option+" ") || line == option {
			return true, nil
		}
	}
	return false, nil
}

func effective(ctx context.Context, socket, session, option string, sessionSet bool) (string, error) {
	if sessionSet {
		return get(ctx, socket, session, option)
	}
	output, err := exec.CommandContext(ctx, "tmux", tmuxArgv(socket, "show-option", "-gqv", option)...).Output()
	if err != nil {
		return "", fmt.Errorf("read global tmux %s: %w", option, err)
	}
	return strings.TrimRight(string(output), "\n"), nil
}

func unsetOption(ctx context.Context, socket, session, option string) error {
	output, err := exec.CommandContext(ctx, "tmux", tmuxArgv(socket, "set-option", "-t", sessionTarget(session), "-u", option)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("unset tmux %s: %s", option, strings.TrimSpace(string(output)))
	}
	return nil
}

// Statuses is the pair of readiness states the daemon publishes: In tracks
// the microphone path (idle → recording → transcribing), Out the speaker
// path (idle → speaking). Both come from the daemon's socket "status"
// answer, not from the process being alive — a wedged daemon is worse than
// a dead one precisely because it looks ready, so every value outside the
// known set, including "" when the socket never answered, renders as the
// grey "off" rather than the green "idle".
type Statuses struct {
	In  string
	Out string
}

// Segment colours, One Dark palette. Idle stays green as before; recording
// and errors share the red that already meant "needs attention"; the new
// blue marks the speaker path so the two halves can never be confused.
const (
	colorReady = "#98C379"
	colorLive  = "#E06C75"
	colorBusy  = "#E5C07B"
	colorSpeak = "#61AFEF"
	colorOff   = "#5C6370"
)

// Indicator renders the "in:…"/"out:…" segment pair. It is a pure function
// so the exact strings can be pinned down in tests without a tmux server.
func Indicator(statuses Statuses) string { return indicatorWith(statuses, "#[default]") }

// indicatorWith ends the pair with the colour the following text should
// use: the attached status line returns to the session default, while the
// dedicated listener window restores its own muted label colour — the same
// convention the single-segment indicator used.
func indicatorWith(statuses Statuses, after string) string {
	return segment("in", statuses.In) + "#[default] " +
		segment("out", statuses.Out) + after + " "
}

// segment is one coloured "name:state" word, e.g. "#[fg=#98C379]in:idle".
func segment(name, state string) string {
	color, text := appearance(name, state)
	return "#[fg=" + color + "]" + name + ":" + text
}

// appearance maps one published state onto its colour and display text.
// Anything unrecognized — a typo, a stale peer, an empty string — falls
// through to "off", because for the user a state we cannot understand is
// indistinguishable from a daemon that is not answering.
func appearance(name, state string) (string, string) {
	switch name {
	case "in":
		switch state {
		case "idle", "pasted":
			// "pasted" is the bridge's word for a delivered transcription;
			// the input path is ready again either way.
			return colorReady, "idle"
		case "recording", "rec":
			return colorLive, "rec…"
		case "transcribing", "transcribe":
			return colorBusy, "transcribe…"
		case "delivering":
			return colorBusy, "deliver…"
		case "error", "err":
			return colorLive, "err"
		}
	case "out":
		switch state {
		case "idle":
			return colorReady, "idle"
		case "speaking", "speak":
			return colorSpeak, "speak…"
		case "error", "err":
			return colorLive, "err"
		}
	}
	return colorOff, "off"
}

// label is the static mode reminder after the segments; the changing part
// moved into in:/out: themselves.
func (s *Status) label() string {
	return "mode:" + s.mode
}

func (s *Status) set(ctx context.Context, option, value string) error {
	return setOption(ctx, s.socket, s.session, option, value)
}

func (s *Status) get(ctx context.Context, option string) (string, error) {
	return get(ctx, s.socket, s.session, option)
}

func setOption(ctx context.Context, socket, session, option, value string) error {
	output, err := exec.CommandContext(ctx, "tmux", tmuxArgv(socket, "set-option", "-t", sessionTarget(session), option, value)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("set tmux %s: %s", option, strings.TrimSpace(string(output)))
	}
	return nil
}

func get(ctx context.Context, socket, session, option string) (string, error) {
	output, err := exec.CommandContext(ctx, "tmux", tmuxArgv(socket, "show-option", "-t", sessionTarget(session), "-qv", option)...).Output()
	if err != nil {
		return "", fmt.Errorf("read tmux %s: %w", option, err)
	}
	return strings.TrimRight(string(output), "\n"), nil
}

// sessionTarget makes numeric session names unambiguous to tmux. A bare
// "1" is interpreted as window index 1; "1:" explicitly addresses session
// "1".
func sessionTarget(session string) string { return session + ":" }

// tmux evaluates # constructs inside status strings. Keep command-line input
// visible but inert, even if somebody passes unusual Codex arguments.
func cleanStatusText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		if r == '#' {
			return '＃'
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}
