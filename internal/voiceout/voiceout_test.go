package voiceout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeEngine counts synthesized phrases instead of producing real audio.
// Tests register it under an engine name and load a matching profile.
type fakeEngine struct {
	mu    sync.Mutex
	calls int
	texts []string
	rates []float64
	instr []string
	wav   []byte
	err   error
}

func (f *fakeEngine) Synthesize(ctx context.Context, p Profile, rate float64, text string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.texts = append(f.texts, text)
	f.rates = append(f.rates, rate)
	f.instr = append(f.instr, p.Instructions)
	if f.err != nil {
		return nil, f.err
	}
	return f.wav, nil
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakePlayer records played files; with block=true it hangs inside Play
// until the context is canceled or Stop is called — the Hush scenario.
type fakePlayer struct {
	mu        sync.Mutex
	played    []string
	block     bool
	started   chan struct{}
	startOnce sync.Once
	stopCh    chan struct{}
	stopOnce  sync.Once
}

func (p *fakePlayer) Play(ctx context.Context, wavPath string) error {
	p.mu.Lock()
	p.played = append(p.played, wavPath)
	p.mu.Unlock()
	if p.started != nil {
		p.startOnce.Do(func() { close(p.started) })
	}
	if !p.block {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.stopCh:
		return errors.New("stopped")
	}
}

func (p *fakePlayer) Stop() {
	if p.stopCh != nil {
		p.stopOnce.Do(func() { close(p.stopCh) })
	}
}

func (p *fakePlayer) playedCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.played)
}

var fakeWAV = []byte("RIFF" + strings.Repeat("\x00", 60) + "WAVE")

