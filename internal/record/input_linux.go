//go:build linux

package record

// PulseAudio is the practical capture path on desktop Linux: it follows the
// system's default input, so a headset plugged in later is picked up without
// reconfiguring hey-agent.
func defaultDevice() string { return "default" }

func inputArgs(device string) ([]string, error) {
	return []string{"-f", "pulse", "-i", device}, nil
}
