package ui

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

type imageReference struct {
	dir, id string
	index   int
}

type imageOpenFixture struct {
	*pictureFixture
	opened []imageReference
}

func (f *imageOpenFixture) OpenImage(_ context.Context, dir, id string, index int) error {
	f.lock.Lock()
	defer f.lock.Unlock()
	f.opened = append(f.opened, imageReference{dir, id, index})
	return nil
}

func TestOpenImageRoute(t *testing.T) {
	f := &imageOpenFixture{pictureFixture: newPictureFixture()}
	const host = "127.0.0.1:8123"
	s := New(f, host, testToken)
	h := s.Handler()
	for _, tc := range []struct {
		body, origin string
		auth         bool
		status       int
	}{
		{`{"id":"m","dir":"in","index":5}`, "http://" + host, false, 401},
		{`{"id":"m","dir":"in","index":5}`, "http://foreign.test", true, 403},
		{`{"id":"m","dir":"in"}`, "http://" + host, true, 404},
		{`{"id":"m","dir":"in","index":-1}`, "http://" + host, true, 404},
		{`{"id":"m","index":5}`, "http://" + host, true, 404},
		{`{"id":"m","dir":"sideways","index":5}`, "http://" + host, true, 404},
		{`{"id":"","dir":"in","index":5}`, "http://" + host, true, 404},
		{`{"id":"m","dir":"in","index":5,"path":"/etc/passwd"}`, "http://" + host, true, 400},
		{`{"id":"m","dir":"in","index":5}`, "http://" + host, true, 200},
		{`{"id":"m","dir":"out","index":2}`, "http://" + host, true, 200},
	} {
		r := httptest.NewRequest("POST", "http://"+host+"/api/file/open-image", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", tc.origin)
		if tc.auth {
			r.Header.Set("Cookie", cookieName+"="+testToken)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d %s, want %d", tc.body, w.Code, w.Body, tc.status)
		}
	}
	if !reflect.DeepEqual(f.opened, []imageReference{{"in", "m", 5}, {"out", "m", 2}}) {
		t.Fatal("wrong references or refused request launched an app", f.opened)
	}
	s.SetMobile()
	r := httptest.NewRequest("POST", "http://"+host+"/api/file/open-image", strings.NewReader(`{"id":"m","dir":"in","index":5}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://"+host)
	r.Header.Set("Cookie", cookieName+"="+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || len(f.opened) != 2 {
		t.Fatal("Android must not launch a desktop app", w.Code, f.opened)
	}
}

func TestImageAppCommand(t *testing.T) {
	t.Setenv("SystemRoot", `C:\Windows`)
	path := filepath.Join(t.TempDir(), "image with spaces.png")
	for _, tc := range []struct {
		goos string
		args []string
	}{
		{"linux", []string{"xdg-open", path}},
		{"darwin", []string{"open", path}},
		{"windows", []string{filepath.Join(`C:\Windows`, "System32", "rundll32.exe"), "url.dll,FileProtocolHandler", path}},
	} {
		if got := imageAppCommand(tc.goos, path); !reflect.DeepEqual(got, tc.args) {
			t.Fatalf("%s: %q", tc.goos, got)
		}
	}
}

// Real verified incoming/outgoing file reads, with an OS opener recorder.
// No desktop app is actually launched.
func TestLiveOpenImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable opener recorder; Windows argument selection tested separately")
	}
	alice, _, live := liveWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sample := newPictureFixture().sample
	dir := t.TempDir()
	var paths []string
	for i, data := range [][]byte{sample, []byte(`<svg onload="alert(1)"></svg>`)} {
		path := filepath.Join(dir, []string{"original.png", "fake.png"}[i])
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	sent, err := alice.SendMessage(ctx, client.Outgoing{To: live.a.Address, Files: paths, Wait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "opened-path")
	t.Setenv("AGENTNET_IMAGE_OPEN_RECORD", record)
	t.Setenv("PATH", dir)
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if err := os.WriteFile(filepath.Join(dir, opener), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AGENTNET_IMAGE_OPEN_RECORD\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		live *Live
		dir  string
	}{{live, "in"}, {NewLive(alice), "out"}} {
		if err := tc.live.OpenImage(ctx, tc.dir, sent.ID, 0); err != nil {
			t.Fatal(tc.dir, err)
		}
		var opened string
		for until := time.Now().Add(5 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			// The shell creates the file before writing the path.
			if b, err := os.ReadFile(record); err == nil && bytes.HasSuffix(b, []byte{'\n'}) {
				opened = strings.TrimSpace(string(b))
				break
			}
		}
		if opened == "" || filepath.Ext(opened) != ".png" {
			t.Fatalf("opener path %q", opened)
		}
		data, err := os.ReadFile(opened)
		if err != nil || !bytes.Equal(data, sample) {
			t.Fatalf("%s original bytes: %v", tc.dir, err)
		}
		info, err := os.Stat(opened)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("image is not private", info, err)
		}
		if err := tc.live.OpenImage(ctx, tc.dir, sent.ID, 1); err == nil {
			t.Fatal("SVG renamed .png launched an app")
		}
		if err := tc.live.OpenImage(ctx, tc.dir, sent.ID, 7); err == nil {
			t.Fatal("missing attachment launched an app")
		}
		if err := tc.live.a.CleanStaging(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(opened); !os.IsNotExist(err) {
			t.Fatal("restart cleanup left plaintext", err)
		}
		os.Remove(record)
	}
}
