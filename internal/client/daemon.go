package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/notify"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const maxBackoff = time.Minute

// Quarantine reasons.
const (
	reasonKeyChanged = "key_changed"
	reasonInvalid    = "invalid"
)

// Run holds the push stream open until ctx ends, reconnecting with jittered
// exponential backoff. It never polls: the Hub pushes messages and sparse
// pings. A message that cannot be processed yet ends the connection; the Hub
// pushes every unacknowledged message again on the next one.
//
// Each Run is one session: a fresh session id announced to the Hub, and,
// with opts.Listen, an HTTPS listener for direct deliveries.
func (a *Agent) Run(ctx context.Context, opts RunOptions) error {
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if errors.Is(err, lockfile.ErrLocked) {
		return fmt.Errorf("another agentnet daemon is already running for %s", a.home)
	}
	if err != nil {
		return err
	}
	defer release()
	stopWorker, err := a.startWorker(ctx)
	if err != nil {
		return err
	}
	defer stopWorker()

	ad := protocol.SessionAd{Address: a.Address, Session: protocol.NewID()}
	if opts.Listen != "" {
		endpoint, cert, stop, err := a.startDirect(opts, ad.Session)
		if err != nil {
			return err
		}
		defer stop()
		ad.Endpoint, ad.CertPEM = endpoint, cert
	}
	protocol.SignAd(&ad, a.id.Sign)
	a.adQuery = "?ad=" + ad.Encode()
	a.Logf("session %s#%s", a.Address, ad.Session)
	backoff := time.Second
	for {
		healthy, err := a.streamOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrRevoked) {
			return err
		}
		if healthy {
			backoff = time.Second
		}
		wait := backoff/2 + rand.N(backoff/2+1)
		a.Logf("hub stream ended (%v); reconnecting in %s", err, wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// streamOnce runs one push connection. healthy reports that the Hub accepted
// it and every pushed message was processed, so the caller can reset backoff.
func (a *Agent) streamOnce(ctx context.Context) (healthy bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := a.hub.request(ctx, "GET", "/v1/stream"+a.adQuery, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.hub.http.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return false, err
	}
	a.Logf("connected to hub as %s", a.Address)

	// Three missed pings mean the connection is dead even if TCP has not noticed.
	watchdog := time.AfterFunc(3*a.heartbeat, cancel)
	defer watchdog.Stop()

	// One worker retries queued sends and receipts, so a large upload never
	// delays reading the stream (pings, acks, incoming messages).
	kick := make(chan struct{}, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
				a.sync(ctx)
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	a.kick = func() {
		select {
		case kick <- struct{}{}:
		default: // a retry pass is already pending
		}
	}
	a.kick()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 2*protocol.MaxBody)
	var event, data string
	for sc.Scan() {
		watchdog.Reset(3 * a.heartbeat)
		line := sc.Text()
		switch {
		case line == "":
			if err := a.dispatch(ctx, event, data); err != nil {
				return false, err
			}
			event, data = "", ""
		case strings.HasPrefix(line, "event: "):
			event = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data += line[len("data: "):]
		}
	}
	if err := sc.Err(); err != nil {
		return true, err
	}
	return true, io.EOF
}

// sync retries queued sends and unsent receipts. The stream's worker runs it
// on connect and on each Hub ping, so retries ride on existing traffic
// instead of a poll loop.
func (a *Agent) sync(ctx context.Context) {
	if err := a.FlushOutbox(ctx); err != nil {
		a.Logf("outbox: %v", err)
	}
	if err := a.flushReceipts(ctx); err != nil {
		a.Logf("receipts: %v", err)
	}
}

func (a *Agent) dispatch(ctx context.Context, event, data string) error {
	switch event {
	case "message":
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return a.quarantineUndecodable(ctx, []byte(data))
		}
		if err := a.accept(ctx, env); err != nil {
			return errors.Join(errors.New("message "+env.ID+" not processed yet"), err)
		}
	case "release":
		// The Hub operator's recommended client version: saved, then the
		// worker is woken to tell the person (never on this reader).
		if err := a.saveRelease([]byte(data)); err != nil {
			a.Logf("release announcement ignored: %v", err)
		} else {
			a.wakeWorker()
		}
	case "ping":
		a.wakeWorker()
		// Prove this connection is alive; the Hub drops unanswered streams.
		var ping protocol.PingAck
		if err := json.Unmarshal([]byte(data), &ping); err == nil && ping.Conn != "" {
			if err := a.hub.do(ctx, "POST", "/v1/stream/ack", ping, nil); err != nil {
				a.Logf("ping ack: %v", err)
			}
		}
		a.kick()
	}
	return nil
}

// quarantineUndecodable holds a malformed envelope under its id, if it has
// one, and reports it quarantined so the Hub stops redelivering it.
func (a *Agent) quarantineUndecodable(ctx context.Context, raw []byte) error {
	var head struct{ ID, From string }
	if json.Unmarshal(raw, &head) != nil || head.ID == "" {
		a.Logf("dropping undecodable push event without an id")
		return nil
	}
	a.Logf("message %s is malformed; quarantined", head.ID)
	if err := a.store.quarantine(head.ID, head.From, reasonInvalid, raw); err != nil {
		return err
	}
	return a.flushReceipts(ctx)
}

// accept verifies, decrypts and persists one envelope, then sends its receipt:
// delivered for a verified message, quarantined for one that failed
// verification (so a hostile or broken sender cannot wedge the mailbox).
// Transient failures return an error before any receipt.
func (a *Agent) accept(ctx context.Context, env envelope.Envelope) error {
	seen, err := a.store.seen(env.ID)
	if err != nil {
		return err
	}
	if seen {
		err = a.store.resendReceipt(env.ID)
	} else {
		err = a.verifyAndStore(ctx, env)
	}
	if err != nil {
		return err
	}
	return a.flushReceipts(ctx)
}

// flushReceipts sends every stored disposition the Hub has not acknowledged.
func (a *Agent) flushReceipts(ctx context.Context) error {
	pending, err := a.store.unsentReceipts()
	if err != nil {
		return err
	}
	for _, r := range pending {
		err := a.hub.do(ctx, "POST", "/v1/messages/"+url.PathEscape(r.id)+"/ack", protocol.AckRequest{State: r.state}, nil)
		var he *HubError
		if errors.As(err, &he) && he.Status == 404 {
			err = nil // the Hub no longer holds it; nothing to report
		}
		if err != nil {
			return err
		}
		if err := a.store.markAcked(r); err != nil {
			return err
		}
	}
	return nil
}

func (a *Agent) verifyAndStore(ctx context.Context, env envelope.Envelope) error {
	if env.To != a.Address {
		return a.hold(env, reasonInvalid)
	}
	sender, _, found, err := a.store.peer(env.From)
	if err != nil {
		return err
	}
	if !found {
		e, err := a.directory(ctx, env.From)
		if err != nil {
			if retryable(err) {
				return err
			}
			return a.hold(env, reasonInvalid)
		}
		if err := a.store.pin(e.Public); err != nil {
			return err
		}
		sender = e.Public
	}
	in, err := envelope.Open(env, a.id, a.Address, sender)
	if err == nil {
		// The key that verified it is the evidence, not whatever is pinned
		// by the time it is stored.
		if err := a.store.addInbox(in, sender.Fingerprint()); err != nil {
			return err
		}
		a.wakeWorker()
		return nil
	}
	// A failure may mean the sender's keys changed; hold the message until
	// the user decides to trust the new keys. If the directory cannot be
	// asked right now, retry later rather than calling the message invalid.
	e, derr := a.directory(ctx, env.From)
	if derr != nil && retryable(derr) {
		return derr
	}
	if derr == nil && !sameKeys(sender, e.Public) {
		if err := a.store.setPending(e.Public); err != nil {
			return err
		}
		a.Logf("message %s held: keys for %s changed; run `agentnet trust %s` after verifying", env.ID, env.From, env.From)
		return a.hold(env, reasonKeyChanged)
	}
	a.Logf("message %s from %s rejected: %v", env.ID, env.From, err)
	return a.hold(env, reasonInvalid)
}

func (a *Agent) hold(env envelope.Envelope, reason string) error {
	raw, _ := json.Marshal(env)
	return a.store.quarantine(env.ID, env.From, reason, raw)
}

// startWorker marks jobs a previous daemon left running as interrupted and
// starts the single worker for this home. The returned function stops it and
// waits, killing any harness it is running.
func (a *Agent) startWorker(ctx context.Context) (func(), error) {
	if err := a.store.interruptRunning(); err != nil {
		return nil, err
	}
	if stale, _ := filepath.Glob(filepath.Join(a.home, outFilePrefix+"*")); len(stale) > 0 {
		for _, f := range stale {
			os.Remove(f) // answers of jobs a previous daemon left running
		}
	}
	a.notifyTried, a.reviewTried, a.reviewGen, a.releaseTried = nil, nil, "", "" // a new run tries failed notices once more
	wake := make(chan struct{}, 1)
	a.wakeWorker = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	stopKicks, err := listenKicks(a.home, a.wakeWorker)
	if err != nil {
		// Still works: new messages and Hub pings wake the worker.
		a.Logf("local wake-up socket unavailable (%v); accept/cancel apply at the next Hub ping", err)
		stopKicks = func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); a.worker(ctx, wake) }()
	a.wakeWorker()
	return func() { cancel(); <-done; stopKicks(); a.wakeWorker = func() {}; notify.Close() }, nil
}
