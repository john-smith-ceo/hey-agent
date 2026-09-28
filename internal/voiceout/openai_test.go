package voiceout

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func ttsResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytesReader(body)),
		Request:    request,
	}
}

func bytesReader(b []byte) io.Reader { return strings.NewReader(string(b)) }

func testOpenAIEngine(transport http.RoundTripper) *openAIEngine {
	return &openAIEngine{
		baseURL: "https://tts.invalid/v1",
		model:   "env-model",
		voice:   "env-voice",
		key:     "test-key",
		http:    &http.Client{Transport: transport},
	}
}

func TestOpenAISynthesizesSpeechRequest(t *testing.T) {
	var captured map[string]any
	e := testOpenAIEngine(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.String() != "https://tts.invalid/v1/audio/speech" {
			t.Errorf("url = %s", r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		return ttsResponse(r, http.StatusOK, fakeWAV), nil
	}))

	profile := Profile{Engine: "openai", Model: "gpt-4o-mini-tts", Voice: "cedar",
		Instructions: "speak slowly", Speed: 1.1}
	data, err := e.Synthesize(context.Background(), profile, 1.25, "Привет.")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(fakeWAV) {
		t.Fatalf("audio bytes lost: %d", len(data))
	}
	if captured["model"] != "gpt-4o-mini-tts" || captured["voice"] != "cedar" ||
		captured["input"] != "Привет." || captured["instructions"] != "speak slowly" ||
		captured["response_format"] != "wav" {
		t.Fatalf("bad request body: %v", captured)
	}
	// speed = profile.Speed * rate = 1.1 * 1.25
	if speed, ok := captured["speed"].(float64); !ok || speed < 1.37 || speed > 1.38 {
		t.Fatalf("speed = %v, want ~1.375", captured["speed"])
	}
}

func TestOpenAIEnvDefaultsFillProfileGaps(t *testing.T) {
	var captured map[string]any
	e := testOpenAIEngine(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		_ = json.NewDecoder(r.Body).Decode(&captured)
		return ttsResponse(r, http.StatusOK, fakeWAV), nil
	}))
	// Profile leaves model and voice empty: env-level defaults apply.
	if _, err := e.Synthesize(context.Background(), Profile{Engine: "openai"}, 1, "hi"); err != nil {
		t.Fatal(err)
	}
	if captured["model"] != "env-model" || captured["voice"] != "env-voice" {
		t.Fatalf("env defaults not applied: %v", captured)
	}
	if _, hasInstructions := captured["instructions"]; hasInstructions {
		t.Fatalf("empty instructions must be omitted: %v", captured)
	}
}

func TestOpenAIErrorHidesBodyAndKey(t *testing.T) {
	e := testOpenAIEngine(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return ttsResponse(r, http.StatusUnauthorized, []byte("provider-secret-detail")), nil
	}))
	_, err := e.Synthesize(context.Background(), Profile{Engine: "openai"}, 1, "hi")
	if err == nil {
		t.Fatal("401 must be an error")
	}
	if strings.Contains(err.Error(), "provider-secret-detail") || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error leaks body or key: %v", err)
	}
}

func TestOpenAINoKeyIsCleanError(t *testing.T) {
	e := testOpenAIEngine(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("request must not be sent without a key")
		return nil, nil
	}))
	e.key = ""
	_, err := e.Synthesize(context.Background(), Profile{Engine: "openai"}, 1, "hi")
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("missing key error = %v", err)
	}
	if strings.Contains(err.Error(), "test-key") {
		t.Fatalf("error leaks key: %v", err)
	}
}

func TestResolveAPIKeyOrder(t *testing.T) {
	// Explicit config wins over everything.
	t.Setenv("HEY_AGENT_API_KEY", "sk-env")
	if got := resolveAPIKey("sk-cfg"); got != "sk-cfg" {
		t.Fatalf("explicit key = %q", got)
	}
	if got := resolveAPIKey(""); got != "sk-env" {
		t.Fatalf("env key = %q", got)
	}
	// Falls back to the env file when no env var is set.
	t.Setenv("HEY_AGENT_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPEN_AI_API_KEY", "")
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("OPENAI_API_KEY=sk-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEY_AGENT_ENV_FILE", envFile)
	if got := resolveAPIKey(""); got != "sk-file" {
		t.Fatalf("env-file key = %q", got)
	}
}
