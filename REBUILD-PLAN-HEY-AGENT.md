# Rebuild plan: `hey-agent`

Status: planned, not implemented.
Owner: full-stack implementation agent.
Product decision: the only public name is `hey-agent`.

## 1. Outcome

Rebuild the voice-input utility under the neutral name `hey-agent`. It must
send speech transcription to the explicitly selected tmux pane, regardless of
which assistant or model is running there.

The source/runtime layout is:

```text
tools/hey-agent/
  README.md
  .env            # local secret, mode 600, never committed
```

The executable is built in the repository and linked to `~/.local/bin/hey-agent`.

The source repository may remain at its current location during migration, but
all product-facing names, module paths, runtime paths, docs, service keys,
temporary files and packaging must become `hey-agent` before release.

## 2. Architecture

- Keep one foreground binary; no daemon, launch agent, systemd unit, tray,
  autostart or background watcher.
- Process startup performs only argument parsing and explicit command dispatch.
  `hey-agent` exits after help/doctor and does not capture keys or open
  ffmpeg until `listen` is explicitly invoked.
- `listen` owns the hotkey and tmux status only for its lifetime. ffmpeg is
  started only while recording; temporary audio is removed in all outcomes.
- The target pane is explicit or the current tmux pane. No GUI focus lookup,
  clipboard use or synthetic keystrokes outside the existing tmux send path.
- Keep `--submit` opt-in. Default behavior pastes transcription for review and
  never presses Enter.

## 3. Naming and storage migration

- Rename the Go module, command directory, binary, README title, Makefile,
  packaging metadata, tmux session/window/status options, environment variables,
  keychain service, temp-file prefix and user config directory to `hey-agent`.
- Remove `hey-claude`, `hey-codex` and `hey-claudex` aliases from installation
  and runtime logic. Historical references belong only in migration notes.
- Canonical runtime secret is `tools/hey-agent/.env`, permission `600`, ignored
  by Git. Read only `HEY_AGENT_API_KEY` or documented provider key aliases;
  never print secret values or commit them.
- Install creates only `~/.local/bin/hey-agent` and no resident service. There
  is no in-process uninstall command; Homebrew or removal of that symlink owns
  uninstall lifecycle.
- Update the archii tools index to point to the real project and this plan.

## 4. Provider contract

Use one configurable OpenAI-compatible transcription HTTP contract so the
binary can work with different providers and models:

```text
HEY_AGENT_BASE_URL   default: provider's transcription API base URL
HEY_AGENT_MODEL      required/default documented model name
HEY_AGENT_API_KEY    read from tools/hey-agent/.env or process environment
```

The provider client must send multipart audio, enforce a bounded request
timeout, return useful status errors without response secrets, and accept only
the transcription text needed by the tmux sender. Provider configuration is
loaded on explicit `listen`, never at idle process startup.

## 5. Commands

```text
hey-agent help
hey-agent doctor
hey-agent setup-key
hey-agent keys
hey-agent listen [flags]
hey-agent stop
```

`doctor` is read-only and checks dependencies, `.env` permissions, provider
configuration, tmux availability and platform input permissions. It must not
start a daemon or modify tmux state.

## 6. Implementation order

1. Freeze current dirty worktree state and record the migration boundary.
2. Rename module/package/imports and public command surface.
3. Remove aliases and all old runtime/config/key names.
4. Replace the fixed OpenAI configuration with the provider contract above.
5. Move secret loading to `tools/hey-agent/.env`; add permission and redaction
   checks.
6. Simplify startup so only `listen` allocates hotkey, tmux status and ffmpeg.
7. Update Makefile, packaging, README and archii tools documentation.
8. Build the single binary into `bin/hey-agent` and link it from
   `~/.local/bin/hey-agent`.
9. Run tests and a manual doctor/listen/stop smoke test in a disposable tmux
   session.
10. Commit the rename and rebuild separately from unrelated dirty changes.

## 7. Verification and acceptance

- `go test ./...` passes.
- `go vet ./...` passes.
- Binary contains no old public names, aliases, keychain services or config
  paths except migration documentation.
- Fresh install produces exactly one executable link at `~/.local/bin/hey-agent`.
- `.env` is `600`, ignored by Git, and never appears in logs or command output.
- Running `help`, `doctor` and `keys` consumes no microphone, ffmpeg,
  network or persistent process resources.
- No process or child process exists before explicit `listen`.
- `listen` records only after hotkey activation, sends one bounded request, and
  cleans temporary audio after success and failure.
- Default transcription is pasted but not submitted; `--submit` is the only
  path that presses Enter.
- `stop` restores tmux status and never kills the user's tmux session.
- Provider URL/model can be changed without rebuilding the binary.
- A missing key, missing dependency, unavailable pane and provider failure all
  produce actionable errors and leave no resident process or temporary audio.

## 8. Out of scope

- No automatic reading of agent output; that belongs to `agent-voice-over`.
- No model-specific aliases or assistant detection in the core product.
- No GUI application, tray icon, launch daemon or system service in v1.
- No raw audio retention beyond the single request lifecycle.
