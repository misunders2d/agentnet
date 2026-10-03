// Package client is the laptop side of AgentNet: enrollment, trusted peer
// keys, an end-to-end-encrypted outbox and inbox, and the push daemon.
package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/notify"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// ErrPeerRevoked means the addressed agent has been revoked.
var ErrPeerRevoked = errors.New("recipient has been revoked")

// KeyChangedError blocks sending until the user explicitly trusts the new key.
type KeyChangedError struct {
	Address, Pinned, Offered string
}

func (e *KeyChangedError) Error() string {
	return fmt.Sprintf("keys for %s changed (trusted %s, directory now offers %s); verify with the owner, then run `agentnet trust %s`",
		e.Address, e.Pinned, e.Offered, e.Address)
}

// Agent is one enrolled agent's local state and Hub connection.
type Agent struct {
	Address string
	Logf    func(format string, args ...any)

	humanMu        sync.RWMutex // local human end commits serialize with ordinary copy/file delivery
	home           string
	id             *identity.Identity
	store          *store
	teams          teamRuntime   // the relay's team directory as last seen (teams.go)
	typing         typingRuntime // live typing signals, memory only (typing.go)
	groupWork      groupWork     // group journal upkeep due on the next sync (convgroup.go)
	hub            *hubConn
	heartbeat      time.Duration
	adQuery        string                     // this run's signed session ad, for the push stream
	kick           func()                     // wakes the current stream's retry worker
	kickMu         sync.Mutex                 // guards kick for kickNow
	prefetchFailed map[string]bool            // conversation files that could not be kept this run (historyfiles.go; the stream worker only)
	wakeWorker     func()                     // wakes the question/task worker; a no-op outside Run
	changes        *changeFeed                // local state changed (changes.go)
	listed         listedCache                // rosters the Hub lists for persons not pinned here (persons.go)
	members        memberState                // the Hub's member list from the push stream (members.go)
	session        string                     // this run's session id (Run); "" outside Run
	convWork       convWork                   // conversation upkeep due on the next sync (conv.go)
	agentSweep     agentSweep                 // the worker's look at requests to its agent (agentjob.go)
	alertWake      chan struct{}              // wakes the desktop alert loop (alerts.go)
	openConv       func(conv string) []string // RunOptions.OpenConv

	notify       func(title, body string, argv []string, onClick func()) error // desktop notification; argv and onClick may be nil
	notifyTried  map[string]bool                                               // review items a notification was attempted for, this run
	reviewTried  map[string]bool                                               // review items a review notice was attempted for, this run
	reviewGen    string                                                        // review_to_gen those attempts were made under
	releaseTried string                                                        // release a notification was attempted for, this run

	exe       string                       // the daemon's program file as started (RunOptions.Executable)
	canSwitch func() (ok bool, why string) // RunOptions.CanSwitch
	prepare   func(UpdateRequest) error    // RunOptions.PrepareSwitch
	stopRun   context.CancelFunc           // stops the current Run (switching for an update)
	update    struct {
		sync.Mutex
		pending   *UpdateRequest // requested: no new job starts
		switching *UpdateRequest // the daemon is stopping for it
		ready     bool           // started: requests are settled and looked at
	}
}

func paths(home string) (identityPath, dbPath string) {
	return filepath.Join(home, "identity.json"), filepath.Join(home, "agent.db")
}

// Join enrolls a new agent named agentName with an invite code.
//
// Keys and the enrollment intent are saved before the Hub is contacted, so if
// the Hub's answer is lost, running Join again with the same invite and name
// re-sends the identical request, which the Hub accepts as a replay.
func Join(ctx context.Context, home, code, agentName string) (*Agent, error) {
	return join(ctx, home, code, agentName, nil)
}

