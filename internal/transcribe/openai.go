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
	"unicode/utf8"
)

const (
	DefaultBaseURL = "https://api.openai.com/v1"
	DefaultModel   = "gpt-transcribe"
	RequestTimeout = 60 * time.Second
	// maxPromptRunes keeps the vocabulary prompt well inside the provider's
	// prompt budget (whisper-family models read only the last 224 tokens).
	maxPromptRunes = 500
)

type Config struct {
	BaseURL  string
	Model    string
	APIKey   string
	Language string
	Timeout  time.Duration
	// VocabularyFile lists names the model should spell exactly as written,
	// one per line; lines starting with "#" are comments. It is read on every
	// request, so edits apply without a restart. Empty or missing — no prompt.
	VocabularyFile string
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
	// vocabulary is the path of the vocabulary file, see Config.VocabularyFile.
	vocabulary string
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
		baseURL:    baseURL,
		model:      model,
		apiKey:     config.APIKey,
		language:   strings.TrimSpace(config.Language),
		http:       &http.Client{Timeout: timeout},
		vocabulary: config.VocabularyFile,
	}
}

// DefaultVocabularyFile is ~/.config/hey-agent/vocabulary.txt, next to the
// rest of hey-agent's private configuration.
func DefaultVocabularyFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "hey-agent", "vocabulary.txt")
}

// vocabularyPrompt turns the vocabulary into the transcription prompt: a short
// comma-separated list, which the model takes as spelling context — names come
// out as written ("Джарвис", "ВСадике") instead of the nearest common word.
// Words past maxPromptRunes are dropped whole.
func vocabularyPrompt(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var words []string
	size := 0
	for _, line := range strings.Split(string(data), "\n") {
		word := strings.TrimSpace(line)
		if word == "" || strings.HasPrefix(word, "#") {
			continue
		}
		n := utf8.RuneCountInString(word) + 2
		if size+n > maxPromptRunes {
			break
		}
		words = append(words, word)
		size += n
	}
	if len(words) == 0 {
		return ""
	}
	return strings.Join(words, ", ") + "."
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
	prompt := vocabularyPrompt(p.vocabulary)
	text, status, err := p.request(ctx, filename, prompt)
	// The vocabulary is only a hint: a provider that rejects the prompt must
	// not cost the user the recording, so retry once without it.
	if status == http.StatusBadRequest && prompt != "" {
		text, _, err = p.request(ctx, filename, "")
	}
	return text, err
}

// request sends one transcription request and reports the HTTP status, so
// Transcribe can tell a rejected prompt from other failures.
func (p *Provider) request(ctx context.Context, filename, prompt string) (string, int, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", 0, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return "", 0, err
	}
	if err := w.WriteField("model", p.model); err != nil {
		return "", 0, err
	}
	if p.language != "" {
		if err := w.WriteField("language", p.language); err != nil {
			return "", 0, err
		}
	}
	if prompt != "" {
		if err := w.WriteField("prompt", prompt); err != nil {
			return "", 0, err
		}
	}
	if err := w.Close(); err != nil {
		return "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint("audio/transcriptions"), &body)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := p.http.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", resp.StatusCode, fmt.Errorf("provider transcription API returned %s", resp.Status)
	}
	var data struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &data); err != nil {
		return "", resp.StatusCode, err
	}
	if strings.TrimSpace(data.Text) == "" {
		return "", resp.StatusCode, errors.New("transcription response is empty")
	}
	return data.Text, resp.StatusCode, nil
}