func newTestSpeaker(t *testing.T) *Speaker {
	t.Helper()
	// Isolate the tests from the developer machine's real environment.
	t.Setenv("HEY_AGENT_VOICE_PROFILE", "")
	t.Setenv("HEY_AGENT_PLAYER", "")
	t.Setenv("HEY_AGENT_TTS_BASE_URL", "")
	t.Setenv("HEY_AGENT_ENV_FILE", filepath.Join(t.TempDir(), ".env-absent"))
	s, err := New(Config{ConfigDir: t.TempDir(), DataDir: t.TempDir(), APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mockVoice wires the fake engine under the "mock" profile name.
func mockVoice(t *testing.T, s *Speaker, engine synthEngine) {
	t.Helper()
	s.engines["mock"] = engine
	writeProfileFile(t, s.configDir, "mock", `{"name":"mock","engine":"mock"}`)
}

func TestSpeakSynthesizesAndPlaysStages(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	player := &fakePlayer{}
	mockVoice(t, s, eng)
	s.player = player

	text := "Первая фраза. Вторая фраза."
	if err := s.Speak(context.Background(), Request{Text: text, Voice: "mock"}); err != nil {
		t.Fatal(err)
	}
	if eng.count() != 1 || player.playedCount() != 1 {
		t.Fatalf("synth=%d play=%d, want 1/1", eng.count(), player.playedCount())
	}
	if !strings.Contains(eng.texts[0], "Первая фраза.") {
		t.Fatalf("engine got uncleaned text: %q", eng.texts[0])
	}
}

func TestSpeakCachesSynthesizedAudio(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	mockVoice(t, s, eng)
	s.player = &fakePlayer{}

	req := Request{Text: "Одна и та же фраза.", Voice: "mock"}
	if err := s.Speak(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := s.Speak(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if eng.count() != 1 {
		t.Fatalf("cache miss on repeat: synth calls = %d, want 1", eng.count())
	}
	if err := s.Speak(context.Background(), Request{Text: "Другая фраза.", Voice: "mock"}); err != nil {
		t.Fatal(err)
	}
	if eng.count() != 2 {
		t.Fatalf("new text must synthesize: synth calls = %d, want 2", eng.count())
	}
}

func TestSpeakHushStopsPlaybackAndSkipsStages(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	mockVoice(t, s, eng)
	player := &fakePlayer{block: true, started: make(chan struct{}), stopCh: make(chan struct{})}
	s.player = player

	var parts []string
	for i := 0; i < 40; i++ {
		parts = append(parts, "Короткая фраза номер.")
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Speak(context.Background(), Request{Text: strings.Join(parts, " "), Voice: "mock"})
	}()

	<-player.started // playback of stage one is in flight
	s.Hush()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Speak after Hush = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Speak did not return after Hush")
	}
	if eng.count() != 1 {
		t.Fatalf("stages after Hush were still synthesized: %d calls", eng.count())
	}
	if player.playedCount() != 1 {
		t.Fatalf("played = %d, want 1", player.playedCount())
	}
}

func TestSpeakCallerCancel(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	mockVoice(t, s, eng)
	player := &fakePlayer{block: true, started: make(chan struct{}), stopCh: make(chan struct{})}
	s.player = player

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- s.Speak(ctx, Request{Text: "Фраза.", Voice: "mock"})
	}()
	<-player.started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Speak = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Speak ignored caller cancellation")
	}
}

func TestSpeakRejectsUnknownEngine(t *testing.T) {
	s := newTestSpeaker(t)
	writeProfileFile(t, s.configDir, "bogus", `{"name":"bogus","engine":"waveshark"}`)
	s.player = &fakePlayer{}
	err := s.Speak(context.Background(), Request{Text: "Фраза.", Voice: "bogus"})
	if err == nil || !strings.Contains(err.Error(), "unsupported TTS engine") {
		t.Fatalf("Speak = %v, want unsupported engine error", err)
	}
}

func TestSpeakValidatesSpeedUp(t *testing.T) {
	s := newTestSpeaker(t)
	if err := s.Speak(context.Background(), Request{Text: "Фраза.", SpeedUp: 30}); err == nil {
		t.Fatal("speed-up above 25 must be an error")
	}
}

func TestSpeakEmptyAfterCleanupIsQuietOK(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	mockVoice(t, s, eng)
	s.player = &fakePlayer{}
	// Only a fenced code block: nothing speakable survives cleanup.
	if err := s.Speak(context.Background(), Request{Text: "```\ncode\n```", Voice: "mock"}); err != nil {
		t.Fatal(err)
	}
	if eng.count() != 0 {
		t.Fatalf("empty text was synthesized: %d calls", eng.count())
	}
}

func TestHushWithoutSpeechIsNoop(t *testing.T) {
	s := newTestSpeaker(t)
	s.player = &fakePlayer{stopCh: make(chan struct{})}
	s.Hush()
	s.Hush() // second call must also be safe
}

func TestSpeakPerRequestInstructionsOverride(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	mockVoice(t, s, eng)
	s.player = &fakePlayer{}

	req := Request{Text: "Фраза.", Voice: "mock", Instructions: "whisper", SpeedUp: 25}
	if err := s.Speak(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if eng.count() != 1 {
		t.Fatalf("synth calls = %d", eng.count())
	}
	if eng.instr[0] != "whisper" {
		t.Fatalf("request instructions did not reach the engine: %q", eng.instr[0])
	}
	if eng.rates[0] != 1.25 {
		t.Fatalf("speed-up did not reach the engine: rate %v", eng.rates[0])
	}
}

func TestCommandPlayerArgs(t *testing.T) {
	ff := newCommandPlayer("ffplay")
	got := strings.Join(ff.args, " ")
	if !strings.Contains(got, "-nodisp") || !strings.Contains(got, "-autoexit") || !strings.Contains(got, "-loglevel error") {
		t.Fatalf("ffplay args = %q", got)
	}
	mpv := newCommandPlayer("/usr/bin/mpv")
	if strings.Join(mpv.args, " ") != "--no-video --really-quiet" {
		t.Fatalf("mpv args = %q", mpv.args)
	}
	custom := newCommandPlayer("my-player")
	if len(custom.args) != 0 {
		t.Fatalf("custom player args = %q, want none", custom.args)
	}
	custom.Stop() // nothing playing: must not panic
}

func TestPiperRequiresModel(t *testing.T) {
	engine := piperEngine{home: t.TempDir()}
	_, err := engine.Synthesize(context.Background(), Profile{Engine: "piper"}, 1, "hi")
	if err == nil || !strings.Contains(err.Error(), "no model") {
		t.Fatalf("piper without model = %v, want error", err)
	}
}

func TestLexiconAppliedBeforeSynthesis(t *testing.T) {
	s := newTestSpeaker(t)
	eng := &fakeEngine{wav: fakeWAV}
	mockVoice(t, s, eng)
	s.player = &fakePlayer{}
	if err := os.WriteFile(filepath.Join(s.configDir, "lexicon.txt"), []byte("API = А Пэ И\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Speak(context.Background(), Request{Text: "Через API.", Voice: "mock"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(eng.texts[0], "А Пэ И") {
		t.Fatalf("lexicon not applied before synth: %q", eng.texts[0])
	}
}