// join enrolls with the invite code; with link, the join request carries
// what link makes from this device's entry (a device link), and the state
// it returns is saved with the enrollment.
func join(ctx context.Context, home, code, agentName string, link func(identity.Public, ed25519.PrivateKey) (*protocol.JoinLink, map[string]string)) (*Agent, error) {
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		return nil, err
	}
	address := protocol.Address(inv.Label, agentName)
	if _, _, err := protocol.SplitAddress(address); err != nil {
		return nil, err
	}
	if err := secfile.EnsureDir(home); err != nil {
		return nil, err
	}
	idPath, dbPath := paths(home)
	id, err := identity.Load(idPath)
	if errors.Is(err, os.ErrNotExist) {
		if id, err = identity.Generate(); err == nil {
			err = id.Save(idPath)
		}
	}
	if err != nil {
		return nil, err
	}
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	defer st.db.Close()
	if enrolled, _ := st.config("enrolled"); enrolled == "1" {
		return nil, fmt.Errorf("%s already holds an enrolled agent", home)
	}
	if err := st.setConfig(map[string]string{"address": address, "hub": inv.Hub, "hub_cert": inv.CertPEM, "join_secret": inv.Secret}); err != nil {
		return nil, err
	}
	conn, err := newHubConn(inv.Hub, inv.CertPEM, "", nil)
	if err != nil {
		return nil, err
	}
	var v protocol.VersionInfo
	if err := conn.do(ctx, "GET", "/v1/version", nil, &v); err != nil {
		return nil, fmt.Errorf("cannot reach the Hub at %s: %w", inv.Hub, err)
	}
	if v.Protocol != protocol.ProtocolVersion {
		return nil, fmt.Errorf("the Hub speaks protocol %d and this agentnet %d; update the older one", v.Protocol, protocol.ProtocolVersion)
	}
	if v.RealmID != "" { // the relay's workspace identity (realm.go); an older relay names none, and none is invented
		if err := st.recordRealm(v.RealmID); err != nil {
			return nil, err
		}
	}
	req := protocol.JoinRequest{Secret: inv.Secret, Public: id.Public(address)}
	saved := map[string]string{"enrolled": "1"}
	if link != nil {
		var state map[string]string
		req.Link, state = link(req.Public, id.Sign)
		for k, v := range state {
			saved[k] = v
		}
	}
	protocol.SignJoin(&req, id.Sign)
	if err := conn.do(ctx, "POST", "/v1/join", req, nil); err != nil {
		var he *HubError
		if errors.As(err, &he) && he.Code == protocol.CodeRosterStale {
			return nil, fmt.Errorf("%w (nothing was enrolled)", ErrLinkStale)
		}
		if errors.As(err, &he) && he.Status == http.StatusConflict && he.Code == protocol.CodeAddressTaken {
			// A definite refusal: nothing was enrolled. Never pick another
			// name here; the person confirms the address they want.
			return nil, fmt.Errorf("%s: %w; nothing was enrolled, and this invitation and this computer's key are still valid. "+
				"Hub: %s. Ask the person to confirm that address or choose another NAME, then run: agentnet join --agent NAME CODE",
				address, ErrAddressTaken, he.Msg)
		}
		if errors.As(err, &he) && he.Status == http.StatusForbidden {
			// A definite refusal: this invitation can never enroll.
			return nil, fmt.Errorf("the Hub refused this invitation (%w): it is invalid, expired or already used, and nothing was enrolled. "+
				"Ask for a new invitation, then run: agentnet join --agent NAME NEW_CODE", err)
		}
		return nil, fmt.Errorf("enrollment not confirmed: %w (run the same join command again to retry)", err)
	}
	if err := st.setConfig(saved); err != nil {
		return nil, err
	}
	if err := st.deleteConfig("join_secret"); err != nil {
		return nil, err
	}
	st.db.Close()
	return Open(home)
}

// ErrAddressTaken means the Hub refused a join because the address is
// enrolled (or was, and is revoked). The invite and keys stay usable.
var ErrAddressTaken = errors.New("address already taken on this Hub")

// Open loads an enrolled agent from home.
func Open(home string) (*Agent, error) {
	idPath, dbPath := paths(home)
	id, err := identity.Load(idPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no enrolled agent in %s; run `agentnet join` first", home)
		}
		return nil, err
	}
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	if enrolled, _ := st.config("enrolled"); enrolled != "1" {
		st.db.Close()
		return nil, fmt.Errorf("enrollment in %s is incomplete; run `agentnet join` again with the same invitation "+
			"(with another NAME if the Hub said the address was taken)", home)
	}
	a := &Agent{home: home, id: id, store: st, heartbeat: protocol.HeartbeatInterval, Logf: func(string, ...any) {}, wakeWorker: func() {}, notify: desktopNotify,
		changes: newChangeFeed(), alertWake: make(chan struct{}, 1)}
	st.onChange = a.changes.bump
	var hubURL, cert string
	if a.Address, err = st.config("address"); err == nil {
		if hubURL, err = st.config("hub"); err == nil {
			cert, err = st.config("hub_cert")
		}
	}
	if err == nil {
		a.hub, err = newHubConn(hubURL, cert, a.Address, id.Sign)
	}
	if err != nil {
		st.db.Close()
		return nil, fmt.Errorf("corrupt agent config: %w", err)
	}
	a.hub.workspaceCheck = a.WorkspaceRequestGuard()
	a.typing.groupMembers = a.GroupMembers // verified effective group membership (groups.go); never the frozen root
	return a, nil
}

// Close releases local storage.
func (a *Agent) Close() error {
	a.typingDisconnected()
	return a.store.db.Close()
}

// Self returns this agent's own directory entry.
func (a *Agent) Self() identity.Public { return a.id.Public(a.Address) }

func (a *Agent) directory(ctx context.Context, address string) (protocol.DirectoryEntry, error) {
	var e protocol.DirectoryEntry
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return e, err
	}
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name, nil, &e); err != nil {
		return e, err
	}
	if e.Public.Address != address {
		return e, errors.New("directory answered for a different address")
	}
	return e, e.Public.Verify()
}

