// Package itest runs AgentNet end to end: a real TLS Hub and separate client
// homes talking over HTTP and the push stream.
package itest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func join(t *testing.T, code, name string) (*client.Agent, string) {
	t.Helper()
	home := filepath.Join(t.TempDir(), name)
	a, err := client.Join(context.Background(), home, code, name)
	if err != nil {
		t.Fatalf("join %s: %v", name, err)
	}
	t.Cleanup(func() { a.Close() })
	return a, home
}

// daemon is a client daemon running in the background.
type daemon struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when Run returns
	err    error         // Run's result, valid after done is closed
}

func (d *daemon) stop() { d.cancel(); <-d.done }

func runDaemon(t *testing.T, a *client.Agent) *daemon {
	t.Helper()
	a.Logf = t.Logf
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{cancel: cancel, done: make(chan struct{})}
	go func() { d.err = a.Run(ctx); close(d.done) }()
	t.Cleanup(d.stop)
	return d
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func inboxBodies(t *testing.T, a *client.Agent) []string {
	t.Helper()
	msgs, err := a.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range msgs {
		out = append(out, m.Body)
	}
	return out
}

func hasBody(t *testing.T, a *client.Agent, body string) bool {
	for _, b := range inboxBodies(t, a) {
		if b == body {
			return true
		}
	}
	return false
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(path, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestM1Journey(t *testing.T) {
	hubDir := filepath.Join(t.TempDir(), "hub")
	hp := testhub.Start(t, hubDir, "127.0.0.1:0", "")

	// Bootstrap admin, then invite bob.
	adminCode := testhub.BootstrapCode(t, hubDir)
	alice, aliceHome := join(t, adminCode, "alice")
	if _, err := os.Stat(filepath.Join(hubDir, hub.BootstrapFile)); !os.IsNotExist(err) {
		t.Fatal("bootstrap invite file not removed after use")
	}
	if _, err := client.Join(ctx(t), filepath.Join(t.TempDir(), "again"), adminCode, "again"); err == nil {
		t.Fatal("invite reused")
	}
	bobCode, err := alice.Invite(ctx(t), "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, bobHome := join(t, bobCode, "laptop")
	if bob.Address != "bob/laptop" {
		t.Fatalf("bob address %q", bob.Address)
	}
	if _, err := bob.Invite(ctx(t), "eve", time.Hour, false); err == nil {
		t.Fatal("non-admin minted an invite")
	}

	// Live push: bob online, alice sends, bob replies while alice is offline.
	bobD := runDaemon(t, bob)
	secret := "hello bob, secret-body-7f3a"
	res, err := alice.Send(ctx(t), bob.Address, secret, "")
	if err != nil || res.State != "custody" {
		t.Fatalf("send = %+v, %v", res, err)
	}
	waitFor(t, "bob to receive", func() bool { return hasBody(t, bob, secret) })
	waitFor(t, "delivered receipt", func() bool {
		r, err := alice.Status(ctx(t), res.ID)
		return err == nil && r.State == "delivered"
	})
	msgs, _ := bob.Inbox(false, false)
	if _, err := bob.Reply(ctx(t), msgs[0].ID, "hi alice"); err != nil {
		t.Fatal(err)
	}
	aliceD := runDaemon(t, alice)
	waitFor(t, "alice offline catch-up", func() bool { return hasBody(t, alice, "hi alice") })
	if got, _ := alice.Inbox(false, false); got[0].ReplyTo != msgs[0].ID {
		t.Fatalf("reply_to = %q", got[0].ReplyTo)
	}

	// The Hub never stores plaintext.
	for _, f := range []string{"hub.db", "hub.db-wal"} {
		data, _ := os.ReadFile(filepath.Join(hubDir, f))
		if bytes.Contains(data, []byte("secret-body-7f3a")) {
			t.Fatalf("plaintext found in %s", f)
		}
	}

	// Hub restart while alice has a message to send: it queues, then flushes
	// when the daemon reconnects; bob's daemon reconnects too.
	aliceD.stop()
	hp.Stop()
	queued := "sent while hub down"
	res, err = alice.Send(ctx(t), bob.Address, queued, "")
	if err != nil || res.State != "queued" {
		t.Fatalf("send with hub down = %+v, %v", res, err)
	}
	hp = testhub.Start(t, hubDir, hp.Addr, "")
	runDaemon(t, alice)
	waitFor(t, "queued message after restart", func() bool { return hasBody(t, bob, queued) })

	// Duplicate: alice's response was "lost", she retries the same envelope.
	aliceDB := openDB(t, filepath.Join(aliceHome, "agent.db"))
	var dupID string
	if err := aliceDB.QueryRow(`SELECT id FROM outbox WHERE body = ?`, queued).Scan(&dupID); err != nil {
		t.Fatal(err)
	}
	mustExec(t, aliceDB, `UPDATE outbox SET state = 'queued' WHERE id = ?`, dupID)
	if err := alice.FlushOutbox(ctx(t)); err != nil {
		t.Fatal(err)
	}
	// And the Hub pushes it again as if bob's ack was lost.
	bobD.stop()
	hubDB := openDB(t, filepath.Join(hubDir, "hub.db"))
	mustExec(t, hubDB, `UPDATE messages SET state = 'custody' WHERE id = ?`, dupID)
	bobD = runDaemon(t, bob)
	waitFor(t, "redelivered ack", func() bool {
		r, err := alice.Status(ctx(t), dupID)
		return err == nil && r.State == "delivered"
	})
	count := 0
	for _, b := range inboxBodies(t, bob) {
		if b == queued {
			count++
		}
	}
	var hubCopies int
	hubDB.QueryRow(`SELECT count(*) FROM messages WHERE id = ?`, dupID).Scan(&hubCopies)
	if count != 1 || hubCopies != 1 {
		t.Fatalf("duplicate: bob has %d copies, hub has %d", count, hubCopies)
	}

	// A Hub that tampers with stored ciphertext is detected by the recipient.
	bobD.stop()
	res, err = alice.Send(ctx(t), bob.Address, "tamper target", "")
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	hubDB.QueryRow(`SELECT envelope FROM messages WHERE id = ?`, res.ID).Scan(&raw)
	var env map[string]any
	json.Unmarshal(raw, &env)
	ct, _ := base64.StdEncoding.DecodeString(env["ct"].(string))
	ct[len(ct)/2] ^= 1
	env["ct"] = ct
	raw, _ = json.Marshal(env)
	mustExec(t, hubDB, `UPDATE messages SET envelope = ? WHERE id = ?`, raw, res.ID)
	bobD = runDaemon(t, bob)
	bobDB := openDB(t, filepath.Join(bobHome, "agent.db"))
	waitFor(t, "tampered message quarantined", func() bool {
		var n int
		bobDB.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ?`, res.ID).Scan(&n)
		return n == 1
	})
	if hasBody(t, bob, "tamper target") {
		t.Fatal("tampered message reached inbox")
	}
	waitFor(t, "quarantined receipt", func() bool {
		r, err := alice.Status(ctx(t), res.ID)
		return err == nil && r.State == "quarantined"
	})

	// A directory key substitution blocks sending until explicitly trusted.
	carolCode, _ := alice.Invite(ctx(t), "carol", time.Hour, false)
	carol, _ := join(t, carolCode, "desk")
	if _, err := alice.Send(ctx(t), carol.Address, "first contact", ""); err != nil {
		t.Fatal(err)
	}
	fake, _ := identity.Generate()
	fakePub, _ := json.Marshal(fake.Public(carol.Address))
	mustExec(t, hubDB, `UPDATE agents SET public = ? WHERE address = ?`, string(fakePub), carol.Address)
	var kc *client.KeyChangedError
	if _, err := alice.Send(ctx(t), carol.Address, "after swap", ""); !errors.As(err, &kc) {
		t.Fatalf("key change not blocked: %v", err)
	}
	if _, err := alice.Trust(ctx(t), carol.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := alice.Send(ctx(t), carol.Address, "after explicit trust", ""); err != nil {
		t.Fatalf("send after trust: %v", err)
	}

	// Revocation: bob's stream is closed, his daemon stops, and nobody can
	// send to or as bob.
	if err := alice.Revoke(ctx(t), bob.Address); err != nil {
		t.Fatal(err)
	}
	select {
	case <-bobD.done:
		if !errors.Is(bobD.err, client.ErrRevoked) {
			t.Fatalf("bob daemon ended with %v", bobD.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("revoked daemon kept running")
	}
	if _, err := alice.Send(ctx(t), bob.Address, "to revoked", ""); !errors.Is(err, client.ErrPeerRevoked) {
		t.Fatalf("send to revoked: %v", err)
	}
	if _, err := bob.Send(ctx(t), alice.Address, "from revoked", ""); !errors.Is(err, client.ErrRevoked) {
		t.Fatalf("send as revoked: %v", err)
	}
}
