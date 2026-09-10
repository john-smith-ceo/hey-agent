package record

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Recorder interface {
	Record(ctx context.Context, autoStop bool) (string, error)
}

type FFmpeg struct {
	device  string
	silence time.Duration
}

func NewFFmpeg(device string, silence time.Duration) *FFmpeg {
	return &FFmpeg{device: device, silence: silence}
}

// DefaultDevice returns the platform's ffmpeg input device used when the
// caller does not specify one explicitly.
func DefaultDevice() string { return defaultDevice() }

var silenceStart = regexp.MustCompile(`silence_start:`)

func (r *FFmpeg) Record(ctx context.Context, autoStop bool) (string, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return "", errors.New("ffmpeg not found; install it (macOS: brew install ffmpeg, Debian: apt install ffmpeg)")
	}
	file, err := os.CreateTemp("", "hey-agent-*.wav")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	input, err := inputArgs(r.device)
	if err != nil {
		os.Remove(path)
		return "", err
	}
	args := []string{"-hide_banner", "-loglevel", "info"}
	args = append(args, input...)
	args = append(args, "-ac", "1", "-ar", "16000")
	if autoStop {
		// -25 dBFS резал тихую речь вместе с паузами: у этого микрофона голос
		// проваливается под порог, и запись обрывалась посреди фразы. -35 dBFS
		// держит раздумье внутри записи, а --silence по-прежнему задаёт паузу.
		args = append(args, "-af", fmt.Sprintf("silencedetect=noise=-35dB:d=%0.3f", r.silence.Seconds()))
	}
	args = append(args, "-c:a", "pcm_s16le", "-y", path)
	cmd := exec.Command("ffmpeg", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.Remove(path)
		return "", err
	}
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("start ffmpeg: %w", err)
	}

	var diagnostics []string
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { _ = cmd.Process.Signal(os.Interrupt) }) }
	doneLogs := make(chan struct{})
	go func() {
		defer close(doneLogs)
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			line := s.Text()
			diagnostics = append(diagnostics, line)
			if len(diagnostics) > 12 {
				diagnostics = diagnostics[1:]
			}
			// silencedetect reports silence_end only when it first observes a
			// quiet period. If a speaker starts immediately after enabling the
			// microphone, that event never occurs; stopping on silence_start is
			// therefore the reliable definition of “a full --silence pause”.
			if autoStop && silenceStart.MatchString(line) {
				stop()
			}
		}
	}()
	go func() {
		<-ctx.Done()
		stop()
	}()
	err = cmd.Wait()
	<-doneLogs
	interrupted := err != nil && (strings.Contains(err.Error(), "signal: interrupt") || strings.Contains(strings.Join(diagnostics, "\n"), "Exiting normally, received signal 2."))
	if err != nil && !interrupted {
		os.Remove(path)
		return "", fmt.Errorf("ffmpeg: %w: %s", err, strings.Join(diagnostics, " | "))
	}
	info, statErr := os.Stat(path)
	if statErr != nil || info.Size() < 1024 {
		os.Remove(path)
		return "", errors.New("recording is empty")
	}
	return path, nil
}