func sameKeys(a, b identity.Public) bool {
	return bytes.Equal(a.SignKey, b.SignKey) && a.BoxRecipient == b.BoxRecipient
}

// sendKey returns the trusted key for a recipient. The first key seen is
// pinned; a later different key blocks sending until Trust is called.
//
// When the Hub is unreachable a previously pinned key is used so the message
// can be queued; the Hub still refuses it later if the recipient is revoked.
func (a *Agent) sendKey(ctx context.Context, address string) (identity.Public, error) {
	pinned, _, found, err := a.store.peer(address)
	if err != nil {
		return identity.Public{}, err
	}
	e, err := a.directory(ctx, address)
	if err != nil {
		if found && retryable(err) {
			return pinned, nil
		}
		return identity.Public{}, err
	}
	if e.Revoked {
		return identity.Public{}, ErrPeerRevoked
	}
	if !found {
		return e.Public, a.store.pin(e.Public)
	}
	if sameKeys(pinned, e.Public) {
		return pinned, nil
	}
	if err := a.store.setPending(e.Public); err != nil {
		return identity.Public{}, err
	}
	return identity.Public{}, &KeyChangedError{address, pinned.Fingerprint(), e.Public.Fingerprint()}
}

// SendResult reports what is known about a message after Send.
type SendResult struct {
	ID     string `json:"id"`
	Path   string `json:"path,omitempty"`   // direct or relay once handed over
	State  string `json:"state"`            // queued, custody or delivered
	Detail string `json:"detail,omitempty"` // why it is still queued
}

// Outgoing is a message to send.
type Outgoing struct {
	To       string // person/agent, or person/agent#session for one running daemon
	Body     string
	ReplyTo  string
	Files    []string
	Named    []OutgoingFile // more files, each with the name it is shown under (a page's staged uploads)
	Fallback bool           // if the addressed session has ended, deliver to the agent's inbox
	Kind     string         // envelope.KindMessage (default), KindQuestion or KindTask
	// Wait, if positive, waits up to this long for the recipient's receipt
	// after the Hub takes custody (one request, woken by the receipt).
	Wait    time.Duration
	Status  string           // outcome, for answers and results
	Target  *envelope.Target // named executor on a question/task; negotiated with agi1
	AgentID string           // named executor author on an answer/result
	// FollowUp, if set, stays local: when the recipient's first reply
	// arrives, the worker processes it once with these instructions and
	// stores a summary (or a needs-human flag) for the local user. It never
	// sends anything back.
	FollowUp      string
	ReplyReceiver *ReplyReceiver // explicit local return selection; never sent on wire

	releaseSpoolLock func()

	// claim runs in the transaction that stores the outgoing message; it
	// records which received question or task the message answers.
	claim func(tx *sql.Tx, replyID string) error
}

const maxFollowUp = 4 << 10

// desktopNotify shows a notification unless AGENTNET_NOTIFY=off (for
// servers and tests). onClick, if set, runs when the person clicks it
// (Linux only for now).
func desktopNotify(title, body string, argv []string, onClick func()) error {
	if os.Getenv("AGENTNET_NOTIFY") == "off" {
		return errors.New("turned off by AGENTNET_NOTIFY=off")
	}
	return notify.ShowAction(title, body, argv, onClick)
}

// Send is SendMessage for the common case.
func (a *Agent) Send(ctx context.Context, to, body, replyTo string, files ...string) (SendResult, error) {
	return a.SendMessage(ctx, Outgoing{To: to, Body: body, ReplyTo: replyTo, Files: files})
}

