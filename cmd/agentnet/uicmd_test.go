package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIRefusesWithoutDemoAndTouchesNoHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "no-home")
	for _, args := range [][]string{
		{"--home", home, "ui"},
		{"--home", home, "ui", "--listen", "127.0.0.1:0"},
		{"--home", home, "ui", "--demo", "--listen", "0.0.0.0:0"},
		{"--home", home, "ui", "--demo", "--listen", "192.168.1.5:0"},
		{"--home", home, "ui", "--demo", "extra"},
	} {
		if err := run(args); err == nil {
			t.Errorf("%v: accepted", args)
		}
	}
	if err := runUI(context.Background(), nil, io.Discard); err == nil || !strings.Contains(err.Error(), "--demo") {
		t.Fatalf("without --demo: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("ui created the home (%v)", err)
	}
	if _, ok := topics["ui"]; !ok {
		t.Fatal("no help topic for ui")
	}
}

func TestUIDemoServesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- runUI(ctx, []string{"--demo"}, pw); pw.Close() }()
	buf := make([]byte, 4096)
	var out string
	for !strings.Contains(out, "Stop with") {
		n, err := pr.Read(buf)
		if err != nil {
			t.Fatalf("output %q: %v", out, err)
		}
		out += string(buf[:n])
	}
	if !strings.Contains(out, "Open: http://127.0.0.1:") || !strings.Contains(out, "/?t=") {
		t.Fatalf("output %q", out)
	}
	cancel()
	go io.Copy(io.Discard, pr)
	if err := <-done; err != nil {
		t.Fatalf("stop: %v", err)
	}
}
