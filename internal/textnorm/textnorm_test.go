package textnorm

import "testing"

func TestKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  Léa  MARTIN ", "lea martin"},
		{"François", "francois"},
		{"Adresse E-mail", "adresse e-mail"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := Key(tt.in); got != tt.want {
			t.Errorf("Key(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClean(t *testing.T) {
	if got := Clean("  Jean \t Dupont  "); got != "Jean Dupont" {
		t.Errorf("Clean = %q", got)
	}
}