// SendMessage encrypts the body and files to the recipient and delivers
// them: straight to a reachable daemon of the recipient when one advertises
// a verified endpoint, otherwise (or if that fails) through the Hub. Files
// are encrypted into a private spool first; if the Hub is unreachable the
// message stays queued and the daemon resumes it.
func (a *Agent) SendMessage(ctx context.Context, m Outgoing) (SendResult, error) {
	binding, err := a.prepareReplyReceiver(m.ReplyReceiver)
	if err != nil {
		return SendResult{}, err
	}
	receiverStored := false
	defer func() {
		if !receiverStored && binding != nil && binding.setup != nil {
			a.releaseSpool(envelope.Envelope{Blobs: blobsOf(binding.setup.in.Attachments)})
		}
	}()
	if binding != nil && m.FollowUp != "" {
		return SendResult{}, errors.New("explicit reply receiver cannot use legacy follow-up")
	}
	if m.ReplyTo != "" { // never continue a conversation (DM) in the older format
		if conv, err := a.store.convOf(m.ReplyTo); err != nil {
			return SendResult{}, err
		} else if conv != "" {
			return SendResult{}, ErrConversationItem
		}
	}
	if len(m.Files)+len(m.Named) > envelope.MaxAttachments {
		return SendResult{}, fmt.Errorf("at most %d attachments per message", envelope.MaxAttachments)
	}
	if len(m.FollowUp) > maxFollowUp {
		return SendResult{}, fmt.Errorf("follow-up instructions are limited to %d bytes", maxFollowUp)
	}
	to, session, err := protocol.SplitTarget(m.To)
	if err != nil {
		return SendResult{}, err
	}
	peer, err := a.sendKey(ctx, to)
	if err != nil {
		return SendResult{}, err
	}
	recipient, err := peer.Recipient()
	if err != nil {
		return SendResult{}, err
	}
	var route *protocol.SessionAd
	if infos, err := a.sessions(ctx, to); err == nil {
		if session != "" && !m.Fallback && !live(infos, session) {
			return SendResult{}, ErrSessionExpired
		}
		route = a.directRoute(infos, to, session, peer)
	}
	if m.Kind == "" {
		m.Kind = envelope.KindMessage
	}
	in := envelope.Inner{
		ID: protocol.NewID(), From: a.Address, To: to, TS: time.Now().Unix(),
		Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Session: session, Fallback: m.Fallback, Status: m.Status,
		Target: m.Target, AgentID: m.AgentID,
	}
	if namedAgentFields(in) {
		if m.Target != nil && (m.Target.Address != to || m.Target.Fingerprint != peer.Fingerprint()) {
			return SendResult{}, errors.New("selected agent does not belong to the verified recipient key")
		}
		if err := a.requireAgentIdentity(ctx, peer); err != nil && !retryable(err) && !isResponderProgress(in) { // progress waits instead
			return SendResult{}, err
		}
	}
	if len(m.Files)+len(m.Named) > 0 {
		// Cleanup must not see spooled files before the outbox refers to them.
		release, err := lockfile.Wait(a.spoolLockPath())
		if err != nil {
			return SendResult{}, err
		}
		defer release() // idempotent; released early below, before delivery
		m.releaseSpoolLock = release
	}
	files := make([]OutgoingFile, 0, len(m.Files)+len(m.Named))
	for _, path := range m.Files {
		files = append(files, OutgoingFile{Path: path})
	}
	for _, f := range append(files, m.Named...) {
		att, err := a.spoolNamed(f, recipient)
		if err != nil {
			a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
			return SendResult{}, err
		}
		in.Attachments = append(in.Attachments, att)
	}
	for _, f := range append(files, m.Named...) {
		// This device's own copy (encrypted to its own key), so the sender
		// can open what it sent later: the spooled copy is readable by the
		// recipient only (historyfiles.go keepSent, as conversation sends do).
		if err := a.keepSent(f.Path); err != nil {
			a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
			return SendResult{}, err
		}
	}
	returnHost, e := a.receiverReturnHost(ctx, in.ReplyTo)
	if e != nil {
		a.releaseSpool(envelope.Envelope{Blobs: blobsOf(in.Attachments)})
		return SendResult{}, e
	}
	var returnCopy *outCopy
	defer func() {
		if !receiverStored && returnCopy != nil {
			a.releaseSpool(envelope.Envelope{Blobs: blobsOf(returnCopy.in.Attachments)})
		}
	}()
	if returnHost != nil {
		if binding != nil {
			return SendResult{}, errors.New("an explicit new receiver cannot replace an original request return destination")
		}
		c, e := a.receiverReturnCopy(ctx, in, append(files, m.Named...), *returnHost)
		if e != nil {
			return SendResult{}, e
		}
		returnCopy = &c
	}
	var remoteCopies []outCopy
	if binding != nil && binding.receiver.Host != nil {
		remoteCopies = []outCopy{{in: in, recipientFP: peer.Fingerprint()}}
		if err := a.prepareRemoteCopies(ctx, binding, remoteCopies, append(files, m.Named...)); err != nil {
			a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
			if binding.setup != nil {
				a.releaseSpool(envelope.Envelope{Blobs: blobsOf(binding.setup.in.Attachments)})
			}
			return SendResult{}, err
		}
		in = remoteCopies[0].in
	}
	var env envelope.Envelope
	if len(remoteCopies) > 0 {
		env = remoteCopies[0].env
	} else {
		env, err = envelope.Seal(in, a.id.Sign, recipient)
	}
	if err == nil {
		beforeOutbox()
		if returnCopy != nil {
			if m.FollowUp != "" {
				return SendResult{}, errors.New("legacy follow-up cannot replace committed return fanout")
			}
			guard := func(tx *sql.Tx, id string) error {
				if e := receiverReturnCurrent(tx, in.ReplyTo, *returnHost); e != nil {
					return e
				}
				if m.claim != nil {
					return m.claim(tx, id)
				}
				return nil
			}
			err = a.store.addConvOutbox([]outCopy{{in: in, env: env, state: stateQueued, recipientFP: peer.Fingerprint()}, *returnCopy}, envelope.Inner{}, guard, "")
		} else {
			err = a.store.addOutbox(env, in, m.FollowUp, m.claim, boundOutgoing{binding: binding, fingerprint: peer.Fingerprint()})
		}
	}
	if err != nil {
		a.releaseSpool(envelope.Envelope{ID: in.ID, Blobs: blobsOf(in.Attachments)})
		return SendResult{}, err
	}
	receiverStored = true
	defer notifyDaemon(a.home) // a messenger page open in the daemon shows the message and its state
	if m.releaseSpoolLock != nil {
		m.releaseSpoolLock()
	}
	if binding != nil && binding.remote != nil && binding.remote.Role == "origin" {
		if _, err := a.deliver(ctx, binding.setup.env, nil); err != nil && !retryable(err) {
			return SendResult{ID: env.ID, State: stateReceiverWaiting, Detail: err.Error()}, err
		}
		return SendResult{ID: env.ID, State: stateReceiverWaiting, Detail: "awaiting exact selected-host delegation approval"}, nil
	}
	if returnCopy != nil {
		if _, e := a.deliver(ctx, returnCopy.env, nil); e != nil && !retryable(e) {
			return SendResult{ID: env.ID, State: stateQueued, Detail: e.Error()}, e
		}
	}
	res, err := a.deliver(ctx, env, route)
	if err != nil || m.Wait <= 0 || res.Path != protocol.PathRelay || res.State != protocol.StateCustody {
		return res, err // direct delivery already has the recipient's receipt
	}
	if r, werr := a.waitReceipt(ctx, env.ID, m.Wait); werr == nil && r.State != "" {
		res.State = r.State
		if r.State != protocol.StateCustody {
			a.store.setOutboxState(env.ID, r.State, "", "")
		}
	}
	return res, nil // custody stays true even if the wait was cut short
}

