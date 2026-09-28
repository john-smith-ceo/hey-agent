// Package voiceout turns assistant text into spoken audio.
//
// It is a port of the agent-voice-over speaker pipeline (see
// docs/voiceover-features.md): markdown cleanup, a pronunciation lexicon,
// sentence staging, OpenAI or Piper synthesis, a WAV cache, and ffplay/mpv
// playback. The daemon owns the speak queue and the record/speak interlock —
// this package only plays one request at a time and can be interrupted with
// Hush.
package voiceout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// maxStageChars bounds one synthesized phrase. Short phrases fail fast
	// on network errors and, more importantly, let Hush interrupt speech at
	// a sentence boundary instead of mid-paragraph.
	maxStageChars = 400

	// maxSpeedUpPercent is the accepted range for Request.SpeedUp,
	// matching the agent-voice-over socket contract (0..25).
	maxSpeedUpPercent = 25.0

	// minWAVSize is the size of a bare WAV header. Anything at or below it
	// is treated as "engine produced no audio".
	minWAVSize = 44

	// defaultProfileName is used when neither the config file
	// (configDir/profile) nor HEY_AGENT_VOICE_PROFILE pick a profile.
	defaultProfileName = "cedar"
)

// Config configures a Speaker. Directories default to the XDG-style hey-agent
// locations; secrets are read from the environment or the hey-agent env file
// and are never written to logs or error messages.
type Config struct {
	ConfigDir string // default ~/.config/hey-agent
	DataDir   string // default ~/.local/share/hey-agent
	APIKey    string // shared provider key (HEY_AGENT_API_KEY); resolved in New
	Player    Player // optional playback override, mostly for tests
}

// Request is one spoken phrase. It mirrors the agent-voice-over voiceRequest
// socket fields so the daemon can map them one to one.
type Request struct {
	Text         string  `json:"text"`
	Voice        string  `json:"voice,omitempty"`        // profile name or OpenAI voice
	Instructions string  `json:"instructions,omitempty"` // per-phrase style override
	NormalSpeed  float64 `json:"normal_speed,omitempty"` // base rate, 0 means 1.0
	SpeedUp      float64 `json:"speed_up,omitempty"`     // percent boost, 0..25
}

// Speaker synthesizes text and plays it. The zero value is not usable;
// build one with New. Speak serializes itself (the daemon still owns the
// real queue between clients) and Hush is safe to call at any time.
type Speaker struct {
	configDir string
	dataDir   string
	cacheDir  string
	home      string

	player  Player
	engines map[string]synthEngine // dispatch table; tests can inject fakes

	speakMu sync.Mutex         // serializes Speak calls in this process
	mu      sync.Mutex         // guards cancel
	cancel  context.CancelFunc // cancels the in-flight Speak
}

// New resolves directories and environment and returns a ready Speaker.
// Config and data directories are created with 0700 like the rest of
// hey-agent's private state.
func New(cfg Config) (*Speaker, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configDir := cfg.ConfigDir
	if configDir == "" {
		configDir = filepath.Join(home, ".config", "hey-agent")
	}
	dataDir := cfg.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share", "hey-agent")
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	player := cfg.Player
	if player == nil {
		player = newCommandPlayer(firstEnv("HEY_AGENT_PLAYER", "ffplay"))
	}
	s := &Speaker{
		configDir: configDir,
		dataDir:   dataDir,
		cacheDir:  filepath.Join(dataDir, "cache"),
		home:      home,
		player:    player,
	}
	s.engines = map[string]synthEngine{
		"openai": newOpenAIEngine(cfg.APIKey),
		"piper":  piperEngine{home: home},
	}
	return s, nil
}

