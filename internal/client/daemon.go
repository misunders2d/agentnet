package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/cenkalti/backoff/v7"
	sse "github.com/tmaxmax/go-sse"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/notify"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// newReconnectBackoff is the schedule both stream loops wait on between
// connections (github.com/cenkalti/backoff/v7): waits in [0.5 s, 1 s],
// [1, 2], [2, 4], [4, 8], [8, 16], [16, 32], then [30 s, 60 s] for good
// (0.75 s doubling, ±1/3, capped at 45 s), reset after a healthy connection.
// The library's upper edge may exceed the band by a nanosecond. It never
// returns backoff.Stop.
func newReconnectBackoff() *backoff.ExponentialBackOff {
	return &backoff.ExponentialBackOff{InitialInterval: 750 * time.Millisecond, RandomizationFactor: 1.0 / 3, Multiplier: 2, MaxInterval: 45 * time.Second}
}

// Quarantine reasons.
const (
	reasonKeyChanged = "key_changed"
	reasonInvalid    = "invalid"
)

// ErrDaemonRunning means another process holds this home's daemon lock.
var ErrDaemonRunning = errors.New("another agentnet daemon is already running")

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
		return fmt.Errorf("%w for %s", ErrDaemonRunning, a.home)
	}
	if err != nil {
		return err
	}
	defer release()
	if err := a.CleanOpened(); err != nil { // this process alone writes there; nothing is open yet
		a.Logf("plaintext left by an earlier run: %v", err)
	}
	a.exe, a.canSwitch, a.prepare = opts.Executable, opts.CanSwitch, opts.PrepareSwitch
	a.update.Lock()
	a.update.pending, a.update.switching, a.update.ready = nil, nil, false
	a.update.Unlock()
	ctx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	a.stopRun = stopRun
	stopWorker, err := a.startWorker(ctx)
	if err != nil {
		return a.startFailed(err)
	}
	defer stopWorker()
	a.openConv, a.openPage = opts.OpenConv, opts.OpenPage
	alertCtx, stopAlerts := context.WithCancel(ctx)
	alertsDone := make(chan struct{})
	go func() { defer close(alertsDone); a.alertLoop(alertCtx, a.alertWake) }()
	defer func() { stopAlerts(); <-alertsDone }()
	statusCtx, stopStatuses := context.WithCancel(ctx)
	statusDone := make(chan struct{})
	go func() { defer close(statusDone); a.statusLoop(statusCtx) }() // statuses due, from this run or an earlier process (headless.go)
	defer func() { stopStatuses(); <-statusDone }()
	remindCtx, stopRemind := context.WithCancel(ctx)
	remindDone := make(chan struct{})
	go func() { defer close(remindDone); a.remindLoop(remindCtx) }()
	defer func() { stopRemind(); <-remindDone }()
	eraseCtx, stopErase := context.WithCancel(ctx)
	eraseDone := make(chan struct{})
	go func() { defer close(eraseDone); a.eraseLoop(eraseCtx) }() // convclear.go
	defer func() { stopErase(); <-eraseDone }()
	if opts.Owned != nil {
		stopOwned, err := opts.Owned()
		if err != nil {
			return a.startFailed(err)
		}
		defer stopOwned()
	}

	ad := protocol.SessionAd{Address: a.Address, Session: protocol.NewID()}
	a.session = ad.Session
	if opts.Listen != "" {
		endpoint, cert, stop, err := a.startDirect(opts, ad.Session)
		if err != nil {
			return a.startFailed(err)
		}
		defer stop()
		ad.Endpoint, ad.CertPEM = endpoint, cert
	}
	// Everything local is up: only now does an update this start completes
	// count as done, and only now are new requests looked at.
	a.settleUpdate()
	protocol.SignAd(&ad, a.id.Sign)
	a.adQuery = "?ad=" + ad.Encode()
	a.Logf("session %s#%s", a.Address, ad.Session)
	reconnect := newReconnectBackoff()
	reconnect.Reset()
	stopped := func() error { // ctx ended: a stop, or a switch for an update
		if r := a.UpdateSwitching(); r != nil {
			return &RestartForUpdate{Request: *r}
		}
		return nil
	}
	if a.LinkState().State == LinkPending {
		// This device joined to be linked: it is no member until its person
		// approves it on their other device; it waits on its only stream.
		a.Logf("waiting for approval of this device on %s", a.LinkState().Approver)
		if _, err := a.awaitLink(ctx, a.adQuery, ad.Session); err != nil { // this run's own session
			if ctx.Err() != nil {
				return stopped()
			}
			return err
		}
		a.Logf("this device is linked to its person")
	}
	a.sweepSelfConsent(ctx) // invites of this person's own agents still waiting here (participation.go)
	for {
		healthy, err := a.streamOnce(ctx)
		if ctx.Err() != nil {
			return stopped()
		}
		if errors.Is(err, ErrRevoked) || errors.Is(err, ErrLinkRefused) || errors.Is(err, ErrLinkExpired) {
			return err // the Hub ended this agent: reconnecting cannot help
		}
		if healthy {
			reconnect.Reset()
		}
		wait := reconnect.NextBackOff()
		a.Logf("hub stream ended (%v); reconnecting in %s", err, wait.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return stopped()
		case <-time.After(wait):
		}
	}
}