func blobsOf(atts []envelope.Attachment) []envelope.Blob {
	var out []envelope.Blob
	for _, a := range atts {
		out = append(out, a.Blob)
	}
	return out
}

// deliver tries the direct route (if any) and then the Hub. The spool is
// released only once one of them has confirmed custody of the message.
func (a *Agent) deliver(ctx context.Context, env envelope.Envelope, route *protocol.SessionAd) (SendResult, error) {
	if state, _, found, e := a.store.outboxState(env.ID); e != nil {
		return SendResult{}, e
	} else if found && state == stateReceiverWaiting {
		return SendResult{ID: env.ID, State: state}, nil
	}
	if ok, err := a.receiverOriginalMayDeliver(env); err != nil || !ok {
		state, _, _, _ := a.store.outboxState(env.ID)
		return SendResult{ID: env.ID, State: state}, err
	}
	var required, conv, sub, body, pid, humanRaw, rowStatus, agentID, capturedFP string
	if err := a.store.db.QueryRow(`SELECT coalesce(required_cap, ''), coalesce(conv, ''),coalesce(sub,''),body,coalesce(pid,''),coalesce(human,''),coalesce(status,''),coalesce(agent_id,''),coalesce(recipient_fp,'') FROM outbox WHERE id=?`, env.ID).Scan(&required, &conv, &sub, &body, &pid, &humanRaw, &rowStatus, &agentID, &capturedFP); err != nil {
		return SendResult{}, err
	}
	progress := rowStatus == envelope.StatusProgress
	if progress && required == "" {
		required = protocol.CapProgress
	}
	if humanRaw != "" || required == protocol.CapHumanParticipation && sub == envelope.SubExcerpt {
		a.humanMu.RLock()
		defer a.humanMu.RUnlock()
	}
	receiverCap, err := receiverCopyNeedsCapability(a.store.db, env.ID, sub, body)
	if err != nil {
		return SendResult{}, err
	}
	if required == "" && receiverCap {
		required = protocol.CapReplyReceiver
	}
	if required != "" {
		key, err := a.sendKey(ctx, env.To)
		if err == nil && required == protocol.CapAgentReaction && capturedFP != "" && key.Fingerprint() != capturedFP {
			// Sealed for the reader key captured at enqueue: never for its replacement.
			a.store.setOutboxState(env.ID, stateNotDelivered, "not sent: the reader's key changed", "")
			return SendResult{ID: env.ID, State: stateNotDelivered}, nil
		}
		if err == nil {
			err = a.requireParticipationCaps(ctx, key, required)
			if err == nil && receiverCap && required != protocol.CapReplyReceiver {
				err = a.requireParticipationCaps(ctx, key, protocol.CapReplyReceiver)
			}
			// Progress needs prg1 and, besides, whatever its author's
			// identity or participation already needs: never one without the other.
			if err == nil && progress && required != protocol.CapProgress {
				err = a.requireParticipationCaps(ctx, key, protocol.CapProgress)
			}
			if err == nil && progress && required == protocol.CapProgress && agentID != "" {
				err = a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
			}
			if err == nil && required == protocol.CapAgentReaction { // and what the assistant's own reply needs there
				err = a.assistantReactionCaps(ctx, key, conv, pid, agentID)
			}
			if err == nil && required == protocol.CapAgentReaction && humanRaw != "" { // to a captured audience: as a human-audience turn
				err = a.requireParticipationCaps(ctx, key, protocol.CapHumanParticipation)
			}
			control := env.V == envelope.Version3 && groupControlSub(sub)
			status := sub == envelope.SubStatus
			var historical HistoryItem
			if sub == envelope.SubHistory {
				var item HistoryItem
				if json.Unmarshal([]byte(body), &item) == nil {
					control = groupControlSub(item.Sub)
					status = item.Sub == envelope.SubStatus
					historical = item
					pid = item.PID
				}
			}
			if err == nil && required == protocol.CapGroup && control {
				err = a.requireGroupControlCapability(ctx, key)
			}
			if err == nil && required == protocol.CapGroup && status {
				err = a.requireParticipationCaps(ctx, key, protocol.CapHeadless)
			}
			if err == nil && required == protocol.CapGroup && historical.PID != "" {
				err = a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity)
			}
			if err == nil && required == protocol.CapGroup && pid != "" {
				cap := protocol.CapAgentIdentity
				if sub == envelope.SubGroupProof || sub == envelope.SubGroupContext {
					cap = protocol.CapExternalParticipation
				} else if p, e := a.participation(conv, pid); e == nil && p.External && (p.Host.Address == env.To || historical.PID != "") {
					cap = protocol.CapExternalParticipation
				}
				err = a.requireParticipationCaps(ctx, key, cap)
			}
			// An assistant's reaction as history needs agr1 and what that
			// assistant's own output needs there, besides the copy's own
			// participation or group requirement: never to an older reader.
			if item, assistant := historyAssistant(body, conv); err == nil && sub == envelope.SubHistory && assistant {
				if required != protocol.CapAgentReaction {
					err = a.requireParticipationCaps(ctx, key, protocol.CapAgentReaction)
				}
				if err == nil {
					err = a.assistantReactionCaps(ctx, key, conv, item.PID, item.AgentID)
				}
			}
		}
		if err != nil {
			// Waiting copies release when the recipient's signed capabilities
			// change; progress is never sent unmarked to an older session.
			if (conv != "" || required == protocol.CapProgress || required == protocol.CapAgentReaction) && errors.Is(err, errAgentIdentityUnsupported) {
				return SendResult{ID: env.ID, State: stateConvWaiting, Detail: err.Error()}, a.store.setOutboxState(env.ID, stateConvWaiting, err.Error(), "")
			}
			if retryable(err) {
				return SendResult{ID: env.ID, State: stateQueued, Detail: err.Error()}, a.store.setOutboxState(env.ID, stateQueued, err.Error(), "")
			}
			a.store.setOutboxState(env.ID, stateFailed, err.Error(), "")
			return SendResult{ID: env.ID, State: stateFailed}, err
		}
	}
	if ok, err := a.mayDeliver(env); err != nil || !ok {
		state, _, _, _ := a.store.outboxState(env.ID)
		return SendResult{ID: env.ID, State: state}, err
	}
	if route != nil {
		r, err := a.sendDirect(ctx, env, *route)
		if err == nil {
			return a.handedOver(env, r.State, protocol.PathDirect)
		}
		a.Logf("direct delivery to %s failed (%v); using the Hub", route.Endpoint, err)
		if err := a.store.coolRoute(route.Endpoint, time.Now().Add(routeCooldown)); err != nil {
			a.Logf("route cooldown: %v", err)
		}
	}
	var r protocol.Receipt
	err = a.uploadAll(ctx, env)
	if err == nil && len(env.Blobs) > 0 {
		// Uploading files takes time: decide again, from what is stored
		// now, just before the message itself is handed over (a person
		// frozen meanwhile keeps it queued, its files uploaded).
		if ok, err := a.receiverOriginalMayDeliver(env); err != nil || !ok {
			state, _, _, _ := a.store.outboxState(env.ID)
			return SendResult{ID: env.ID, State: state}, err
		}
		if ok, err := a.mayDeliver(env); err != nil || !ok {
			state, _, _, _ := a.store.outboxState(env.ID)
			return SendResult{ID: env.ID, State: state}, err
		}
	}
	if err == nil {
		err = a.hub.do(ctx, "POST", "/v1/messages", env, &r)
	}
	switch {
	case err == nil:
		return a.handedOver(env, r.State, protocol.PathRelay)
	case retryable(err):
		return SendResult{ID: env.ID, State: stateQueued, Detail: err.Error()}, a.store.setOutboxState(env.ID, stateQueued, err.Error(), "")
	default:
		a.store.setOutboxState(env.ID, stateFailed, err.Error(), "")
		return SendResult{ID: env.ID, State: stateFailed}, err
	}
}

