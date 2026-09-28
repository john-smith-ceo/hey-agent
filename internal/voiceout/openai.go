package voiceout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTTSBaseURL = "https://api.openai.com/v1"
	defaultTTSModel   = "gpt-4o-mini-tts"
	defaultTTSVoice   = "onyx"
	ttsTimeout        = 60 * time.Second

	// maxTTSAudio bounds one stage of synthesized speech. 32 MiB is far
	// beyond what 400 characters can produce; the cap only guards the cache
	// and memory against a misbehaving provider.
	maxTTSAudio = 32 << 20
)

// openAIEngine speaks through an OpenAI-compatible /audio/speech endpoint.
// Endpoint, model and voice come from HEY_AGENT_TTS_* env, and the profile
// may still override model and voice per voice line.
type openAIEngine struct {
	baseURL string
	model   string // fallback when the profile leaves model empty
	voice   string // fallback when the profile leaves voice empty
	key     string // never logged, never returned in errors
	http    *http.Client
}

func newOpenAIEngine(apiKey string) *openAIEngine {
	base := strings.TrimRight(strings.TrimSpace(firstEnv("HEY_AGENT_TTS_BASE_URL", defaultTTSBaseURL)), "/")
	return &openAIEngine{
		baseURL: base,
		model:   firstEnv("HEY_AGENT_TTS_MODEL", defaultTTSModel),
		voice:   firstEnv("HEY_AGENT_TTS_VOICE", defaultTTSVoice),
		key:     resolveAPIKey(apiKey),
		http:    &http.Client{Timeout: ttsTimeout},
	}
}

func (e *openAIEngine) Synthesize(ctx context.Context, p Profile, rate float64, text string) ([]byte, error) {
	if e.key == "" {
		return nil, errors.New("voiceout: TTS API key is not configured (set HEY_AGENT_API_KEY)")
	}
	model := p.Model
	if model == "" {
		model = e.model
	}
	voice := p.Voice
	if voice == "" {
		voice = e.voice
	}
	speed := p.Speed
	if speed <= 0 {
		speed = 1
	}
	speed *= rate
	body := map[string]any{
		"model":           model,
		"input":           text,
		"voice":           voice,
		"response_format": "wav",
		"speed":           speed,
	}
	if p.Instructions != "" {
		body["instructions"] = p.Instructions
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/audio/speech", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("voiceout: openai tts: %w", err)
	}
	defer resp.Body.Close()
	// Error and audio paths are both read with a cap; the error message
	// carries only the HTTP status — never the body, which could quote the
	// request back or leak provider internals.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("voiceout: openai tts: provider returned %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTTSAudio+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTTSAudio {
		return nil, errors.New("voiceout: openai tts: response exceeds size limit")
	}
	return data, nil
}

// resolveAPIKey finds the provider key without ever exposing it: the explicit
// Config.APIKey first, then the shared env names, then the hey-agent env
// file. The list intentionally matches what `hey-agent doctor` and
// setup-key already use, so one key serves both transcription and speech.
func resolveAPIKey(explicit string) string {
	if key := strings.TrimSpace(explicit); key != "" {
		return key
	}
	for _, name := range []string{"HEY_AGENT_API_KEY", "OPENAI_API_KEY", "OPEN_AI_API_KEY"} {
		if key := strings.TrimSpace(os.Getenv(name)); key != "" {
			return key
		}
	}
	if key, err := loadEnvFileKey(agentEnvFile()); err == nil {
		return key
	}
	return ""
}

// agentEnvFile mirrors cmd/hey-agent: HEY_AGENT_ENV_FILE overrides the
// canonical ~/Tools/linux/hey-agent/.env.
func agentEnvFile() string {
	if path := strings.TrimSpace(os.Getenv("HEY_AGENT_ENV_FILE")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Tools", "linux", "hey-agent", ".env")
}

// loadEnvFileKey reads one NAME=value secret file. Accepts the same three
// key names as the rest of hey-agent and tolerates export/quotes; anything
// else in the file is ignored.
func loadEnvFileKey(path string) (string, error) {
	if path == "" {
		return "", errors.New("no env file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		name, value, found := strings.Cut(line, "=")
		if !found || !ttsKeyNames[strings.TrimSpace(name)] {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			} else {
				value = value[1 : len(value)-1]
			}
		}
		if value == "" {
			return "", fmt.Errorf("%s is empty", strings.TrimSpace(name))
		}
		return value, nil
	}
	return "", errors.New("no API key found in env file")
}

var ttsKeyNames = map[string]bool{
	"HEY_AGENT_API_KEY": true,
	"OPENAI_API_KEY":    true,
	"OPEN_AI_API_KEY":   true,
}
