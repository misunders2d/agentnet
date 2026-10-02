package client

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type workspaceRealmTransport func(*http.Request) (*http.Response, error)

func (f workspaceRealmTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestWorkspaceRequestGuardConcurrencyReconnectAndOffline(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	realm, err := a.RealmID()
	if err != nil {
		t.Fatal(err)
	}
	offered, offline, calls := realm, false, 0
	a.hub.http.Transport = workspaceRealmTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if offline {
			return nil, errors.New("fixture offline")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"protocol\":1,\"realm_id\":\"" + offered + "\"}")), Request: r}, nil
	})
	guard := a.WorkspaceRequestGuard()
	if err = guard(tctx(t), "/v1/version"); err != nil || calls != 0 {
		t.Fatal("version guard recursion")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for range 12 {
		wg.Go(func() { errs <- guard(tctx(t), "/v1/messages") })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("duplicate concurrent check: %d", calls)
	}
	offline = true
	if err = guard(tctx(t), "/v1/stream?ad=fixture"); err == nil {
		t.Fatal("offline treated as verified")
	}
	offline = false
	if err = guard(tctx(t), "/v1/messages"); err != nil {
		t.Fatalf("transient outage became terminal: %v", err)
	}
	offered = protocol.NewID()
	if err = guard(tctx(t), "/v1/stream"); err == nil {
		t.Fatal("reconnect realm change accepted")
	}
	before := calls
	offered = realm
	if err = guard(tctx(t), "/v1/messages"); err == nil || calls != before {
		t.Fatal("proven mismatch did not stay blocked")
	}
}
func TestWorkspaceRequestGuardLegacyAbsentOnlyWhenUnpinned(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	a.hub.http.Transport = workspaceRealmTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{\"protocol\":1}")), Request: r}, nil
	})
	if err := a.WorkspaceRequestGuard()(tctx(t), "/v1/messages"); !errors.Is(err, ErrRealmUnsupported) {
		t.Fatalf("pinned realm downgrade accepted: %v", err)
	}
	if err := a.store.deleteConfig("realm_id"); err != nil {
		t.Fatal(err)
	}
	if err := a.WorkspaceRequestGuard()(tctx(t), "/v1/messages"); err != nil {
		t.Fatalf("never-pinned old relay refused: %v", err)
	}
}
