package core

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui"
)

// Existing TLS sockets become silent; newly accepted sockets remain usable.
// Closing a retired socket unblocks the fault without a timer or mock Run.
type mobileRouteConn struct {
	net.Conn
	broken atomic.Bool
	closed chan struct{}
	once   sync.Once
}

func (c *mobileRouteConn) Read(p []byte) (int, error) {
	for {
		n, err := c.Conn.Read(p)
		if !c.broken.Load() || err != nil {
			return n, err
		}
		// Drop old-route bytes, but still observe the client's actual close.
	}
}
func (c *mobileRouteConn) Write(p []byte) (int, error) {
	if c.broken.Load() {
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(p)
}
func (c *mobileRouteConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type mobileRouteListener struct {
	net.Listener
	mu    sync.Mutex
	conns []*mobileRouteConn
}

func (l *mobileRouteListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	x := &mobileRouteConn{Conn: c, closed: make(chan struct{})}
	l.mu.Lock()
	l.conns = append(l.conns, x)
	l.mu.Unlock()
	return x, nil
}
func (l *mobileRouteListener) breakExisting() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.broken.Store(true)
	}
	return len(l.conns)
}
func (l *mobileRouteListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conns)
}

// TestAppNetworkRecoveryPreservesQueuedMessageAndReceive: a message the
// stopped phone queued in its DM goes once after network recovery, and an
// older device's message still arrives (MEL-713: a device conversation takes
// no new turn from this app; a DM needs both persons and a connected peer).
func TestAppNetworkRecoveryPreservesQueuedMessageAndReceive(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	route := &mobileRouteListener{Listener: ln}
	home := t.TempDir()
	relay, err := hub.Open(hub.Config{DataDir: home, PublicURL: "https://" + ln.Addr().String(), AdminLabel: "owner", Version: "synthetic-test", Logf: func(string, ...any) {}})
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = relay.Serve(ctx, route) }()
	defer func() { cancel(); <-done; relay.Close() }()
	code, err := os.ReadFile(filepath.Join(home, hub.BootstrapFile))
	if err != nil {
		t.Fatal(err)
	}
	phoneHome := t.TempDir()
	enrolled, err := client.Join(ctx, phoneHome, strings.TrimSpace(string(code)), "phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = enrolled.CreatePerson(ctx, "Phone"); err != nil {
		t.Fatal(err)
	}
	invite, err := enrolled.Invite(ctx, "peer", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	address, key := enrolled.Address, enrolled.Self().Fingerprint()
	enrolled.Close()
	peer, err := client.Join(ctx, t.TempDir(), invite, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err = peer.CreatePerson(ctx, "Peer"); err != nil {
		t.Fatal(err)
	}
	peerCtx, stopPeer := context.WithCancel(ctx)
	peerDone := make(chan struct{})
	go func() { defer close(peerDone); _ = peer.Run(peerCtx, client.RunOptions{HumanOnly: true}) }()
	defer func() { stopPeer(); <-peerDone }()
	app, err := OpenApp(phoneHome, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	url := app.URL()
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "real phone and peer streams", func() bool { return app.session.a.MemberView().Current && peer.MemberView().Current })
	var conv string
	waitMobile(t, "peer conversation capabilities", func() bool {
		var createErr error
		conv, createErr = app.session.a.CreateDM(ctx, peer.Address)
		if createErr != nil && !strings.HasPrefix(createErr.Error(), client.WaitPeerUpdate) {
			t.Fatal(createErr)
		}
		return createErr == nil
	})
	const original = "Original queued mobile message"
	// rows are agent's copies of the original message in the DM.
	rows := func(agent *client.Agent) []client.ConvMessage {
		all, err := agent.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		var found []client.ConvMessage
		for _, m := range all {
			if m.Body == original {
				found = append(found, m)
			}
		}
		return found
	}
	app.Stop()
	id := protocol.NewID()
	draft, _ := json.Marshal(ui.DMDraft{ID: id, Conv: conv, Body: original})
	raw, err := app.session.SendDMJSON(string(draft))
	if err != nil {
		t.Fatal(err)
	}
	var queued ui.Sent
	if err = json.Unmarshal([]byte(raw), &queued); err != nil || queued.LID != id || queued.State != "queued" {
		t.Fatal("original message was not durably queued", raw, err)
	}
	old := route.breakExisting()
	resumeStarted := time.Now()
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	peer.NetworkChanged()
	waitMobile(t, "new TLS route after stopped resume", func() bool {
		return route.count() > old && app.session.a.MemberView().Current && peer.MemberView().Current
	})
	waitMobile(t, "original message at peer", func() bool { return len(rows(peer)) == 1 })
	t.Logf("stopped resume to original message stored: %s", time.Since(resumeStarted))
	// Break the active push route too, then deliver in both directions using
	// the public App lifecycle hook rather than replacing Session or its Run.
	app.session.mu.Lock()
	run := app.session.done
	app.session.mu.Unlock()
	old = route.breakExisting()
	peer.NetworkChanged()
	incoming, err := peer.Send(client.WithQueuedSend(ctx, protocol.NewID()), address, "Incoming after active handover", "")
	if err != nil {
		t.Fatal(err)
	}
	handoverStarted := time.Now()
	app.NetworkChanged()
	waitMobile(t, "incoming stored through fresh phone stream", func() bool {
		messages, err := app.session.a.Inbox(false, false)
		for _, m := range messages {
			if m.ID == incoming.ID && m.Body == "Incoming after active handover" {
				return err == nil
			}
		}
		return false
	})
	t.Logf("active network change to incoming stored: %s", time.Since(handoverStarted))
	waitMobile(t, "proven incoming delivery", func() bool {
		thread, err := ui.NewLive(peer).Thread(incoming.ID)
		return err == nil && len(thread.Messages) == 1 && thread.Messages[0].State == "delivered"
	})
	t.Logf("active network change to proven incoming delivery: %s", time.Since(handoverStarted))
	app.session.mu.Lock()
	sameRun := app.session.done == run
	app.session.mu.Unlock()
	if !sameRun || route.count() <= old || app.URL() != url || app.session.a.Address != address || app.session.a.Self().Fingerprint() != key {
		t.Fatal("network recovery replaced the run, origin or identity")
	}
	// Repeated lifecycle wakes drain existing storage, not another UI send.
	// Networking grants no permission.
	app.NetworkChanged()
	if err = app.Start(); err != nil {
		t.Fatal(err)
	}
	waitMobile(t, "original message proven delivered once", func() bool {
		sent := rows(app.session.a)
		return len(sent) == 1 && sent[0].LID == id && sent[0].State == "delivered"
	})
	if got := rows(peer); len(got) != 1 || got[0].LID != id {
		t.Fatal("lifecycle wake duplicated the original message", got)
	}
	if agents, err := app.session.a.LocalAgents(); err != nil || len(agents) != 0 {
		t.Fatal("recovery introduced a phone executor", err)
	}
}
