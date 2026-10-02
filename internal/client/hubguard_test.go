package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// Open installs the workspace guard on the Hub connection: once the Hub
// offers a different workspace identity, a signed request is refused before
// it is built or sent, while the version probe itself still goes through.
func TestOpenInstallsWorkspaceRequestGuard(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	if a.hub.workspaceCheck == nil {
		t.Fatal("Open did not install the workspace guard")
	}
	pinned, err := a.RealmID()
	if err != nil || pinned == "" {
		t.Fatalf("pinned realm: %q %v", pinned, err)
	}
	var paths []string
	a.hub.http.Transport = workspaceRealmTransport(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"protocol":1,"realm_id":"00000000000000000000000000000001"}`)), Request: r}, nil
	})
	var out any
	err = a.hub.do(tctx(t), http.MethodGet, "/v1/messages", nil, &out)
	var changed *RealmChangedError
	if !errors.As(err, &changed) || changed.Pinned != pinned {
		t.Fatalf("changed realm not refused: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/v1/version" {
		t.Fatalf("only the version probe may leave: %v", paths)
	}
	if err = a.hub.do(tctx(t), http.MethodGet, "/v1/messages", nil, &out); !errors.As(err, &changed) || len(paths) != 1 {
		t.Fatalf("a proven mismatch must stay blocked without another probe: %v %v", err, paths)
	}
	if err = a.hub.do(tctx(t), http.MethodGet, "/v1/version", nil, &out); err != nil || len(paths) != 2 {
		t.Fatalf("version probe must bypass the guard: %v %v", err, paths)
	}
	if got, err := a.RealmID(); err != nil || got != pinned {
		t.Fatalf("pin changed: %q %v", got, err)
	}
}
