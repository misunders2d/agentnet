package protocol

import "testing"

func TestNewer(t *testing.T) {
	for _, tt := range []struct {
		candidate, running string
		want               bool
	}{
		{"v0.2.1", "v0.3.0", false},
		{"v0.3.0", "v0.3.0", false},
		{"v0.3.1", "v0.3.0", true},
		{"v0.10.0", "v0.9.9", true},
		{"v1.0.0", "v0.99.99", true},
		{"v0.2.1", "v0.2.1+0760ccc", false},
		{"v0.3.0", "v0.2.1+0760ccc", true},
		{"v0.2.1", "v0.2.1-28-gcc5d858-dirty", false},
		{"v0.3.0", "v0.2.1-28-gcc5d858-dirty", true},
		{"v0.3.0", "v0.3.0-dirty", false},
		{"v0.3.1", "v0.3.0-dirty", true},
		{"v0.3.1", "dev", false},
		{"dev", "v0.3.0", false},
		{"v0.3.1-rc.1", "v0.3.0", false},
		{"v0.3.1", "v0.3.0-unrecognized", false},
	} {
		if got := Newer(tt.candidate, tt.running); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.candidate, tt.running, got, tt.want)
		}
	}
}
