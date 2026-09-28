# hey-agent

Voice input for an explicitly selected tmux pane. `hey-agent` records
speech, sends the audio to an OpenAI-compatible transcription API and pastes the text
into your pane. It starts nothing and launches nothing: your assistant is
already running, and the transcription goes to it.

macOS and Linux (X11).

## Name

The product has one public executable: `hey-agent`. It sends transcription
only to the explicit `--target` pane or to the current tmux pane.

## Install

### Codex plugin

После обычной установки бинарника добавьте marketplace из GitHub и включите
плагин:

```sh
codex plugin marketplace add john-smith-ceo/hey-claudex --ref main
codex plugin add hey-codex@hey-claudex
```

В панели tmux, где уже работает Codex, вызовите `/hey-codex`. Плагин включит
слушатель для этой же панели: не нужен отдельный target, соседняя панель или
другой assistant. Остановить: `hey-claudex stop`.

Плагин управляет уже установленным `hey-codex`; `ffmpeg`, `tmux`, доступ к
микрофону и сам бинарник всё ещё устанавливаются по инструкции ниже.

### macOS

```sh
brew install ffmpeg tmux
git clone https://github.com/john-smith-ceo/hey-agent
cd hey-agent && go build -o bin/hey-agent ./cmd/hey-agent && ./bin/hey-agent install
```

Grant Microphone and Accessibility permission to the terminal application that
launches it — the key is captured through a CoreGraphics event tap.

### Linux

Needs an X11 session; Wayland is not supported yet. The key is captured through
XRecord, which requires the Xlib headers at build time:

```sh
sudo apt install ffmpeg tmux libx11-dev libxtst-dev
git clone https://github.com/john-smith-ceo/hey-agent
cd hey-agent && go build -o bin/hey-agent ./cmd/hey-agent && ./bin/hey-agent install
```

`install` links `hey-agent` into `~/.local/bin`.

### First run

```sh
hey-agent setup-key            # store the provider API key in tools/hey-agent/.env
hey-agent doctor               # ffmpeg, tmux, hotkey, provider config
```

`setup-key --env-file /path/to/.env` reads `OPENAI_API_KEY` or
`OPEN_AI_API_KEY` from a dotenv file instead of prompting.

## Provider

The provider uses one OpenAI-compatible HTTP contract. Configuration is read
when `listen` starts:

```sh
export HEY_AGENT_BASE_URL=https://api.openai.com/v1
export HEY_AGENT_MODEL=gpt-transcribe
export HEY_AGENT_API_KEY=...
```

The canonical local secret file is `tools/hey-agent/.env` with mode `600`;
`HEY_AGENT_ENV_FILE` can override its path. `HEY_AGENT_BASE_URL` and
`HEY_AGENT_MODEL` are optional and default to the
OpenAI-compatible endpoint and `gpt-transcribe`. The request timeout is 60
seconds. API key values are never printed.

## Use

From the pane where your assistant runs:

```sh
hey-agent listen
```

Press Right Alt (Right Option on macOS), speak, then press it again — or simply
pause. The text appears in the input line and **is not submitted**: you read it
and press Enter yourself.

```sh
hey-agent listen --submit        # send it as soon as the recording ends
hey-agent listen --mode push     # record only while the key is held
hey-agent listen --key Shift_L   # a different key
hey-agent listen --silence 2s   # a shorter pause ends the recording sooner
hey-agent stop                   # stop listening
hey-agent keys                   # every selectable key
hey-agent config --mode push --silence 2s --submit
                                # change settings for the next recording
hey-agent config --no-submit    # return to manual review
```

Runtime settings can be changed while the listener is running. They are
applied only after the current recording has finished, never in the middle of
recording. The command must be run from the tmux session whose listener is
being configured.

`--submit` is deliberately independent of `--mode`: how a recording starts and
what happens to its result are separate questions, and holding a key, releasing
it and watching the text go is the most useful combination of the two.

Between the paste and the Enter there is a quarter-second pause. tmux
acknowledges the paste immediately, but the receiving program is still digesting
the bracketed-paste terminator, and an Enter arriving in the same burst of
events is swallowed as pasted text. `--submit-delay` widens it for a slow
application.

## Keys

Keys fall into two groups, and the group decides what is possible.

**Free** — `Alt_R`, `Super_R`, `Menu`, `Scroll_Lock`, `Pause`, `Caps_Lock`. They
do nothing on their own, so a bare press is unambiguous and both modes work.

