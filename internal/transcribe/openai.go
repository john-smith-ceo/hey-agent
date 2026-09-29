package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.openai.com/v1"
	DefaultModel   = "gpt-transcribe"
	RequestTimeout = 60 * time.Second
)

type Config struct {
	BaseURL  string
	Model    string
	APIKey   string
	Language string
	Timeout  time.Duration
}

type Client interface {
	Transcribe(context.Context, string) (string, error)
}

type Provider struct {
	baseURL  string
	model    string
	apiKey   string
	language string
	http     *http.Client
}

func NewProvider(config Config) *Provider {
	baseURL := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultModel
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = RequestTimeout
	}
	return &Provider{
		baseURL:  baseURL,
		model:    model,
		apiKey:   config.APIKey,
		language: strings.TrimSpace(config.Language),
		http:     &http.Client{Timeout: timeout},
	}
}

func (p *Provider) endpoint(path string) string { return p.baseURL + "/" + strings.TrimLeft(path, "/") }

// Verify checks authentication and model visibility without uploading audio.
//
// The model is looked up in the list rather than fetched directly: retrieving
// gpt-transcribe by name answers 500, so a direct fetch would report a working
// key as broken.
func (p *Provider) Verify(ctx context.Context) error {
	if strings.TrimSpace(p.apiKey) == "" {
		return errors.New("API key is empty")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint("models"), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+p.apiKey)
	response, err := p.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("provider returned %s", response.Status)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &listed); err != nil {
		return err
	}
	for _, entry := range listed.Data {
		if entry.ID == p.model {
			return nil
		}
	}
	return fmt.Errorf("this key cannot see the %s model", p.model)
}

func (p *Provider) Transcribe(ctx context.Context, filename string) (string, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return "", errors.New("API key is empty")
	}
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", err
	}
	if err := w.WriteField("model", p.model); err != nil {
		return "", err
	}
	if p.language != "" {
		if err := w.WriteField("language", p.language); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("audio/transcriptions"), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := p.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("provider transcription API returned %s", resp.Status)
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return "", err
	}
	if strings.TrimSpace(data.Text) == "" {
		return "", errors.New("transcription response is empty")
	}
	return data.Text, nil
}
