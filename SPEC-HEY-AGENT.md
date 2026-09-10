# hey-agent: review and implementation specification

## Review findings

### High priority

1. Provider and secret paths needed stronger tests. The runtime now uses an
   OpenAI-compatible provider with a bounded 60-second HTTP timeout and a
   `600` dotenv file, but these guarantees must stay executable in tests.
2. Cancellation must be terminal for the current recording: `Escape` must not
   transcribe, paste, or submit audio. The bridge test covers the state
   transition; a future recorder integration test should assert no provider
   call as well.
3. The full suite contains a localhost `httptest` dependency. CI must permit
   loopback sockets; sandbox runs may report an environment failure there.

### Medium priority

1. `doctor --verify-api` assumes a `/models` endpoint. Providers that support
   transcription but hide model listing should be documented or get a direct
   bounded probe in a later provider revision.
2. The listener is supervised by a hidden tmux window. This is intentional and
   preserves the no-daemon rule, but every stop path must restore tmux status.
3. `setup-key` writes outside the repository into `tools/hey-agent/.env`.
   The directory must be created with `0700`; the file must be `0600`; neither
   path nor key contents may be logged as a secret value.

## Test specification

The automated suite must cover:

- `Escape` emits only `Cancel` on macOS and X11 event adapters.
- cancellation returns bridge state to `idle` and never enters transcription;
- dotenv save/load, mode `0600`, empty-key rejection, and key-name parsing;
- provider custom base URL/model, multipart field shape, 60-second default
  timeout, and redacted HTTP error messages;
- empty transcription rejection and tmux submit opt-in;
- tmux status restoration after normal stop and already-gone listener window.

## UI scope decision

System tray/menu-bar integration is explicitly out of scope for v1. The
listener remains terminal/tmux-only, with no cgo GUI dependency, daemon,
autostart entry, or desktop-environment requirements.
