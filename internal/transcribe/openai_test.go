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