// streamOnce runs one push connection. healthy reports that the Hub accepted
// it and every pushed message was processed, so the caller can reset backoff.
func (a *Agent) streamOnce(ctx context.Context) (healthy bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cursor, _ := a.store.config("receipt_cursor")
	seq, _ := strconv.ParseInt(cursor, 10, 64)
	if seq < 0 {
		seq = 0
	}
	req, err := a.hub.request(ctx, "GET", "/v1/stream"+a.adQuery+"&receipts="+strconv.FormatInt(seq, 10), nil)
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
	a.membersConnected(resp.Header)
	defer a.membersDisconnected()
	a.teamsConnected(resp.Header) // teams.go: whether this relay pushes the team directory
	defer a.teamsDisconnected()
	a.typingConnected(resp.Header)
	defer a.typingDisconnected()
	a.convWork.due(convPublish | convRetry | convRelease | convHistory | convServe | convFetch) // a new connection: publish, then look again
	a.wakeStatus()                                                                              // statuses that could not reach the Hub before
	a.groupWork.recover.Store(true)                                                             // and replay group journal records not yet published (groups.go)

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
	a.kickMu.Lock()
	a.kick = func() {
		select {
		case kick <- struct{}{}:
		default: // a retry pass is already pending
		}
	}
	a.kickMu.Unlock()
	a.kick()

	return readStream(resp.Body, func() { watchdog.Reset(3 * a.heartbeat) }, func(event, data string) error { return a.dispatch(ctx, event, data) })
}

// readStream parses the push stream body with go-sse and hands each event
// to dispatch in order, synchronously; a dispatch error ends the stream as
// unhealthy (the Hub redelivers). It returns (true, io.EOF) when the Hub
// closed the stream, and (true, err) for a read error. touch is called on
// every read that brings bytes, comments and keepalives included, so the
// caller's watchdog sees the connection alive.
//
// The bound is per EVENT: go-sse's scanner token is one whole event (all
// its lines up to the blank line; parser.splitFunc), so MaxEventSize caps an
// event's bytes together, however many short lines it has; the memory held
// while one event is parsed is O(streamEventLimit) (scanner buffer, the
// token's copy and the assembled data are each bounded by it), not a precise
// ceiling. One event over streamEventLimit ends the stream with an explicit
// error and nothing of it is dispatched (TestStreamBoundIsPerEvent). The Hub
// writes single-line frames and every frame type must stay under this
// limit: the member and team directories are bounded by protocol.MaxMembers
// and MaxTeams; group heads are emitted as bounded batches by the Hub.
func readStream(body io.Reader, touch func(), dispatch func(event, data string) error) (healthy bool, err error) {
	r := &streamBody{r: body, touch: touch}
	for e, rerr := range sse.Read(r, &sse.ReadConfig{MaxEventSize: streamEventLimit}) {
		if rerr != nil {
			if errors.Is(rerr, errStreamEnd) || errors.Is(rerr, sse.ErrUnexpectedEOF) {
				return true, io.EOF // the Hub closed; an unfinished event or line is dropped
			}
			if errors.Is(rerr, bufio.ErrTooLong) {
				return true, fmt.Errorf("push stream event larger than %d bytes: %w", streamEventLimit, rerr)
			}
			return true, rerr
		}
		if err := dispatch(e.Type, e.Data); err != nil {
			return false, err
		}
	}
	return true, io.EOF
}

// streamEventLimit is the most one push event may take on the wire, all its
// lines together (the same 2*MaxBody the scanner allowed one line before).
const streamEventLimit = 2 * protocol.MaxBody

