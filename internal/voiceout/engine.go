package voiceout

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// synthEngine produces the WAV audio for one stage of speech. Engines are
// looked up by Profile.Engine, so tests can register fakes under their own
// names without touching the real backends.
type synthEngine interface {
	Synthesize(ctx context.Context, p Profile, rate float64, text string) ([]byte, error)
}

// engineFor maps a profile's engine name to a synthesizer. An empty engine
// means piper, matching the agent-voice-over default.
func (s *Speaker) engineFor(name string) (synthEngine, error) {
	if name == "" {
		name = "piper"
	}
	engine, ok := s.engines[name]
	if !ok {
		return nil, fmt.Errorf("voiceout: unsupported TTS engine %q", name)
	}
	return engine, nil
}

// piperEngine shells out to the piper binary, one process per stage. The
// original did the same: piper is not resident, so the model reloads for
// every phrase (roughly a third of a second each) — a known trade-off
// recorded in the port inventory.
type piperEngine struct {
	home string
}

func (e piperEngine) Synthesize(ctx context.Context, p Profile, rate float64, text string) ([]byte, error) {
	bin := expand(p.PiperBin, e.home)
	if bin == "" {
		bin = filepath.Join(e.home, ".local/share/piper/engine/piper")
	}
	model := expand(p.Model, e.home)
	if model == "" {
		return nil, errors.New("voiceout: piper profile has no model")
	}
	if rate <= 0 {
		rate = 1
	}
	// Speed-up is expressed by shortening the length scale: rate 1.25 makes
	// the voice talk 25% faster.
	lengthScale := p.LengthScale / rate

	tmp, err := os.CreateTemp("", "voiceout-piper-*.wav")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpName)

	cmd := exec.CommandContext(ctx, bin, "--model", model,
		"--length_scale", fmt.Sprint(lengthScale), "--output_file", tmpName)
	cmd.Stdin = strings.NewReader(text + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("voiceout: piper: %w (%s)", err, truncate(string(out), 300))
	}
	if st, err := os.Stat(tmpName); err != nil || st.Size() <= minWAVSize {
		return nil, errors.New("voiceout: piper produced no audio")
	}

	filtered := tmpName
	if p.Filter != "" {
		filtered, err = applyFilter(ctx, p, tmpName)
		if err != nil {
			return nil, err
		}
		defer os.Remove(filtered)
	}
	return os.ReadFile(filtered)
}

// applyFilter runs the ffmpeg chain from the profile over raw piper output.
// The {gain} placeholder is replaced with the profile's gain_db. Profiles
// like jarvis-v1 use it for EQ/compression/limiting; OpenAI profiles leave
// Filter empty and never reach this code.
func applyFilter(ctx context.Context, p Profile, raw string) (string, error) {
	out := raw + ".filtered.wav"
	flt := strings.ReplaceAll(p.Filter, "{gain}", fmt.Sprintf("%.1f", p.GainDB))
	cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-i", raw,
		"-filter_complex", flt, "-map", "[out]", out)
	if outb, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(out)
		return raw, fmt.Errorf("voiceout: ffmpeg: %s", truncate(string(outb), 200))
	}
	return out, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}
