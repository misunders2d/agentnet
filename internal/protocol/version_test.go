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

// A client is current for a release when it runs that release or a newer
// one, or a development build made from one; nothing else is, and there is
// no current client for a latest that is not a release.
func TestCurrent(t *testing.T) {
	for _, tt := range []struct {
		running, latest string
		want            bool
	}{
		{"v0.8.17", "v0.8.17", true},
		{"v0.8.18", "v0.8.17", true},
		{"v0.9.0", "v0.8.17", true},
		{"v0.8.17-3-gabcdef1", "v0.8.17", true},
		{"v0.8.17+0760ccc", "v0.8.17", true},
		{"v0.8.17-dirty", "v0.8.17", true},
		{"v0.8.16", "v0.8.17", false},
		{"v0.8.16-3-gabcdef1-dirty", "v0.8.17", false},
		{"", "v0.8.17", false},
		{"dev", "v0.8.17", false},
		{"v0.8.17-rc1", "v0.8.17", false},
		{"v0.8.17", "dev", false},
		{"v0.8.17", "", false},
	} {
		if got := Current(tt.running, tt.latest); got != tt.want {
			t.Errorf("Current(%q, %q) = %v, want %v", tt.running, tt.latest, got, tt.want)
		}
	}
	if !IsRelease("v0.8.17") || IsRelease("v0.8.17-3-gabcdef1") || IsRelease("dev") {
		t.Error("IsRelease")
	}
}