// errStreamEnd stands in for io.EOF beneath go-sse. sse.Read yields an
// event still being assembled when it meets a clean EOF (by design, for
// short LLM-style responses); the SSE specification says an event without
// its final blank line is not dispatched, and this daemon relies on that: a
// connection cut mid-event must not deliver half of it. Seeing any other
// error instead of EOF, sse.Read drops the partial event and reports the
// error, which readStream turns back into io.EOF.
var errStreamEnd = errors.New("stream ended")

// streamBody is the push stream's body as go-sse reads it: every read that
// brings bytes touches the watchdog, and EOF is reported as errStreamEnd.
type streamBody struct {
	r     io.Reader
	touch func()
}

func (b *streamBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if n > 0 && b.touch != nil {
		b.touch()
	}
	if err == io.EOF {
		err = errStreamEnd
	}
	return n, err
}

// sync retries queued sends and unsent receipts. The stream's worker runs it
// on connect and on each Hub ping, so retries ride on existing traffic
// instead of a poll loop.
func (a *Agent) sync(ctx context.Context) {
	a.convSync(ctx)  // only what an event made due: no request otherwise
	a.syncTeams(ctx) // team references queued by the stream, verified and pinned (teams.go)
	a.groupSync(ctx) // group journal: pending publications first, then heads the stream said changed (convgroup.go)
	if err := a.FlushOutbox(ctx); err != nil {
		a.Logf("outbox: %v", err)
	}
	if err := a.flushReceipts(ctx); err != nil {
		a.Logf("receipts: %v", err)
	}
}

