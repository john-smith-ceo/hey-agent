package target

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// clientsFormat is the list-clients format. One call is enough for the whole
// resolve: tmux expands each client row against that client's session, its
// current window, and the window's active pane, so #{pane_id} here already
// answers "which pane would display-message name for this client". Reading
// activity and pane from one snapshot also beats the spec's literal two
// commands (list-clients, then display-message) — the two values can never
// disagree about timing because they describe the same instant.
//
// Fields are comma-separated. A tab separator is tempting but wrong: tmux
// renders control characters in -F as '_' for clients with no TMUX env —
// exactly the daemon case — so tabs never survive. Commas survive; the only
// user-controlled fields (session and window names) get #{s|,|_|} escapes,
// and client_flags — the one column commas are native to — sits last so
// SplitN lets it absorb its own separators.
const clientsFormat = "#{client_activity},#{client_tty},#{s|,|_|:#{client_session}},#{session_id},#{window_id},#{window_index},#{pane_id},#{s|,|_|:#{window_name}},#{client_flags}"

// clientFieldCount is how many comma-separated fields clientsFormat produces.
const clientFieldCount = 9

// tmuxClient is one parsed list-clients row.
type tmuxClient struct {
	activity    int64 // client_activity, seconds since epoch
	tty         string
	sessionName string
	sessionID   string
	windowID    string
	windowIndex int
	paneID      string
	attached    bool // the "attached" entry of client_flags
	windowName  string
}

// parseClients parses the whole list-clients output. A row that does not fit
// the format is a hard error rather than a skipped line: malformed output
// means the tmux on the other end does not speak the dialect we asked for,
// and quietly ignoring rows could hide the freshest client and let a stale
// one win.
func parseClients(out []byte) ([]tmuxClient, error) {
	var clients []tmuxClient
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		client, err := parseClient(line)
		if err != nil {
			return nil, fmt.Errorf("%w: unparsable list-clients line %q: %v", ErrNoTmux, line, err)
		}
		clients = append(clients, client)
	}
	return clients, nil
}

// parseClient parses one comma-separated list-clients row.
func parseClient(line string) (tmuxClient, error) {
	fields := strings.SplitN(line, ",", clientFieldCount)
	if len(fields) < clientFieldCount {
		return tmuxClient{}, fmt.Errorf("expected %d comma-separated fields, got %d", clientFieldCount, len(fields))
	}
	var c tmuxClient
	var err error
	if c.activity, err = strconv.ParseInt(fields[0], 10, 64); err != nil {
		return tmuxClient{}, fmt.Errorf("client_activity %q is not a unix timestamp: %v", fields[0], err)
	}
	c.tty = fields[1]
	c.sessionName = fields[2]
	c.sessionID = fields[3]
	c.windowID = fields[4]
	if c.windowIndex, err = strconv.Atoi(fields[5]); err != nil {
		return tmuxClient{}, fmt.Errorf("window_index %q is not a number: %v", fields[5], err)
	}
	c.paneID = fields[6]
	c.windowName = fields[7]
	c.attached = hasFlag(fields[8], "attached")
	// tmux always fills these; an empty one means the output is not what we
	// asked for. tty and window name stay unchecked on purpose — a
	// control-mode client can legitimately have no tty, and a window can
	// legitimately have an empty name.
	for _, required := range []struct{ name, value string }{
		{"client_session", c.sessionName},
		{"session_id", c.sessionID},
		{"window_id", c.windowID},
		{"pane_id", c.paneID},
	} {
		if required.value == "" {
			return tmuxClient{}, fmt.Errorf("required field %s is empty", required.name)
		}
	}
	return c, nil
}

// hasFlag reports whether the comma-separated client_flags column contains
// the given flag.
func hasFlag(flags, want string) bool {
	for _, flag := range strings.Split(flags, ",") {
		if flag == want {
			return true
		}
	}
	return false
}

// pick chooses the attached client with the freshest activity. Detached
// clients are filtered first — nobody is watching through them, so they can
// neither win nor count towards ambiguity. Two attached clients whose
// activity differs by no more than grace are treated as tied, because tmux
// measures activity in whole seconds and "same second" is not a real
// ordering. The tie is refused even if the rivals happen to point at the
// same pane: the spec asks for a visible signal on ambiguity, and a lucky
// coincidence today is a silent leak the day the panes differ.
func pick(clients []tmuxClient, grace time.Duration) (tmuxClient, error) {
	freshest := -1
	for i := range clients {
		if !clients[i].attached {
			continue
		}
		if freshest == -1 || clients[i].activity > clients[freshest].activity {
			freshest = i
		}
	}
	if freshest == -1 {
		if len(clients) > 0 {
			return tmuxClient{}, fmt.Errorf("%w: %d client(s), all detached", ErrNoClient, len(clients))
		}
		return tmuxClient{}, fmt.Errorf("%w: server has no clients", ErrNoClient)
	}
	best := clients[freshest]
	var rivals []tmuxClient
	for _, c := range clients {
		if !c.attached {
			continue
		}
		if time.Duration(best.activity-c.activity)*time.Second <= grace {
			rivals = append(rivals, c)
		}
	}
	if len(rivals) > 1 {
		return tmuxClient{}, fmt.Errorf("%w: %s", ErrAmbiguous, describe(rivals))
	}
	return best, nil
}

// describe renders the tied clients for the ambiguity error, so the log line
// itself carries the "visible signal" the spec asks for.
func describe(clients []tmuxClient) string {
	parts := make([]string, len(clients))
	for i, c := range clients {
		parts[i] = fmt.Sprintf("%s (session %s, activity %d)", c.tty, c.sessionName, c.activity)
	}
	return strings.Join(parts, " vs ")
}
