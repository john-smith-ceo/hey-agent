package daemon

import (
	"context"
	"fmt"
	"os"
	"time"
)

// busyFileMaxAge is the legacy jarvis-voice-drain semantic (SPEC §4): a marker
// older than five minutes is abandoned — its recorder crashed — so it gets
// removed instead of blocking speech forever.
const busyFileMaxAge = 300 * time.Second

// waitForMic holds a phrase while another recorder owns the microphone. The
// busy file is only a marker: fresh → wait and re-check, stale → remove and
// proceed, missing or unreadable → proceed. A broken marker must not mute the
// daemon; a stuck one would create the acoustic loop this check exists to
// prevent, so freshness wins.
func (d *Daemon) waitForMic(ctx context.Context) error {
	path := d.cfg.BusyFile
	if path == "" {
		return nil
	}
	for {
		info, err := os.Stat(path)
		switch {
		case os.IsNotExist(err):
			return nil
		case err != nil:
			fmt.Fprintf(d.cfg.Log, "busy-file check failed, speaking anyway: %v\n", err)
			return nil
		case time.Since(info.ModTime()) > busyFileMaxAge:
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(d.cfg.Log, "remove stale busy file: %v\n", err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.busyPoll):
		}
	}
}