// beforeOutbox lets tests pause a send between spooling and the outbox write.
var beforeOutbox = func() {}

func (a *Agent) spoolLockPath() string { return filepath.Join(a.home, "spool.lock") }

// handedOver records who now holds the message, then frees the spool: the
// receipt is persisted before the sender's own copy of the files goes.
func (a *Agent) handedOver(env envelope.Envelope, state, path string) (SendResult, error) {
	if err := a.store.setOutboxState(env.ID, state, "", path); err != nil {
		return SendResult{}, err
	}
	a.releaseSpool(env)
	return SendResult{ID: env.ID, State: state, Path: path}, nil
}

// FlushOutbox retries every queued message once.
func (a *Agent) FlushOutbox(ctx context.Context) error {
	if _, err := a.holdEndedOutputs(""); err != nil {
		return err
	}
	envs, err := a.store.queued()
	if err != nil {
		return err
	}
	for _, env := range envs {
		if _, err := a.deliver(ctx, env, nil); err != nil && !retryable(err) {
			a.Logf("message %s to %s rejected: %v", env.ID, env.To, err)
		}
	}
	return nil
}

// waitReceipt asks the Hub to answer as soon as message id leaves custody,
// or with its current state after wait. A Hub without this endpoint answers
// 404 and the caller keeps what it knew.
func (a *Agent) waitReceipt(ctx context.Context, id string, wait time.Duration) (protocol.Receipt, error) {
	var r protocol.Receipt
	path := "/v1/messages/" + url.PathEscape(id) + "/wait?timeout=" + url.QueryEscape(wait.String())
	c := *a.hub
	c.timeout = wait + requestTimeout
	err := c.do(ctx, "GET", path, nil, &r)
	return r, err
}

