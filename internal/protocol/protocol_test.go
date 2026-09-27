package protocol

import "testing"

func TestNormalizeHubURL(t *testing.T) {
	ok := map[string]string{
		"https://hub.example":     "https://hub.example",
		"https://hub.example/":    "https://hub.example",
		"https://127.0.0.1:8443/": "https://127.0.0.1:8443",
		" https://[::1]:8443 ":    "https://[::1]:8443",
	}
	for in, want := range ok {
		if got, err := NormalizeHubURL(in); err != nil || got != want {
			t.Errorf("NormalizeHubURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"http://hub.example", "https://", "https://hub.example/api", "https://hub.example?x=1",
		"https://hub.example#f", "https://user:pw@hub.example", "https://hub.example?", "hub.example",
	} {
		if got, err := NormalizeHubURL(bad); err == nil {
			t.Errorf("NormalizeHubURL(%q) = %q, want error", bad, got)
		}
	}
}