func (a *Agent) dispatch(ctx context.Context, event, data string) error {
	switch event {
	case "device_admin":
		return a.onDeviceAdminNotice([]byte(data))
	case "receipt":
		var receipt protocol.ReceiptEvent
		if err := decodeStrict([]byte(data), &receipt); err != nil || !protocol.ValidID(receipt.ID) || receipt.Seq <= 0 || (receipt.State != protocol.StateDelivered && receipt.State != protocol.StateQuarantined && receipt.State != protocol.StateExpired) {
			a.Logf("invalid receipt event ignored")
			return nil
		}
		return a.store.applyReceipt(receipt)
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
	case "members":
		a.onMembers([]byte(data)) // the Hub's member list (members.go)
	case "signal":
		a.onSignal([]byte(data)) // live typing only: no inbox, receipt or worker
	case "teams":
		a.onTeams([]byte(data)) // the Hub's team directory (teams.go): references queued, verified by the sync worker
	case "groups":
		a.onGroupHeads([]byte(data)) // the caller's group journal heads (convgroup.go): noted, fetched by the sync worker
	case "link":
		a.onLinkEvent([]byte(data)) // a device asks to join this person (link.go)
	case "ping":
		a.wakeWorker()
		a.wakeStatus()
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
	if err == nil && in.V == envelope.Version2 {
		return a.admitConv(ctx, env, in, sender, false)
	}
	if err == nil && in.V == envelope.Version3 {
		return a.admitControl(ctx, env, in, sender, false)
	}
	if err == nil {
		if namedAgentFields(in) {
			if err := a.checkDeviceAgent(in, sender); err != nil {
				return a.hold(env, reasonInvalid)
			}
		}
		if in.ReceiverRoute != nil && in.ReceiverRoute.Op != "request" {
			if err := a.receiverSetupSender(a.store.db, in, sender.Fingerprint()); err != nil {
				return a.hold(env, reasonInvalid)
			}
		}
		// A person grant needs verified membership before initialState, including
		// a newly linked device. A current grant refreshes its signed chain at
		// this request boundary; a Hub outage retries custody, never lends stale
		// membership authority. This is not periodic polling.
		if in.Kind == envelope.KindQuestion || in.Kind == envelope.KindTask {
			if p, e := a.personOfKey(ctx, in.From, sender); e == nil {
				var grants int
				if e = a.store.db.QueryRow(`SELECT count(*) FROM person_grants WHERE person=?`, p.info.Person).Scan(&grants); e != nil {
					return e
				}
				if grants > 0 {
					if _, e = a.refreshPerson(ctx, p.info.Person, false); e != nil {
						return e
					}
				}
			}
		}
		// The key that verified it is the evidence, not whatever is pinned
		// by the time it is stored.
		var local *envelope.Envelope
		if env.From == a.Address && sender.Fingerprint() == a.Self().Fingerprint() {
			local = &env // exact local outbox provenance is checked in the same insert transaction
		}
		if err := a.store.addReceivedInbox(in, sender.Fingerprint(), local); err != nil {
			return err
		}
		if err := a.processReceiverCatalog(in, sender.Fingerprint()); err != nil {
			return err
		}
		a.wakeWorker()
		if in.ReceiverRoute == nil || in.ReceiverRoute.Op == "request" {
			a.noteStatus(in.ID)
		} // setup is not remote task execution
		if len(in.Attachments) > 0 {
			a.convWork.due(convFetch) // keep its files here (historyfiles.go prefetchFiles)
			a.kickNow()
		}
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
	a.stopSurvivors() // a harness a daemon that died left running: stopped first
	if err := a.store.interruptRunning(); err != nil {
		return nil, err
	}
	if stale, _ := filepath.Glob(filepath.Join(a.home, outFilePrefix+"*")); len(stale) > 0 {
		for _, f := range stale {
			os.Remove(f) // answers of jobs a previous daemon left running
		}
	}
	if err := a.cleanRuns(); err != nil { // their run folders too (runfiles.go)
		a.Logf("run folders left by an earlier run: %v", err)
	}
	a.notifyTried, a.reviewTried, a.reviewGen, a.releaseTried = nil, nil, "", "" // a new run tries failed notices once more
	a.reviewAgain.reset()
	// The stable per-handle wake avoids a startup/stop race with local commits.
	wake := a.workerWake
	// A wake from another agentnet process means it changed local state:
	// the worker looks again, and so does a messenger page; the stream's
	// worker picks up what it queued (history for a device it linked, file
	// requests) and sends it now. That includes copies a send kept waiting:
	// it read the recipient's profile before storing them, and the members
	// push that announced newer support may have come in between, spent on
	// a release pass that found nothing waiting yet; nothing else would
	// look at them again (convRelease).
	stopKicks, err := listenKicks(a.home, func() {
		a.wakeWorker()
		a.wakeStatus() // a status another process noted (noteStatus)
		a.changes.bump()
		a.convWork.due(convHistory | convServe | convFetch | convRetry | convRelease) // convRetry: a local participation record shares its public scope with guests now
		// A responder or named agent was set or removed: say so.
		if a.agentHintStale() {
			a.convWork.due(convPublish)
		}
		a.kickNow()
	})
	if err != nil {
		// Still works: new messages and Hub pings wake the worker.
		a.Logf("local wake-up socket unavailable (%v); accept/cancel apply at the next Hub ping", err)
		stopKicks = func() {}
	}
	// A command waiting for an answer here is woken by each local change
	// (answerwait.go): it never polls.
	stopChanges, err := listenChanges(a.home, a.Changed)
	if err != nil {
		a.Logf("local change socket unavailable (%v); a command waiting for an answer returns at once", err)
		stopChanges = func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); a.worker(ctx, wake) }()
	a.wakeWorker()
	return func() {
		cancel()
		<-done
		stopKicks()
		stopChanges()
		notify.Close()
	}, nil
}

// stopSurvivors stops the harnesses an earlier daemon left running (it
// died: on a normal stop it stops them itself), before their jobs are
// marked interrupted, so a rerun never runs beside one. Only a group still
// proven to be that run's is stopped (survivingGroup).
func (a *Agent) stopSurvivors() {
	home, err := filepath.Abs(a.home) // as the run was given it (runJob)
	if err != nil {
		home = a.home
	}
	rows, err := a.store.db.Query(`SELECT id, run_pgid, coalesce(run_start, '') FROM inbox WHERE state IN (?, ?) AND run_pgid IS NOT NULL`,
		stateRunning, stateCancelReq)
	if err != nil {
		a.Logf("runs left by an earlier daemon: %v", err)
		return
	}
	type run struct {
		id    string
		pgid  int
		start string
	}
	var runs []run
	for rows.Next() {
		var r run
		if rows.Scan(&r.id, &r.pgid, &r.start) == nil {
			runs = append(runs, r)
		}
	}
	rows.Close()
	for _, r := range runs {
		if !survivingGroup(r.pgid, r.start, home) {
			continue
		}
		if err := killGroup(r.pgid); err != nil {
			a.Logf("%s: its harness, left running by a daemon that stopped, could not be stopped (process group %d): %v", r.id, r.pgid, err)
			continue
		}
		a.Logf("%s: its harness was still running, left by a daemon that stopped (process group %d): stopped", r.id, r.pgid)
	}
}
