package tmux

import "testing"

func TestIndicatorUsesStateColorAndRestoresTextColor(t *testing.T) {
	tests := []struct {
		state string
		want  string
	}{
		{"idle", "#[fg=#98C379]●#[fg=#315B82] "},
		{"recording", "#[fg=#E06C75]●#[fg=#315B82] "},
		{"transcribing", "#[fg=#E5C07B]●#[fg=#315B82] "},
		{"pasted", "#[fg=#98C379]●#[fg=#315B82] "},
		{"error", "#[fg=#E06C75]●#[fg=#315B82] "},
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			if got := indicator(tt.state); got != tt.want {
				t.Fatalf("indicator(%q) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestSessionTargetDisambiguatesNumericSessionNames(t *testing.T) {
	if got, want := sessionTarget("1"), "1:"; got != want {
		t.Fatalf("tmux session target = %q, want %q", got, want)
	}
}
