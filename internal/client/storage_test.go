package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type storageTransport func(*http.Request) (*http.Response, error)

func (f storageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func localStorageFixture(t *testing.T) (*Agent, map[string]string) {
	t.Helper()
	a := &Agent{home: t.TempDir()}
	files := map[string]string{
		"staging/upload-a": "abc", "staging/upload-empty": "",
		"spool/id.age": strings.Repeat("s", 7), "kept/hash.age": strings.Repeat("k", 11),
		"downloads/id.age": strings.Repeat("d", 13), "downloads/id.direct": strings.Repeat("p", 5),
		"opened/synthetic-file": strings.Repeat("o", 17),
		"identity.json":         strings.Repeat("i", 100), "agent.db": strings.Repeat("b", 200),
		"user-saved-files/user-saved": strings.Repeat("u", 999), "staging/nested/not-managed": strings.Repeat("n", 888),
	}
	for name, data := range files {
		path := filepath.Join(a.home, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return a, files
}

func TestStorageLocalManagedCountsWithoutMutation(t *testing.T) {
	a, files := localStorageFixture(t)
	view := a.localStorage()
	if !view.Complete || view.Known != (StorageAmount{Files: 7, Bytes: 56}) || len(view.Areas) != 5 {
		t.Fatalf("managed byte counts wrong: %+v", view)
	}
	for _, area := range view.Areas {
		if area.Status != "available" || area.Usage == nil || area.Lifetime == "" || area.Kind == "" {
			t.Fatalf("missing known area/policy: %+v", area)
		}
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{a.home, "synthetic-file", "id.age", "hash.age", "user-saved"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("summary contains filename/path: %q", secret)
		}
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(a.home, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("inspection mutated %s: %v", name, err)
		}
	}
	empty := (&Agent{home: t.TempDir()}).localStorage()
	if !empty.Complete || empty.Known != (StorageAmount{}) {
		t.Fatalf("absent managed folders are not known zero: %+v", empty)
	}
}

func TestStorageLocalRejectsSymlinksAndReportsFailures(t *testing.T) {
	a, _ := localStorageFixture(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private"), []byte(strings.Repeat("x", 777)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Run("linked file", func(t *testing.T) {
		link := filepath.Join(a.home, "staging", "outside-link")
		if err := os.Symlink(filepath.Join(outside, "private"), link); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		view := a.localStorage()
		if !view.Complete || view.Known != (StorageAmount{Files: 7, Bytes: 56}) {
			t.Fatalf("followed linked file or nested folder: %+v", view)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatal("inspection removed symlink")
		}
	})
	t.Run("linked directory", func(t *testing.T) {
		home := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(home, "kept")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		view := (&Agent{home: home}).localStorage()
		if view.Complete || view.Areas[2].Status != "unavailable" || view.Areas[2].Usage != nil || view.Areas[2].Reason == "" {
			t.Fatalf("symlink directory fabricated zero: %+v", view)
		}
	})
	t.Run("unreadable area", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, "downloads"), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		view := (&Agent{home: home}).localStorage()
		if view.Complete || view.Areas[3].Usage != nil || view.Areas[3].Status != "unavailable" || view.Areas[3].Reason == "" {
			t.Fatalf("failed directory fabricated zero: %+v", view)
		}
	})
	t.Run("missing home", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "missing")
		view := (&Agent{home: home}).localStorage()
		if view.Complete {
			t.Fatalf("unavailable home reported complete: %+v", view)
		}
		for _, area := range view.Areas {
			if area.Usage != nil || area.Reason == "" {
				t.Fatal("unavailable home fabricated per-area zero")
			}
		}
		if _, err := os.Stat(home); !os.IsNotExist(err) {
			t.Fatal("inspection created home")
		}
	})
}

func TestStorageRemoteUnknownKeepsLocalAndSanitizes(t *testing.T) {
	available := protocol.HubStorage{Scope: "caller-owned-ciphertext", QuotaScope: "hub-global", QuotaBytes: 500, MaxFileBytes: 100, UploadIdleTTLSeconds: 60,
		Own: protocol.BlobStorageUsage{Stored: protocol.StorageBucket{Files: 1, ReservedBytes: 5, RecordedReceivedBytes: 5}}}
	data, err := json.Marshal(available)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, status string
		code         int
		body         string
		err          error
	}{
		{"reported", "available", 200, string(data), nil},
		{"older", "unsupported", 404, `{"error":"older"}`, nil},
		{"failed", "unavailable", 500, `{"error":"/private/operator token=synthetic-secret"}`, nil},
		{"offline", "unavailable", 0, "", errors.New("/private/operator token=synthetic-secret")},
		{"unrecognized", "unavailable", 200, `{}`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := localStorageFixture(t)
			a.hub = &hubConn{base: "https://synthetic.invalid", timeout: time.Second, http: &http.Client{Transport: storageTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/storage" || r.URL.RawQuery != "" {
					t.Fatalf("storage made a mutating/unscoped request: %s %s", r.Method, r.URL)
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}}
			view, err := a.Storage(context.Background())
			if err != nil || view.Local.Known != (StorageAmount{Files: 7, Bytes: 56}) || view.Remote.Status != tc.status {
				t.Fatalf("remote hid local/unknown state: %+v %v", view, err)
			}
			if tc.status == "available" {
				if !reflect.DeepEqual(view.Remote.Usage, &available) {
					t.Fatalf("remote counts changed: %+v", view.Remote)
				}
			} else if view.Remote.Usage != nil || view.Remote.Reason == "" || strings.Contains(view.Remote.Reason, "synthetic-secret") || strings.Contains(view.Remote.Reason, "/private") {
				t.Fatalf("remote unknown fabricated counts or leaked raw error: %+v", view.Remote)
			}
		})
	}
}

func TestStorageLeavesInboxStateUntouched(t *testing.T) {
	w := newWorld(t, "")
	in := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindTask, Body: "synthetic storage task"}
	if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	before, err := w.bob.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // remote unverifiable, local view must still return
	view, err := w.bob.Storage(ctx)
	if err != nil || view.Remote.Status != "unavailable" {
		t.Fatalf("canceled remote obscured local: %+v %v", view, err)
	}
	after, err := w.bob.Inbox(false, false)
	if err != nil || !reflect.DeepEqual(before, after) || len(after) != 1 || after[0].Read || after[0].State != stateAwaiting {
		t.Fatalf("storage mutated inbox/read/execution state: %+v %v", after, err)
	}
}