// Speak synthesizes req and plays it, blocking until the last phrase has
// finished. It returns nil when cleanup leaves nothing to say. Cancellation —
// from ctx or from Hush — stops playback and skips the remaining phrases;
// the returned error is then ctx.Err(), so the caller can tell "interrupted"
// apart from a real failure.
func (s *Speaker) Speak(ctx context.Context, req Request) error {
	rate, err := requestRate(req)
	if err != nil {
		return err
	}
	profile, err := s.resolveProfile(req.Voice)
	if err != nil {
		return err
	}
	if req.Instructions != "" {
		profile.Instructions = req.Instructions
	}

	// Pipeline ported from agent-voice-over: first strip markdown noise the
	// synthesizer would read aloud, then apply the pronunciation lexicon
	// (and translit for the English-reading-Russian profile), then split
	// into speakable stages.
	text := cleanMarkdown(req.Text)
	text = applyLexicon(text, s.lexiconPath(profile))
	if text == "" {
		return nil
	}
	if profile.Translit {
		text = translit(text)
	}
	stages := splitStages(text, maxStageChars)

	s.speakMu.Lock()
	defer s.speakMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}()

	for _, stage := range stages {
		if err := ctx.Err(); err != nil {
			return err
		}
		wav, err := s.synthesize(ctx, profile, rate, strings.Join(stage, " "))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := s.player.Play(ctx, wav); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
	return nil
}

// Hush interrupts the phrase currently playing: it cancels the in-flight
// Speak context and kills the player process. Safe to call when nothing is
// playing and from any goroutine.
func (s *Speaker) Hush() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.player.Stop()
}

// synthesize returns the WAV file for one stage, serving it from the cache
// when the exact same engine/voice/rate/text was spoken before. The cache is
// what makes repeated phrases ("Готово.", confirmations) instant and free.
func (s *Speaker) synthesize(ctx context.Context, profile Profile, rate float64, text string) (string, error) {
	engine, err := s.engineFor(profile.Engine)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return "", err
	}
	out := filepath.Join(s.cacheDir, cacheKey(profile, rate, text)+".wav")
	if st, err := os.Stat(out); err == nil && st.Size() > minWAVSize {
		return out, nil
	}
	data, err := engine.Synthesize(ctx, profile, rate, text)
	if err != nil {
		return "", err
	}
	if len(data) <= minWAVSize {
		return "", errors.New("voiceout: engine produced no audio")
	}
	tmp, err := os.CreateTemp(s.cacheDir, ".synth-*.wav")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return out, os.Rename(tmpName, out)
}

// cacheKey fingerprints the audio, not just the text: the same words spoken
// by another voice or at another rate must not collide.
func cacheKey(p Profile, rate float64, text string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		p.Engine, p.Name, p.Version, p.Model, p.Voice,
		fmt.Sprint(rate), text,
	}, "|")))
	return hex.EncodeToString(h[:8])
}

// requestRate folds the two request speed fields into one multiplier.
// NormalSpeed is the base rate (0 behaves as 1.0); SpeedUp is a percent
// boost on top of it, bounded like the original socket contract.
func requestRate(req Request) (float64, error) {
	if req.SpeedUp < 0 || req.SpeedUp > maxSpeedUpPercent {
		return 0, fmt.Errorf("voiceout: speed-up must be between 0 and %.0f percent", maxSpeedUpPercent)
	}
	if req.NormalSpeed < 0 {
		return 0, errors.New("voiceout: normal speed must not be negative")
	}
	rate := req.NormalSpeed
	if rate == 0 {
		rate = 1
	}
	return rate * (1 + req.SpeedUp/100), nil
}

func (s *Speaker) lexiconPath(p Profile) string {
	lex := expand(p.Lexicon, s.home)
	if lex == "" {
		return ""
	}
	if !filepath.IsAbs(lex) {
		lex = filepath.Join(s.configDir, lex)
	}
	return lex
}

// expand resolves "~/"-prefixes and environment variables in profile paths.
func expand(s, home string) string {
	if strings.HasPrefix(s, "~/") {
		return filepath.Join(home, s[2:])
	}
	return os.ExpandEnv(s)
}

func firstEnv(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
