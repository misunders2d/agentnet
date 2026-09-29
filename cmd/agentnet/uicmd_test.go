package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestUIRefusesWhatItShouldAndTouchesNoHome(t *testing.T) {
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
	if err := runUI(context.Background(), home, nil, io.Discard); err == nil || !strings.Contains(err.Error(), "daemon --ui") {
		t.Fatalf("without a running page: %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("ui created the home (%v)", err)
	}
	if _, ok := topics["ui"]; !ok {
		t.Fatal("no help topic for ui")
	}
}

// The daemon's page address, token included, goes only to an owner-only
// file that agentnet ui prints; the log gets the address without it, and
// stopping removes the file.
func TestDaemonUIAddressStaysOutOfTheLog(t *testing.T) {
	home := t.TempDir()
	var log bytes.Buffer
	logf := func(f string, v ...any) { fmt.Fprintf(&log, f+"\n", v...) }
	if _, err := startDaemonUI(nil, home, "0.0.0.0:0", logf); err == nil {
		t.Fatal("served on a non-loopback address")
	}
	stop, err := startDaemonUI(nil, home, "127.0.0.1:0", logf)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, uiURLFile)
	data, err := secfile.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	url := strings.TrimSpace(string(data))
	token := url[strings.Index(url, "?t=")+3:]
	if len(token) < 32 || strings.Contains(log.String(), token) || !strings.Contains(log.String(), "agentnet ui") {
		t.Fatalf("log %q (token %d chars)", log.String(), len(token))
	}
	if fi, _ := os.Stat(path); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// agentnet ui offers the address only while a daemon holds the home.
	var out bytes.Buffer
	if err := runUI(context.Background(), home, nil, &out); err == nil || !strings.Contains(err.Error(), "daemon --ui") || out.Len() != 0 {
		t.Fatalf("address offered with no daemon running: %q %v", out.String(), err)
	}
	release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := runUI(context.Background(), home, nil, &out); err != nil || out.String() != "Open: "+url+"\n" {
		t.Fatalf("ui printed %q: %v", out.String(), err)
	}
	// The token opens the page; the address alone does not.
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(strings.Split(url, "?")[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without token: %d", resp.StatusCode)
	}
	resp, err = cl.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("with token: %d", resp.StatusCode)
	}
	stop()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("address file left behind (%v)", err)
	}
}

func TestUIDemoServesAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- runUI(ctx, "", []string{"--demo"}, pw); pw.Close() }()
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

// A DM alert's click opens the page on that conversation without the
// page's token (a command line is readable by other local users); Linux
// only, and nothing without a page.
func TestConvPageCommand(t *testing.T) {
	home := t.TempDir()
	conv := strings.Repeat("ab", 32)
	if argv := convPageCommand(home, conv); argv != nil {
		t.Fatalf("a command with no page: %v", argv)
	}
	if err := secfile.Write(filepath.Join(home, uiURLFile), []byte("http://127.0.0.1:4567/?t=SECRETTOKEN\n")); err != nil {
		t.Fatal(err)
	}
	argv := convPageCommand(home, conv)
	if runtime.GOOS != "linux" {
		if argv != nil {
			t.Fatalf("a click command on %s: %v", runtime.GOOS, argv)
		}
		return
	}
	if len(argv) != 2 || argv[0] != "xdg-open" || argv[1] != "http://127.0.0.1:4567/#conv="+conv || strings.Contains(strings.Join(argv, " "), "SECRET") {
		t.Fatalf("click command: %v", argv)
	}
	if argv := convPageCommand(home, "not-a-conversation"); len(argv) != 2 || argv[1] != "http://127.0.0.1:4567/" {
		t.Fatalf("page command: %v", argv)
	}
}