**Typing** — `Shift_L/R`, `Control_L/R`, `Alt_L`, `Super_L`. These are pressed
constantly while writing, so only a solo press counts: down and up with no other
key in between and within 400 ms. A capital letter never starts a recording.
Hold-to-talk is impossible for them and is refused at startup — holding Shift
is indistinguishable from ordinary typing.

macOS keyboards have no `Menu`, `Scroll_Lock` or `Pause`; asking for one there
is refused with an explanation.

On macOS a free key is swallowed and never reaches applications, the way the
original Right Option tap worked. On Linux XRecord observes the stream without
consuming it, so the key keeps its normal function — which is why the default
is a key that has none.

## Daemon mode

`hey-agent daemon` runs the same input pipeline as a foreground service —
the shape a systemd user unit (`hey-agent.service`, see JOH-348) or a manual
launch uses:

```sh
hey-agent daemon
```

With no `--target` the daemon follows the **attached tmux client**: every
recording resolves the pane the user is actually looking at (the client with
the latest `client_activity`), and re-verifies that pane right before the
text is pasted. If the pane closed, the client detached, or the target moved
mid-recording, the transcript is dropped rather than pasted into the wrong
place — fail closed. `--target %7` pins a fixed pane for debugging;
`--tmux-socket name` points the whole daemon at a different tmux server
(`tmux -L name`).

A Linux tray icon (StatusNotifierItem over D-Bus, no GTK) mirrors the state
machine and offers bind/unbind, submit and voice-output checkboxes, hush and
quit. Without a tray host the daemon runs headless — `--no-tray` asks for
that explicitly.

The daemon owns a unix socket: `$XDG_RUNTIME_DIR/hey-agent.sock`
(fallback `/tmp/hey-agent-$UID.sock`; `HEY_AGENT_SOCKET` overrides). The file
is `0600`, the protocol is one JSON line per request and per reply, and every
command answers within two seconds or fails. Every daemon client command —
`daemon`, `status`, `bind`, `unbind`, `speak`, `hush`, `stop` — also accepts
`--socket` to point at a non-default path. The CLI commands are thin
clients of that socket:

| Command | Purpose |
|---|---|
| `hey-agent status` | state, hotkey bound, target pane, uptime, version |
| `hey-agent bind` / `unbind` | grab / release the hotkey without stopping the daemon |
| `echo text \| hey-agent speak` | speak text aloud; `speak -t "…"`, `--voice`, `--instructions`, `--speed-up`, `--normal-speed` are the `agent-voice-over send` fields |
| `hey-agent hush` | cut the playing phrase off |
| `hey-agent config --submit --silence 2s` | change runtime settings through the socket |
| `hey-agent stop` | graceful shutdown (falls back to the legacy listener when no daemon runs) |

The state machine is `idle → recording → transcribing → delivering → idle`,
plus a `speaking` lane that is mutually exclusive with recording: `speak`
during a recording waits in a one-deep queue (a newer `speak` replaces the
waiting one), and pressing the hotkey while speaking hushes the phrase and
starts recording. A `--busy-file` marker delays `speak` while it is fresh and
is discarded as abandoned once it is older than 300 s.

`hey-agent run --attached` and `hey-agent listen` keep working unchanged —
the daemon is additive, not a replacement yet.

## Status line

State is shown at the bottom of the terminal: `mode:tap`, `rec…`,
`transcribe…`, `done`, `error`, and `mode:tap auto` when submission is on.

In a session you already had, `hey-agent` only appends its indicator to
`status-right` and parks the previous value, restoring it when it stops. Your
theme stays yours. It also raises `status-right-length`, which defaults to 40
characters — short enough to cut the indicator off entirely.

`stop` cleans up even when the listener window died on its own: the borrowed
status line still has to be given back.

## Silence while recording

If something reads answers out loud, it ends up inside the recording. Two flags
prevent that:

```sh
hey-agent listen --busy-file ~/.config/jarvis-voice/mic-busy \
                 --on-record "jarvis-voice hush"
```

The file says "microphone busy" to anything that watches it; the command
interrupts what is already speaking, which a file cannot do. Both also come
from `HEY_AGENT_BUSY_FILE` and `HEY_AGENT_ON_RECORD`.

## Privacy and safety

- Audio is uploaded only after the recording stops.
- Temporary WAV files are removed after success or failure.
- The target pane is explicit: no active-window lookup, no clipboard, no GUI
  focus control, no synthetic keystrokes beyond the optional Enter.
- Without `--submit`, nothing is ever submitted for you.
- An empty transcription is never sent.
- `stop` removes only what `hey-agent` started. It never kills your session.

## Development

```sh
go test ./...
go build ./cmd/hey-agent
```

## License

[MIT](LICENSE)
