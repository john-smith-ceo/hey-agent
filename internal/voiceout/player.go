package voiceout

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"
)

// Player plays one cached WAV file to its end. Play must respect ctx:
// returning when ctx is canceled is what makes Hush able to interrupt a
// phrase. Stop force-kills whatever is playing right now and must be safe
// to call with nothing playing.
type Player interface {
	Play(ctx context.Context, wavPath string) error
	Stop()
}

// commandPlayer wraps an external player binary. The default is ffplay;
// HEY_AGENT_PLAYER switches to mpv or to any custom binary, which then gets
// the file as its only argument.
type commandPlayer struct {
	bin  string
	args []string

	mu  sync.Mutex
	cmd *exec.Cmd
}

func newCommandPlayer(bin string) *commandPlayer {
	var args []string
	switch filepath.Base(bin) {
	case "mpv":
		args = []string{"--no-video", "--really-quiet"}
	case "ffplay":
		args = []string{"-nodisp", "-autoexit", "-loglevel", "error"}
	}
	return &commandPlayer{bin: bin, args: args}
}

func (p *commandPlayer) Play(ctx context.Context, wavPath string) error {
	args := append(append([]string{}, p.args...), wavPath)
	cmd := exec.CommandContext(ctx, p.bin, args...)
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.cmd = nil
		p.mu.Unlock()
	}()
	err := cmd.Run()
	// A killed process reports an exit error; when the kill came from
	// cancellation the meaningful result is the ctx error, not the exit code.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// Stop kills the in-flight player process. It is the second half of Hush:
// the context cancel stops the loop, this stops the sound already playing.
func (p *commandPlayer) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}
