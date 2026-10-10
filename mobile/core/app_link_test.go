package core

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/ui"
)

func waitMobile(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for " + what)
}

// This exercises actual TLS enrollment, signed offer/MAC, pending push and
// original-source approval, with HumanOnly transport on both devices.
func TestAppHumanOnlyLinkApprovalFromOriginalSource(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	hubHome := t.TempDir()
	relay, e := hub.Open(hub.Config{DataDir: hubHome, PublicURL: "https://" + ln.Addr().String(), AdminLabel: "owner", Version: "synthetic-test", Logf: func(string, ...any) {}})
	if e != nil {
		ln.Close()
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = relay.Serve(ctx, ln) }()
	defer func() { cancel(); <-done; relay.Close() }()
	code, e := os.ReadFile(filepath.Join(hubHome, hub.BootstrapFile))
	if e != nil {
		t.Fatal(e)
	}
	sourceHome := t.TempDir()
	source, e := client.Join(ctx, sourceHome, strings.TrimSpace(string(code)), "desktop")
	if e != nil {
		t.Fatal(e)
	}
	defer source.Close()
	if _, e = source.CreatePerson(ctx, "Synthetic owner"); e != nil {
		t.Fatal(e)
	}
	offer, e := source.NewDeviceLink(ctx)
	if e != nil {
		t.Fatal(e)
	}
	phone, e := OpenApp(t.TempDir(), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer phone.Close()
	if e = phone.Start(); e != nil {
		t.Fatal(e)
	}
	if _, e = phone.setup.SetupJoin(ui.SetupJoin{Code: offer.Code}); e != nil {
		t.Fatal(e)
	}
	if state := phone.session.a.LinkState(); state.State != client.LinkPending || state.Approver != source.Address {
		t.Fatal("lost exact pending source", state)
	}
	pending, e := (&phoneProvider{Live: phone.session.live, session: phone.session}).Overview()
	if e != nil || pending.Link == nil || pending.Link.Approver != source.Address || pending.Link.Expires == nil || pending.Link.Expires.Unix() != offer.Expires.Unix() {
		t.Fatal("lost source or expiry in phone projection", pending.Link, e)
	}
	// Source is offline: no other device receives the source-bound offer.
	if requests, e := source.PendingLinks(); e != nil || len(requests) != 0 {
		t.Fatal("pending request appeared without source stream", requests, e)
	}
	sourceCtx, stopSource := context.WithCancel(ctx)
	sourceDone := make(chan struct{})
	go func() { defer close(sourceDone); _ = source.Run(sourceCtx, client.RunOptions{HumanOnly: true}) }()
	defer func() { stopSource(); <-sourceDone }()
	waitMobile(t, "request at original source", func() bool {
		requests, e := source.PendingLinks()
		return e == nil && len(requests) == 1 && requests[0].State == client.LinkPending
	})
	requests, e := source.PendingLinks()
	if e != nil {
		t.Fatal(e)
	}
	if e = source.DecideLink(ctx, requests[0].ID, true); e != nil {
		t.Fatal(e)
	}
	waitMobile(t, "phone linked", func() bool { return phone.session.a.LinkState().State == client.LinkLinked })
	original, e := ui.NewLive(source).Overview()
	if e != nil {
		t.Fatal(e)
	}
	linked, e := phone.session.live.Overview()
	if e != nil {
		t.Fatal(e)
	}
	if original.Person == nil || linked.Person == nil || original.Person.Person != linked.Person.Person {
		t.Fatal("phone did not become same verified person")
	}
	if responders, e := phone.session.a.LocalAgents(); e != nil || len(responders) != 0 {
		t.Fatal("phone created local executors", e)
	}
	assertReceive := func(body string) {
		t.Helper()
		waitMobile(t, "phone live member stream", func() bool { return phone.session.a.MemberView().Current })
		sent, err := source.Send(ctx, phone.session.a.Address, body, "")
		if err != nil {
			t.Fatal(err)
		}
		waitMobile(t, "encrypted message received on phone", func() bool {
			messages, err := phone.session.a.Inbox(false, false)
			for _, message := range messages {
				if message.ID == sent.ID && message.Body == body {
					return err == nil
				}
			}
			return false
		})
		waitMobile(t, "source receives proven delivery receipt", func() bool {
			thread, err := ui.NewLive(source).Thread(sent.ID)
			for _, message := range thread.Messages {
				if message.ID == sent.ID && message.State == "delivered" {
					return err == nil
				}
			}
			return false
		})
		if !phone.session.a.MemberView().Current {
			t.Fatal("phone stream ended after receipt")
		}
	}
	assertReceive("after approval")
	phone.Stop()
	if e := phone.Start(); e != nil {
		t.Fatal(e)
	}
	assertReceive("after foreground restart")
}

func TestPhonePendingLinkShowsTerminalTransportError(t *testing.T) {
	s, home := fixture(t)
	db, e := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = db.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES('link','{"state":"pending","approver":"owner/desktop","expires":1790001000}')`)
	db.Close()
	if e != nil {
		t.Fatal(e)
	}
	s.run = func(context.Context) error { return errors.New("synthetic transport failure") }
	if e = s.Start(); e != nil {
		t.Fatal(e)
	}
	waitMobile(t, "terminal error", func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.lastError != "" })
	p := &phoneProvider{Live: s.live, session: s}
	for _, get := range []func() (ui.Overview, error){p.Overview, p.TopicOverview} {
		view, e := get()
		if e != nil {
			t.Fatal(e)
		}
		if view.Link == nil || view.Link.State != client.LinkPending || !strings.Contains(view.Link.Detail, "synthetic transport failure") {
			t.Fatal("pending error was hidden", view.Link, e)
		}
		if view.Link.Approver != "owner/desktop" || view.Link.Expires == nil || view.Link.Expires.Unix() != 1790001000 {
			t.Fatal("pending source/expiry changed", view.Link)
		}
	}
	if s.a.LinkState().State != client.LinkPending {
		t.Fatal("diagnostic changed enrollment authority")
	}
}

func TestPhoneLinkedTransportFailureInvalidatesPage(t *testing.T) {
	s, _ := fixture(t)
	exit := make(chan struct{})
	s.run = func(ctx context.Context) error {
		select {
		case <-exit:
			return errors.New("synthetic receive-loop failure")
		case <-ctx.Done():
			return nil
		}
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	_, changed := s.a.Changed()
	close(exit)
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("terminal receive failure did not invalidate the local page")
	}
	p := &phoneProvider{Live: s.live, session: s}
	for _, get := range []func() (ui.Overview, error){p.Overview, p.TopicOverview} {
		view, err := get()
		if err != nil || view.TransportError != "synthetic receive-loop failure" {
			t.Fatal("linked transport failure hidden", view.TransportError, err)
		}
		if view.Link != nil {
			t.Fatal("failure invented a device-link request")
		}
	}
	s.run = func(ctx context.Context) error { <-ctx.Done(); return nil }
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	view, err := p.Overview()
	if err != nil || view.TransportError != "" {
		t.Fatal("new receive attempt retained terminal failure", view.TransportError, err)
	}
}
