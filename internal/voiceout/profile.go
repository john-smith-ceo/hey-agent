package voiceout

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Profile is one voice definition, the same JSON shape agent-voice-over
// used: engine picks the synthesizer, the rest tune it. Unknown JSON fields
// are ignored so profiles can carry notes.
type Profile struct {
	Name         string  `json:"name"`
	Title        string  `json:"title"`
	Version      string  `json:"version"`
	Engine       string  `json:"engine"`       // "openai" or "piper" (empty means piper)
	PiperBin     string  `json:"piper_bin"`    // default ~/.local/share/piper/engine/piper
	Model        string  `json:"model"`        // piper .onnx path or OpenAI model id
	LengthScale  float64 `json:"length_scale"` // piper pace, lower is faster
	Lexicon      string  `json:"lexicon"`      // file in ConfigDir, default lexicon.txt
	Filter       string  `json:"filter"`       // ffmpeg chain for piper output, {gain} placeholder
	GainDB       float64 `json:"gain_db"`
	Translit     bool    `json:"translit"`     // cyrillic -> latin before synthesis
	Voice        string  `json:"voice"`        // OpenAI voice id
	Instructions string  `json:"instructions"` // OpenAI style prompt
	Speed        float64 `json:"speed"`        // OpenAI base speed
}

// gentleFemaleStyle is the fallback style for the OpenAI voices that were
// picked as "soft female" in agent-voice-over; the original applied it
// unconditionally to these voices, so the port does the same.
const gentleFemaleStyle = "Говори мягко и нежно, тепло и заботливо, с естественным темпом и короткими паузами."

// openAIVoices is the built-in OpenAI voice list. A bare voice name (the
// Request.Voice field) resolves to a generated minimal profile, so callers
// can say `--voice nova` without anyone writing a JSON file first.
var openAIVoices = map[string]bool{
	"alloy": true, "ash": true, "ballad": true, "coral": true, "echo": true,
	"fable": true, "onyx": true, "nova": true, "sage": true, "shimmer": true,
	"verse": true, "marin": true, "cedar": true,
}

func isGentleOpenAIVoice(name string) bool {
	switch name {
	case "coral", "nova", "shimmer", "alloy", "sage", "verse", "marin":
		return true
	default:
		return false
	}
}

// builtinProfiles are the voice defaults compiled into the binary: the four
// agent-voice-over OpenAI profiles, minus their file storage. They are the
// fallback when the user has not installed any profile JSON yet; a file of
// the same name in the profiles directory wins over the builtin.
var builtinProfiles = map[string]Profile{
	"cedar": {
		Name: "Agent Voice Cedar", Title: "Сидар", Version: "v1",
		Engine: "openai", Model: "gpt-4o-mini-tts", Voice: "cedar",
		Instructions: "Спокойный уверенный мужской голос инженера. Кратко, по делу, без наигранности; ровный темп.",
		Speed:        1.1, Lexicon: "lexicon.txt",
	},
	"onyx": {
		Name: "Agent Voice Onyx", Title: "Оникс", Version: "v1",
		Engine: "openai", Model: "gpt-4o-mini-tts", Voice: "onyx",
		Instructions: "Спокойный сдержанный тон британского дворецкого. Сухая ирония, никакой наигранности, неторопливо и негромко.",
		Speed:        1.15, Lexicon: "lexicon.txt",
	},
	"fable": {
		Name: "Agent Voice Fable", Title: "Фейбл", Version: "v1",
		Engine: "openai", Model: "gpt-4o-mini-tts", Voice: "fable",
		Instructions: "Мужской голос, спокойный рассказчик. Ровный темп, внятно, без нажима.",
		Speed:        1.1, Lexicon: "lexicon.txt",
	},
	"marin": {
		Name: "Agent Voice Marin", Title: "Марин", Version: "v1",
		Engine: "openai", Model: "gpt-4o-mini-tts", Voice: "marin",
		Instructions: "Женский голос, живой и деловой. Уверенно, по делу, тёплый тон без сюсюканья.",
		Speed:        1.1, Lexicon: "lexicon.txt",
	},
}

// resolveProfile turns a request-level voice hint into a loaded Profile.
// An empty name means "the active profile". The lookup order is: JSON path,
// profiles directories (config first, data second — both read-only
// compatible with agent-voice-over files), built-in defaults, and finally
// the bare OpenAI voice list.
func (s *Speaker) resolveProfile(name string) (Profile, error) {
	if name == "" {
		name = s.activeProfileName()
	}
	if strings.HasSuffix(name, ".json") {
		return loadProfileFile(expand(name, s.home))
	}
	for _, dir := range []string{
		filepath.Join(s.configDir, "profiles"),
		filepath.Join(s.dataDir, "profiles"),
	} {
		path := filepath.Join(dir, name+".json")
		if _, err := os.Stat(path); err == nil {
			return loadProfileFile(path)
		}
	}
	if p, ok := builtinProfiles[name]; ok {
		return normalizeProfile(p), nil
	}
	if openAIVoices[name] {
		return normalizeProfile(openAIVoiceProfile(name)), nil
	}
	return Profile{}, fmt.Errorf("voiceout: unknown voice/profile %q", name)
}

// activeProfileName picks the standing voice: HEY_AGENT_VOICE_PROFILE wins
// over the `profile` marker file, and `cedar` is the fallback when neither
// exists.
func (s *Speaker) activeProfileName() string {
	if v := strings.TrimSpace(os.Getenv("HEY_AGENT_VOICE_PROFILE")); v != "" {
		return v
	}
	if b, err := os.ReadFile(filepath.Join(s.configDir, "profile")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	return defaultProfileName
}

// openAIVoiceProfile builds the minimal profile for a bare OpenAI voice
// name, the equivalent of what agent-voice-over generated into
// $CFG/openai-voices/<voice>.json on the fly.
func openAIVoiceProfile(voice string) Profile {
	return Profile{
		Name: "OpenAI " + voice, Title: "OpenAI " + voice, Version: "v1",
		Engine: "openai", Voice: voice, Speed: 1.1, Lexicon: "lexicon.txt",
	}
}

func loadProfileFile(path string) (Profile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, err
	}
	var p Profile
	if err := json.Unmarshal(b, &p); err != nil {
		return Profile{}, fmt.Errorf("voiceout: parse profile %s: %w", filepath.Base(path), err)
	}
	return normalizeProfile(p), nil
}

// normalizeProfile fills the fields the pipeline relies on, the same
// defaults loadConfig applied in agent-voice-over.
func normalizeProfile(p Profile) Profile {
	if p.Engine == "openai" && isGentleOpenAIVoice(p.Voice) && p.Instructions == "" {
		p.Instructions = gentleFemaleStyle
	}
	if p.LengthScale == 0 {
		p.LengthScale = 1
	}
	if p.Lexicon == "" {
		p.Lexicon = "lexicon.txt"
	}
	return p
}
