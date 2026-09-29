package target

import (
	"errors"
	"testing"
	"time"
)

func TestParseClients(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantLen int
		wantErr error
	}{
		{
			name:    "empty output",
			output:  "",
			wantLen: 0,
		},
		{
			name:    "blank lines are ignored",
			output:  "\n\n",
			wantLen: 0,
		},
		{
			name: "two rows",
			output: standLine(100, "/dev/pts/1", "%5") + "\n" +
				standLine(200, "/dev/pts/3", "%7") + "\n",
			wantLen: 2,
		},
		{
			name:    "missing trailing newline still parses",
			output:  standLine(100, "/dev/pts/1", "%5"),
			wantLen: 1,
		},
		{
			name:    "garbage line",
			output:  "gibberish\n",
			wantErr: ErrNoTmux,
		},
		{
			name:    "too few fields",
			output:  "100,/dev/pts/1,5,$5\n",
			wantErr: ErrNoTmux,
		},
		{
			name:    "non-numeric activity",
			output:  "soon,/dev/pts/1,5,$5,@1,1,%7,w,attached\n",
			wantErr: ErrNoTmux,
		},
		{
			name:    "non-numeric window index",
			output:  "100,/dev/pts/1,5,$5,@1,one,%7,w,attached\n",
			wantErr: ErrNoTmux,
		},
		{
			name:    "empty pane id",
			output:  "100,/dev/pts/1,5,$5,@1,1,,w,attached\n",
			wantErr: ErrNoTmux,
		},
		{
			name:    "empty session id",
			output:  "100,/dev/pts/1,5,,@1,1,%7,w,attached\n",
			wantErr: ErrNoTmux,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClients([]byte(tc.output))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tc.wantLen {
				t.Fatalf("expected %d clients, got %d", tc.wantLen, len(got))
			}
		})
	}
}

func TestParseClientFields(t *testing.T) {
	got, err := parseClient(standLine(1790580356, "/dev/pts/1", "%7"))
	if err != nil {
		t.Fatal(err)
	}
	want := tmuxClient{
		activity:    1790580356,
		tty:         "/dev/pts/1",
		sessionName: "5",
		sessionID:   "$5",
		windowID:    "@1",
		windowIndex: 1,
		paneID:      "%7",
		attached:    true,
		windowName:  "codex",
	}
	if got != want {
		t.Fatalf("expected %+v, got %+v", want, got)
	}
}

func TestParseClientAttachment(t *testing.T) {
	tests := []struct {
		name   string
		flags  string
		attach bool
	}{
		{"attached flag", "attached,UTF-8", true},
		{"attached among others", "control,attached", true},
		{"empty flags", "", false},
		{"no attached flag", "UTF-8,readonly", false},
		{"prefix does not count", "unattached", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseClient(clientLine(100, "/dev/pts/1", "5", "$5", "@1", 1, "%7", "w", tc.flags))
			if err != nil {
				t.Fatal(err)
			}
			if got.attached != tc.attach {
				t.Fatalf("flags %q: expected attached=%v, got %v", tc.flags, tc.attach, got.attached)
			}
		})
	}
}

func TestParseClientFlagsWithCommas(t *testing.T) {
	// client_flags is the last field; SplitN lets it keep the commas it is
	// made of instead of breaking the row.
	got, err := parseClient("100,/dev/pts/1,5,$5,@1,1,%7,devin,attached,focused,UTF-8")
	if err != nil {
		t.Fatal(err)
	}
	if got.windowName != "devin" || !got.attached {
		t.Fatalf("expected name=devin attached=true, got %q %v", got.windowName, got.attached)
	}
}

func TestPick(t *testing.T) {
	attached := func(activity int64, pane string) tmuxClient {
		return tmuxClient{activity: activity, tty: "tty", sessionName: "s", sessionID: "$1", windowID: "@1", windowIndex: 1, paneID: pane, attached: true, windowName: "w"}
	}
	detached := func(activity int64, pane string) tmuxClient {
		c := attached(activity, pane)
		c.attached = false
		return c
	}

	tests := []struct {
		name     string
		clients  []tmuxClient
		grace    time.Duration
		wantPane string
		wantErr  error
	}{
		{
			name:    "no clients",
			clients: nil,
			grace:   time.Second,
			wantErr: ErrNoClient,
		},
		{
			name:    "only detached clients",
			clients: []tmuxClient{detached(100, "%5"), detached(200, "%7")},
			grace:   time.Second,
			wantErr: ErrNoClient,
		},
		{
			name:     "single client",
			clients:  []tmuxClient{attached(100, "%7")},
			grace:    time.Second,
			wantPane: "%7",
		},
		{
			name:     "freshest of three",
			clients:  []tmuxClient{attached(100, "%5"), attached(300, "%7"), attached(200, "%9")},
			grace:    time.Second,
			wantPane: "%7",
		},
		{
			name:    "equal activity",
			clients: []tmuxClient{attached(300, "%5"), attached(300, "%7")},
			grace:   time.Second,
			wantErr: ErrAmbiguous,
		},
		{
			name:    "inside grace",
			clients: []tmuxClient{attached(300, "%5"), attached(301, "%7")},
			grace:   time.Second,
			wantErr: ErrAmbiguous,
		},
		{
			name:    "zero grace still ties on equality",
			clients: []tmuxClient{attached(300, "%5"), attached(300, "%7")},
			grace:   0,
			wantErr: ErrAmbiguous,
		},
		{
			name:     "outside grace picks freshest",
			clients:  []tmuxClient{attached(299, "%5"), attached(301, "%7")},
			grace:    time.Second,
			wantPane: "%7",
		},
		{
			// A detached client with a fresher timestamp can neither win
			// nor pull the resolve into ambiguity.
			name:     "detached fresher is ignored",
			clients:  []tmuxClient{detached(999, "%9"), attached(100, "%7")},
			grace:    time.Second,
			wantPane: "%7",
		},
		{
			name:     "detached rival does not create ambiguity",
			clients:  []tmuxClient{attached(100, "%7"), detached(100, "%9")},
			grace:    time.Second,
			wantPane: "%7",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pick(tc.clients, tc.grace)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("expected %v, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.paneID != tc.wantPane {
				t.Fatalf("expected pane %s, got %s", tc.wantPane, got.paneID)
			}
		})
	}
}

func TestLooksLikeNoServer(t *testing.T) {
	tests := []struct {
		stderr string
		want   bool
	}{
		{"error connecting to /tmp/tmux-1000/heydev (No such file or directory)", true},
		{"no server running on /tmp/tmux-1000/default", true},
		{"failed to connect to server", true},
		{"can't find window: 9", false},
		{"exit status 1", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := looksLikeNoServer(tc.stderr); got != tc.want {
			t.Fatalf("looksLikeNoServer(%q): expected %v, got %v", tc.stderr, tc.want, got)
		}
	}
}