// Status reports what is known about message id: for a direct delivery,
// the recipient's own receipt; otherwise what the Hub can prove. With wait,
// a message still in the Hub's custody is waited on for up to that long.
func (a *Agent) Status(ctx context.Context, id string, wait time.Duration) (protocol.Receipt, error) {
	state, path, found, err := a.store.outboxState(id)
	if err != nil {
		return protocol.Receipt{}, err
	}
	if found && path == protocol.PathDirect {
		return protocol.Receipt{ID: id, State: state, Path: path}, nil
	}
	var r protocol.Receipt
	if wait > 0 {
		r, err = a.waitReceipt(ctx, id, wait)
		var he *HubError
		if (errors.As(err, &he) && he.Status == 404 && he.Msg != "unknown message") || (err == nil && r.State == "") {
			wait = 0 // an older Hub without receipt waits, or no state given: ask plainly
		}
	}
	if wait <= 0 {
		err = a.hub.do(ctx, "GET", "/v1/messages/"+url.PathEscape(id), nil, &r)
	}
	if found && hubUnreachable(err) {
		return protocol.Receipt{ID: id, State: state, Path: path}, &LocalStatus{Cause: err}
	}
	if found {
		r.Path = protocol.PathRelay
		if err == nil && r.State != protocol.StateCustody && r.State != state {
			a.store.setOutboxState(id, r.State, "", "")
		}
	}
	return r, err
}

// LocalStatus says the Hub could not be reached at all, so the receipt
// beside it is only this device's own stored record of a message it sent:
// possibly stale, never evidence of current delivery.
type LocalStatus struct{ Cause error }

func (e *LocalStatus) Error() string {
	return "Hub not reachable; local record only: " + e.Cause.Error()
}
func (e *LocalStatus) Unwrap() error { return e.Cause }

// hubUnreachable reports a request that never reached the Hub: its
// connection could not even be dialed. Answers, TLS and key failures are
// never this.
func hubUnreachable(err error) bool {
	var op *net.OpError
	var he *HubError
	return err != nil && !errors.As(err, &he) && errors.As(err, &op) && op.Op == "dial"
}

// Inbox lists received messages, optionally marking the listed ones read.
func (a *Agent) Inbox(unreadOnly, markRead bool) ([]Message, error) {
	msgs, err := a.store.inbox(unreadOnly)
	if err != nil || !markRead {
		return msgs, err
	}
	ids := make([]string, len(msgs))
	for i, m := range msgs {
		ids[i] = m.ID
	}
	if err := a.store.markRead(ids); err != nil {
		return msgs, err
	}
	notifyDaemon(a.home)
	return msgs, nil
}

// Review lists received items waiting for the local human's decision: held
// questions, tasks awaiting acceptance, and items the responder marked
// needs_human. A review notice from another machine is not one (it reports
// decisions waiting there: Notices). Listing changes nothing, not even
// read state.
func (a *Agent) Review() ([]Message, error) {
	args := append(append([]any{}, reviewStates...), envelope.KindMessage, envelope.StatusReviewNotice)
	return a.store.messages(` WHERE `+inReview+` AND NOT (`+receivedNotice+`)`, args...)
}

