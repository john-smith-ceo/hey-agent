package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultSocketPath implements SPEC §2: $XDG_RUNTIME_DIR/hey-agent.sock, with
// a per-uid /tmp fallback for systems without a runtime dir. HEY_AGENT_SOCKET
// overrides everything — the daemon flag --socket and the CLI clients share
// this one source of truth.
func DefaultSocketPath() string {
	if path := strings.TrimSpace(os.Getenv("HEY_AGENT_SOCKET")); path != "" {
		return path
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); dir != "" {
		return filepath.Join(dir, "hey-agent.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("hey-agent-%d.sock", os.Getuid()))
}

// DefaultRuntimeConfigPath is where the daemon keeps the JSON file the bridge
// polls between recordings when --runtime-config is not given. Next to the
// tmp-socket, per uid, like the legacy hey-agent-<hash>.json files.
func DefaultRuntimeConfigPath() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("hey-agent-daemon-%d.json", os.Getuid()))
}

// listenSocket claims the socket path. A leftover file means either a live
// owner or a crash: we ping it, refuse to start when a daemon answers, and
// remove the file when nobody is home.
func listenSocket(path string) (net.Listener, error) {
	if _, err := os.Stat(path); err == nil {
		conn, dialErr := net.DialTimeout("unix", path, 500*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("hey-agent daemon already running (socket %s)", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	// SPEC §7: the socket carries commands, so it obeys the uid boundary —
	// only the owner may talk to the daemon.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secure socket %s: %w", path, err)
	}
	return ln, nil
}

// clientTimeout bounds the CLI side of a command. The daemon answers within
// two seconds by contract; the extra second tolerates a slow connect.
const clientTimeout = 3 * time.Second

// Call sends one command line to a running daemon and decodes its answer line
// (the NDJSON protocol of SPEC §2). A dial error means no daemon is there.
func Call(ctx context.Context, socketPath string, req Request) (Reply, error) {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return Reply{}, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(clientTimeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Reply{}, err
	}
	var reply Reply
	if err := json.NewDecoder(conn).Decode(&reply); err != nil {
		return Reply{}, err
	}
	return reply, nil
}
