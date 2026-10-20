package transcribe

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func providerResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func TestTranscribeSendsMultipartAudio(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "audio-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("RIFF fake wav")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1"})
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authorization = %q", got)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, err = %v", r.Header.Get("Content-Type"), err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatal(nextErr)
			}
			data, readErr := io.ReadAll(part)
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch part.FormName() {
			case "model":
				if string(data) != DefaultModel {
					t.Errorf("model = %q", data)
				}
			case "file":
				if string(data) != "RIFF fake wav" {
					t.Errorf("file = %q", data)
				}
			}
		}
		return providerResponse(r, http.StatusOK, `{"text":"hello"}`), nil
	})}
	text, err := c.Transcribe(context.Background(), file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestProviderRedactsHTTPErrorBody(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(file, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1"})
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return providerResponse(r, http.StatusUnauthorized, "provider-secret-response"), nil
	})}
	_, err := c.Transcribe(context.Background(), file)
	if err == nil || strings.Contains(err.Error(), "provider-secret-response") {
		t.Fatalf("error leaked provider body: %v", err)
	}
}

func TestProviderUsesConfiguredModel(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(file, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", Model: "custom-model"})
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Fatalf("content type = %q, err = %v", r.Header.Get("Content-Type"), err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, nextErr := reader.NextPart()
			if nextErr == io.EOF {
				break
			}
			if nextErr != nil {
				t.Fatal(nextErr)
			}
			data, readErr := io.ReadAll(part)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if part.FormName() == "model" && string(data) != "custom-model" {
				t.Errorf("model = %q", data)
			}
		}
		return providerResponse(r, http.StatusOK, `{"text":"ok"}`), nil
	})}
	if _, err := c.Transcribe(context.Background(), file); err != nil {
		t.Fatal(err)
	}
}

func TestProviderRequestTimeoutIsBounded(t *testing.T) {
	file := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(file, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", Timeout: 10 * time.Millisecond})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	started := time.Now()
	_, err := c.Transcribe(context.Background(), file)
	if err == nil {
		t.Fatal("request timeout must return an error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request was not bounded: %s", elapsed)
	}
}

// formFields reads the multipart form of a transcription request (file part
// included) into name → value.
func formFields(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	reader := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, nextErr := reader.NextPart()
		if nextErr == io.EOF {
			return fields
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		data, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Fatal(readErr)
		}
		fields[part.FormName()] = string(data)
	}
}

func writeAudio(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(file, []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestTranscribeSendsVocabularyPrompt(t *testing.T) {
	vocabulary := filepath.Join(t.TempDir(), "vocabulary.txt")
	if err := os.WriteFile(vocabulary, []byte("# names to keep\nДжарвис\n\n  ВСадике  \n# Devin is spelled in Russian\nДевин\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", VocabularyFile: vocabulary})
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := formFields(t, r)["prompt"]; got != "Джарвис, ВСадике, Девин." {
			t.Errorf("prompt = %q", got)
		}
		return providerResponse(r, http.StatusOK, `{"text":"Джарвис"}`), nil
	})}
	if _, err := c.Transcribe(context.Background(), writeAudio(t)); err != nil {
		t.Fatal(err)
	}
}

func TestTranscribeWithoutVocabularySendsNoPrompt(t *testing.T) {
	for name, path := range map[string]string{"unset": "", "missing": filepath.Join(t.TempDir(), "absent.txt")} {
		c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", VocabularyFile: path})
		c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if _, ok := formFields(t, r)["prompt"]; ok {
				t.Errorf("%s vocabulary: prompt must not be sent", name)
			}
			return providerResponse(r, http.StatusOK, `{"text":"ok"}`), nil
		})}
		if _, err := c.Transcribe(context.Background(), writeAudio(t)); err != nil {
			t.Fatalf("%s vocabulary: %v", name, err)
		}
	}
}

func TestTranscribeRetriesWithoutPromptWhenRejected(t *testing.T) {
	vocabulary := filepath.Join(t.TempDir(), "vocabulary.txt")
	if err := os.WriteFile(vocabulary, []byte("Джарвис\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", VocabularyFile: vocabulary})
	var prompts []string
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		prompt := formFields(t, r)["prompt"]
		prompts = append(prompts, prompt)
		if prompt != "" {
			return providerResponse(r, http.StatusBadRequest, `{"error":{"param":"prompt"}}`), nil
		}
		return providerResponse(r, http.StatusOK, `{"text":"без подсказки"}`), nil
	})}
	text, err := c.Transcribe(context.Background(), writeAudio(t))
	if err != nil {
		t.Fatal(err)
	}
	if text != "без подсказки" || len(prompts) != 2 || prompts[0] == "" || prompts[1] != "" {
		t.Fatalf("text = %q, prompts = %q", text, prompts)
	}
}

func TestTranscribeDoesNotRetryOtherErrors(t *testing.T) {
	vocabulary := filepath.Join(t.TempDir(), "vocabulary.txt")
	if err := os.WriteFile(vocabulary, []byte("Джарвис\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", VocabularyFile: vocabulary})
	calls := 0
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return providerResponse(r, http.StatusUnauthorized, "denied"), nil
	})}
	if _, err := c.Transcribe(context.Background(), writeAudio(t)); err == nil {
		t.Fatal("401 must stay an error")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestVocabularyPromptStaysShort(t *testing.T) {
	vocabulary := filepath.Join(t.TempDir(), "vocabulary.txt")
	if err := os.WriteFile(vocabulary, []byte(strings.Repeat("Длинноеслово\n", 200)), 0o600); err != nil {
		t.Fatal(err)
	}
	prompt := vocabularyPrompt(vocabulary)
	if n := len([]rune(prompt)); n == 0 || n > maxPromptRunes+1 {
		t.Fatalf("prompt length = %d runes", n)
	}
	if !strings.HasSuffix(prompt, "Длинноеслово.") {
		t.Fatalf("prompt must end with a whole word: %q", prompt[len(prompt)-30:])
	}
}

func TestTranscribeSendsLanguageWithPrompt(t *testing.T) {
	vocabulary := filepath.Join(t.TempDir(), "vocabulary.txt")
	if err := os.WriteFile(vocabulary, []byte("Джарвис\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewProvider(Config{APIKey: "test-key", BaseURL: "https://provider.invalid/v1", Language: " ru ", VocabularyFile: vocabulary})
	c.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		fields := formFields(t, r)
		if fields["language"] != "ru" || fields["prompt"] != "Джарвис." {
			t.Errorf("language = %q, prompt = %q", fields["language"], fields["prompt"])
		}
		return providerResponse(r, http.StatusOK, `{"text":"ok"}`), nil
	})}
	if _, err := c.Transcribe(context.Background(), writeAudio(t)); err != nil {
		t.Fatal(err)
	}
}
