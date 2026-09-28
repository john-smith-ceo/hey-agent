package daemon

import (
	"encoding/json"
	"fmt"
)

// Request is one newline-delimited JSON command (SPEC §2): one line in, one
// line out. The speak fields mirror the legacy voiceRequest contract so the
// `agent-voice-over send` payload keeps working after the symlink cutover —
// a bare {"text":…,"voice":…} line with no cmd is treated as speak.
type Request struct {
	Cmd string `json:"cmd"`

	// speak payload (voiceRequest-compatible). Speed fields are floats — the
	// legacy sender passes a rate factor and a percent boost, and decoding
	// them as bool/int would reject a valid voiceRequest line whole.
	Text         string  `json:"text,omitempty"`
	Voice        string  `json:"voice,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	NormalSpeed  float64 `json:"normal_speed,omitempty"`
	SpeedUp      float64 `json:"speed_up,omitempty"`

	// config payload. Submit is a pointer so "not sent" is distinguishable
	// from an explicit false. VoiceEnabled toggles speak/hush without
	// restarting the daemon (the tray "Voice output" checkbox sends it).
	Submit       *bool  `json:"submit,omitempty"`
	VoiceEnabled *bool  `json:"voice_enabled,omitempty"`
	Hotkey       string `json:"hotkey,omitempty"`
	Silence      string `json:"silence,omitempty"`
	Device       string `json:"device,omitempty"`
	Mode         string `json:"mode,omitempty"`
}

// SpeakRequest is the voice part of a Request — the fields internal/voiceout
// will consume once it plugs into the Speaker port.
type SpeakRequest struct {
	Text         string  `json:"text"`
	Voice        string  `json:"voice,omitempty"`
	Instructions string  `json:"instructions,omitempty"`
	NormalSpeed  float64 `json:"normal_speed,omitempty"`
	SpeedUp      float64 `json:"speed_up,omitempty"`
}

func (r Request) speakRequest() SpeakRequest {
	return SpeakRequest{
		Text:         r.Text,
		Voice:        r.Voice,
		Instructions: r.Instructions,
		NormalSpeed:  r.NormalSpeed,
		SpeedUp:      r.SpeedUp,
	}
}

// Response is the single JSON line written back to the client.
type Response map[string]any

func okResponse() Response            { return Response{"ok": true} }
func errResponse(msg string) Response { return Response{"ok": false, "error": msg} }
func errResponsef(format string, a ...any) Response {
	return errResponse(fmt.Sprintf(format, a...))
}

// Reply is a daemon answer decoded by CLI clients. Optional fields stay empty
// when the answer does not carry them.
type Reply struct {
	OK           bool            `json:"ok"`
	Error        string          `json:"error,omitempty"`
	State        string          `json:"state,omitempty"`
	Bound        bool            `json:"bound,omitempty"`
	Target       string          `json:"target,omitempty"`
	Submit       bool            `json:"submit,omitempty"`
	VoiceEnabled bool            `json:"voice_enabled,omitempty"`
	UptimeSec    int64           `json:"uptime_sec,omitempty"`
	Version      string          `json:"version,omitempty"`
	Queued       bool            `json:"queued,omitempty"`
	Detail       string          `json:"detail,omitempty"`
	Note         string          `json:"note,omitempty"`
	Config       json.RawMessage `json:"config,omitempty"`
}