// Notices lists the open review notices: reports from other machines that
// requests wait for a person's decision there. They are decided there;
// here they can only be read and resolved (Resolve).
func (a *Agent) Notices() ([]Message, error) {
	return a.store.messages(` WHERE state = ? AND (`+receivedNotice+`)`, stateNeedHuman, envelope.KindMessage, envelope.StatusReviewNotice)
}

// Fingerprints returns the pinned fingerprint ("" if none) and the one the
// directory currently offers.
func (a *Agent) Fingerprints(ctx context.Context, address string) (pinned, current string, err error) {
	p, _, found, err := a.store.peer(address)
	if err != nil {
		return "", "", err
	}
	if found {
		pinned = p.Fingerprint()
	}
	e, err := a.directory(ctx, address)
	if err != nil {
		return pinned, "", err
	}
	return pinned, e.Public.Fingerprint(), nil
}

// Trust pins the directory's current keys for address and promotes messages
// held because of a key change that now verify. Each message moves to the
// inbox atomically, so an interrupted Trust can simply be run again.
func (a *Agent) Trust(ctx context.Context, address string) (string, error) {
	return a.TrustKey(ctx, address, "")
}

// TrustKey is Trust for the one key the person compared: the key fetched
// from the directory is pinned only if its fingerprint is expect. Another
// key (it changed after the person looked) changes nothing. An empty expect
// trusts whatever the directory has now, as Trust does.
func (a *Agent) TrustKey(ctx context.Context, address, expect string) (string, error) {
	e, err := a.directory(ctx, address)
	if err != nil {
		return "", err
	}
	if got := e.Public.Fingerprint(); expect != "" && got != expect {
		return "", fmt.Errorf("%s's key is now %s, not the %s you compared; nothing was trusted: compare the new fingerprint", address, got, expect)
	}
	if err := a.store.pin(e.Public); err != nil {
		return "", err
	}
	held, err := a.store.held(address, reasonKeyChanged)
	if err != nil {
		return "", err
	}
	defer notifyDaemon(a.home) // promoted questions may be for the worker
	for _, env := range held {
		in, err := envelope.Open(env, a.id, a.Address, e.Public)
		if err != nil {
			a.Logf("held message %s still does not verify: %v", env.ID, err)
			continue
		}
		if in.V == envelope.Version2 { // a conversation message needs its conversation proof too
			if err := a.admitConv(ctx, env, in, e.Public, true); err != nil {
				return "", err
			}
			continue
		}
		if namedAgentFields(in) {
			if err := a.checkDeviceAgent(in, e.Public); err != nil {
				continue
			}
		}
		if err := a.store.promote(in, e.Public.Fingerprint()); err != nil {
			return "", err
		}
	}
	if err := a.flushReceipts(ctx); err != nil {
		return e.Public.Fingerprint(), fmt.Errorf("trusted, but receipts not yet sent (the daemon retries): %w", err)
	}
	return e.Public.Fingerprint(), nil
}

// Invite asks the Hub for an invite code for person label (admin only).
func (a *Agent) Invite(ctx context.Context, label string, ttl time.Duration, admin bool) (string, error) {
	if err := inviteTTL(ttl); err != nil {
		return "", err
	}
	var out struct{ Code string }
	err := a.hub.do(ctx, "POST", "/v1/admin/invites", protocol.InviteRequest{Label: label, TTL: ttl, Admin: admin}, &out)
	return out.Code, err
}

// inviteTTL refuses a lifetime the Hub would not keep (it would quietly
// give the invite a week instead), naming the allowed range.
func inviteTTL(ttl time.Duration) error {
	if ttl <= 0 || ttl > protocol.MaxInviteTTL {
		return fmt.Errorf("invite lifetime %s is out of range: choose more than 0 and at most %gh (30 days); nothing was created", ttl, protocol.MaxInviteTTL.Hours())
	}
	return nil
}

// BrowserInvite creates an invite for a browser invitation link (admin
// only). The Hub refuses before creating anything unless its advertised
// endpoint serves the browser messenger over platform TLS. The admin may
// still connect through an older pinned endpoint after that migration;
// its connection's pin says nothing about the endpoint in the new invite.
func (a *Agent) BrowserInvite(ctx context.Context, label string, ttl time.Duration, admin bool) (string, error) {
	if err := inviteTTL(ttl); err != nil {
		return "", err
	}
	var out struct{ Code string }
	if err := a.hub.do(ctx, "POST", "/v1/admin/invites", protocol.InviteRequest{Label: label, TTL: ttl, Admin: admin, Browser: true}, &out); err != nil {
		var he *HubError
		if errors.As(err, &he) && he.Status == 400 { // an older Hub refuses the unknown field
			return "", fmt.Errorf("%w (a Hub that cannot create browser invitations needs an update)", err)
		}
		return "", err
	}
	return out.Code, nil
}

// Revoke revokes address on the Hub (admin only).
func (a *Agent) Revoke(ctx context.Context, address string) error {
	return a.hub.do(ctx, "POST", "/v1/admin/revoke", protocol.RevokeRequest{Address: address}, nil)
}
