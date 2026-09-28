package voiceout

import (
	"os"
	"path/filepath"
	"testing"
)

func writeProfileFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(full, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, name+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveProfileFallsBackToBuiltin(t *testing.T) {
	s := newTestSpeaker(t)
	p, err := s.resolveProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "openai" || p.Voice != "cedar" {
		t.Fatalf("default profile = %+v, want builtin cedar/openai", p)
	}
}

func TestResolveProfileReadsActiveMarker(t *testing.T) {
	s := newTestSpeaker(t)
	if err := os.WriteFile(filepath.Join(s.configDir, "profile"), []byte("fable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := s.resolveProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Voice != "fable" {
		t.Fatalf("active marker ignored, got voice %q", p.Voice)
	}
}

func TestResolveProfileEnvWinsOverMarker(t *testing.T) {
	s := newTestSpeaker(t)
	if err := os.WriteFile(filepath.Join(s.configDir, "profile"), []byte("fable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEY_AGENT_VOICE_PROFILE", "onyx")
	p, err := s.resolveProfile("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Voice != "onyx" {
		t.Fatalf("env profile ignored, got voice %q", p.Voice)
	}
}

func TestResolveProfileLoadsJSONFromConfigDir(t *testing.T) {
	s := newTestSpeaker(t)
	writeProfileFile(t, s.configDir, "custom", `{"name":"custom","engine":"mock","speed":1.3,"voice":"v9"}`)
	p, err := s.resolveProfile("custom")
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "mock" || p.Speed != 1.3 || p.Voice != "v9" {
		t.Fatalf("json profile not loaded: %+v", p)
	}
	// Normalization: fields the pipeline relies on get their defaults.
	if p.LengthScale != 1 || p.Lexicon != "lexicon.txt" {
		t.Fatalf("profile not normalized: %+v", p)
	}
}

func TestResolveProfileFallsBackToDataDir(t *testing.T) {
	s := newTestSpeaker(t)
	writeProfileFile(t, s.dataDir, "datad", `{"name":"datad","engine":"mock"}`)
	p, err := s.resolveProfile("datad")
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "mock" {
		t.Fatalf("data dir profile not loaded: %+v", p)
	}
}

func TestResolveProfileBareOpenAIVoice(t *testing.T) {
	s := newTestSpeaker(t)
	p, err := s.resolveProfile("nova")
	if err != nil {
		t.Fatal(err)
	}
	if p.Engine != "openai" || p.Voice != "nova" {
		t.Fatalf("bare voice did not resolve to openai profile: %+v", p)
	}
	if p.Instructions != gentleFemaleStyle {
		t.Fatalf("gentle voice got no gentle instructions: %q", p.Instructions)
	}
}

func TestResolveProfileUnknownIsError(t *testing.T) {
	s := newTestSpeaker(t)
	if _, err := s.resolveProfile("no-such-voice"); err == nil {
		t.Fatal("unknown voice must be an error")
	}
}

func TestResolveProfileAcceptsJSONPath(t *testing.T) {
	s := newTestSpeaker(t)
	path := filepath.Join(t.TempDir(), "loose.json")
	if err := os.WriteFile(path, []byte(`{"engine":"mock","voice":"path-voice"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := s.resolveProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Voice != "path-voice" {
		t.Fatalf("json path not loaded: %+v", p)
	}
}

func TestRequestRate(t *testing.T) {
	cases := []struct {
		req  Request
		want float64
		ok   bool
	}{
		{Request{}, 1.0, true},
		{Request{SpeedUp: 25}, 1.25, true},
		{Request{NormalSpeed: 1.2}, 1.2, true},
		{Request{NormalSpeed: 1.2, SpeedUp: 10}, 1.32, true},
		{Request{SpeedUp: 26}, 0, false},
		{Request{SpeedUp: -1}, 0, false},
		{Request{NormalSpeed: -0.5}, 0, false},
	}
	for _, c := range cases {
		got, err := requestRate(c.req)
		if c.ok != (err == nil) {
			t.Fatalf("requestRate(%+v) err=%v, want ok=%v", c.req, err, c.ok)
		}
		if err == nil && (got < c.want-0.001 || got > c.want+0.001) {
			t.Fatalf("requestRate(%+v) = %v, want ~%v", c.req, got, c.want)
		}
	}
}
