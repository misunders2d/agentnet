package main

import (
	"context"
	"strings"
	"testing"
)

func TestDefaultListen(t *testing.T) {
	for _, c := range []struct{ listen, port, want string }{
		{"", "", "127.0.0.1:8443"},  // local run
		{"", "8443", ":8443"},       // container image default
		{"", "31337", ":31337"},     // platform-assigned PORT replaces the image's
		{":9000", "31337", ":9000"}, // an explicit AGENTNET_LISTEN wins
		{"127.0.0.1:1", "", "127.0.0.1:1"},
	} {
		t.Setenv("AGENTNET_LISTEN", c.listen)
		t.Setenv("PORT", c.port)
		if got := defaultListen(); got != c.want {
			t.Errorf("LISTEN=%q PORT=%q: %q, want %q", c.listen, c.port, got, c.want)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"100MiB": 100 << 20, "1GiB": 1 << 30, "512KiB": 512 << 10, "42": 42, "7B": 7} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-1MiB", "1TB", "MiB"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) accepted", bad)
		}
	}
}

// --push-hosts adds push services by host name only, refused before the
// Hub is opened otherwise.
func TestHubServePushHostsChecked(t *testing.T) {
	for _, bad := range []string{"https://push.example.com", "push.example.com:443", "10.0.0.1", "localhost", "push.example.com/x"} {
		err := runHub(context.Background(), []string{"serve", "--data", t.TempDir(), "--push-hosts", "push.example.org," + bad})
		if err == nil || !strings.Contains(err.Error(), "--push-hosts") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
