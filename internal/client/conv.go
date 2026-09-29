package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Human DMs: this installation's person, two-person conversations with a
// signed root (E0), and version 2 messages admitted once per (verifying
// key, logical id). A conversation question or task is held for the person
// (stateConvHeld), unless it is addressed to this device's agent within an
// agent participation (agentjob.go decides whether that runs); no
// conversation message enters the address-approved legacy worker.
//
// Trust: a person record is pinned the first time it is seen, verified
// against its device's pinned key (first contact is TOFU, as for keys); a
// different record later is a conflict and is frozen. A conversation root
// is pinned only from its creator's own device, verified by that key; the
// creator pins its own before its first send. Roots travel inside the
// encrypted message, bounded. Proof that is not complete yet (a root seen
// first from the other member, a person record not yet published) holds
// the message, and new evidence (a members push, a new connection) looks
// again; nothing polls.

// ErrNotPublished means a person was created here but the Hub does not hold
// it yet; the daemon publishes it when it connects.
var ErrNotPublished = errors.New("not yet published on the Hub (the daemon publishes it when it connects)")

// Upkeep that an event made due, done on the next sync (never on a timer).
const (
	convPublish   uint32 = 1 << iota // publish this run's capabilities and the person, if not yet
	convRetry                        // new evidence: look again at every message held for proof, from the start
	convRetryMore                    // continue that look from where the last page ended
	convRelease                      // look again at waiting conversation messages
	convPersons                      // compare published person records with the pinned ones
	convHistory                      // queue the next page of history for a new device of this person (history.go)
)

// proofPage bounds the held messages looked at in one sync.
const proofPage = 50

type convWork struct {
	bits atomic.Uint32
	mu   sync.Mutex
	pos  heldPos // where the look at held messages continues
}

func (w *convWork) due(b uint32) { w.bits.Or(b) }
func (w *convWork) take() uint32 { return w.bits.Swap(0) }

// convSync does the conversation upkeep that is due. It makes no request
// when nothing is due, so the regular ping costs nothing extra.
func (a *Agent) convSync(ctx context.Context) {
	work := a.convWork.take()
	if work == 0 {
		return
	}
	feats, err := a.relayFeatures(ctx)
	if err != nil {
		a.convWork.due(work) // the next sync, on this connection's next event
		return
	}
	if work&convPublish != 0 {
		a.retryApprovedLinks(ctx)
		if err := a.publishOwn(ctx, feats); err != nil {
			a.Logf("publishing person and capabilities: %v", err)
			if retryable(err) {
				a.convWork.due(convPublish)
			}
		}
	}
	if work&convPersons != 0 { // before retrying held messages: a conflict must hold them
		a.checkPersons(ctx, a.MemberView().Members)
	}
	if work&(convRetry|convRetryMore) != 0 {
		if work&convRetry != 0 {
			a.convWork.mu.Lock()
			a.convWork.pos = heldPos{}
			a.convWork.mu.Unlock()
		}
		if a.retryProof(ctx) {
			// More pages: continue on the next sync, which this kicks now,
			// so new evidence reaches every held message without waiting
			// for another event; each sync does one bounded page.
			a.convWork.due(convRetryMore)
			if a.kick != nil {
				a.kick()
			}
		}
	}
	if work&convRelease != 0 {
		a.releaseConv(ctx, feats)
	}
	if work&convHistory != 0 && a.historyStep(ctx) {
		a.convWork.due(convHistory) // one page per sync; the next follows at once
		if a.kick != nil {
			a.kick()
		}
	}
}

// relayFeatures asks the Hub what it supports.
func (a *Agent) relayFeatures(ctx context.Context) ([]string, error) {
	var v protocol.VersionInfo
	err := a.hub.do(ctx, "GET", "/v1/version", nil, &v)
	return v.Features, err
}

// publishOwn publishes this run's capability record and, once per roster,
// this installation's person.
func (a *Agent) publishOwn(ctx context.Context, feats []string) error {
	if slices.Contains(feats, protocol.FeatureCaps) && a.session != "" {
		rec := protocol.CapsRecord{Address: a.Address, Session: a.session, Caps: []string{protocol.CapEnv2, protocol.CapNotify, protocol.CapPerson}, TS: time.Now().Unix()}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(ctx, "PUT", "/v1/caps", rec, nil); err != nil {
			return err
		}
	}
	if slices.Contains(feats, protocol.FeaturePerson) {
		return a.publishPerson(ctx)
	}
	return nil
}

// convSupport reports whether a conversation message can go to the device
// at address now: the relay carries version 2 and the device's signed
// capabilities, for every session the relay lists, include it. notify
// reports that the attention hint may go too: the relay takes it and every
// listed session reads it.
func (a *Agent) convSupport(ctx context.Context, address string, key identity.Public, feats []string) (ok bool, why string, notify bool) {
	if !slices.Contains(feats, protocol.FeatureEnv2) || !slices.Contains(feats, protocol.FeatureCaps) {
		return false, "your Hub cannot carry conversations (it needs an update)", false
	}
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return false, err.Error(), false
	}
	var prof protocol.Profile
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		return false, "cannot ask the Hub what " + address + " can read: " + err.Error(), false
	}
	if r, err := protocol.ParsePersonRoster(prof.Person); err == nil { // fresh evidence, checked before anything is sent
		a.observeRef(ctx, &protocol.PersonRef{ID: r.Person, Seq: r.Seq, Hash: r.Hash()})
	}
	if !prof.Supports(address, key.SignKey, protocol.CapEnv2) || !prof.Supports(address, key.SignKey, protocol.CapPerson) {
		return false, address + " needs to update AgentNet before it can take part in conversations (an older program, or it has not connected since updating)", false
	}
	return true, "", slices.Contains(feats, protocol.FeatureNotify) && prof.Supports(address, key.SignKey, protocol.CapNotify)
}

// asksAttention reports whether in is a turn meant for its recipient's
// attention (docs/revival/NOTIFY.md §1): a message, question or task a
// person typed, or an invited agent's answer or result. Events, excerpts,
// replicas and everything else stay quiet.
func asksAttention(in envelope.Inner) bool {
	if in.V != envelope.Version2 || in.Sub != "" || in.Replica {
		return false
	}
	switch {
	case in.Origin == envelope.OriginUI:
		return in.Kind == envelope.KindMessage || in.Kind == envelope.KindQuestion || in.Kind == envelope.KindTask
	case envelope.AgentOrigin(in.Origin):
		return in.PID != "" && (in.Kind == envelope.KindAnswer || in.Kind == envelope.KindResult)
	}
	return false
}

// CreateDM starts a new two-person conversation with the person that the
// device at address speaks for. Each call starts a separate conversation.
func (a *Agent) CreateDM(ctx context.Context, address string) (string, error) {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("set up your person first (agentnet person create NAME, or link this device to it)")
	}
	feats, err := a.relayFeatures(ctx)
	if err != nil {
		return "", err
	}
	for _, f := range []string{protocol.FeatureEnv2, protocol.FeaturePerson, protocol.FeatureCaps} {
		if !slices.Contains(feats, f) {
			return "", errors.New("your Hub cannot carry conversations (it needs an update)")
		}
	}
	key, err := a.sendKey(ctx, address)
	if err != nil {
		return "", err
	}
	them, err := a.personOfKey(ctx, address, key)
	if err != nil {
		return "", err
	}
	if them.info.Person == me.info.Person {
		return "", errors.New("that is your own person")
	}
	if ok, why, _ := a.convSupport(ctx, address, key, feats); !ok {
		return "", errors.New(why)
	}
	if !a.personSendable(address, them.info.Roster) { // the profile just read may have frozen it
		return "", errPersonConflict
	}
	members := []protocol.ConvMember{{Person: me.info.Person, Roster: me.info.Roster}, {Person: them.info.Person, Roster: them.info.Roster}}
	slices.SortFunc(members, func(x, y protocol.ConvMember) int {
		if x.Person < y.Person {
			return -1
		}
		return 1
	})
	root := protocol.ConvRoot{V: protocol.ConvRootVersion, Kind: protocol.ConvKindDM,
		Creator: protocol.ConvCreator{Person: me.info.Person, Roster: me.info.Roster, Address: a.Address, Fingerprint: me.info.Fingerprint},
		Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix()}
	root.Sign(a.id.Sign)
	raw, _ := json.Marshal(root)
	if len(raw) > protocol.MaxConvRoot {
		return "", errors.New("conversation root too large")
	}
	if err := a.store.addConversation(root, raw, them.info.Person); err != nil {
		return "", err
	}
	return root.ID(), nil
}

// Conversations lists the conversations this installation holds.
func (a *Agent) Conversations() ([]ConversationInfo, error) { return a.store.conversations() }

// ConversationMessages lists a conversation's messages here, oldest first.
func (a *Agent) ConversationMessages(conv string) ([]ConvMessage, error) {
	return a.store.convMessages(conv, a.Address, a.id.Public(a.Address).Fingerprint(), a.ownDevices())
}

// ConvOutgoing is a message to send in a conversation.
type ConvOutgoing struct {
	Kind    string // message (default), question or task
	Body    string
	ReplyTo string
	Origin  string           // envelope.OriginUI (default) or "agent:<harness>"
	Emotion string           // required with an agent origin
	Target  *envelope.Target // the one execution recipient of a question or task, if any
	PID     string           // the agent participation (AskAgent sets it with the target)
	Files   []OutgoingFile   // files to attach (a turn only): encrypted to the recipient while sending

	stored  func()                                 // the message and its files are stored: the spool is theirs
	sub     string                                 // envelope.SubEvent for participation events (participation.go)
	status  string                                 // an agent output's status (agentjob.go)
	claim   func(tx *sql.Tx, replyID string) error // decides, with the outbox write, that it may be stored (agentjob.go)
	selfJob bool                                   // a request to this device's own agent: its job is recorded with it
}

// ConvSent is what became of a conversation message: one copy per device
// (the other member's devices, and this person's other devices).
type ConvSent struct {
	ID, LID string // ID: the first copy's envelope
	State   string // the least advanced copy's: custody, delivered, queued, or waiting (kept: that device cannot read it now)
	Detail  string
	Copies  []ConvCopy
}

// ConvCopy is one device's copy of a sent conversation message.
type ConvCopy struct {
	ID     string `json:"id"`
	To     string `json:"to"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// outCopy is one device's copy being stored.
type outCopy struct {
	env   envelope.Envelope
	in    envelope.Inner
	state string
	why   string
}

// SendConv sends m in conversation conv: one copy to each current device of
// the other member and of this person's other devices, all with one
// logical id. A device that cannot read conversations now gets its copy
// kept as waiting, sent when it can; none is ever sent as version 1. Copies
// to this person's own devices are replicas (history: never executed),
// except the one to a request's execution target.
func (a *Agent) SendConv(ctx context.Context, conv string, m ConvOutgoing) (ConvSent, error) {
	root, raw, found, err := a.store.conversation(conv)
	if err != nil {
		return ConvSent{}, err
	}
	if !found {
		return ConvSent{}, fmt.Errorf("no conversation %s here", conv)
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return ConvSent{}, err
	}
	if r, member := root.Member(me.info.Person); !ok || !member || !a.store.inChain(me.info.Person, r) {
		return ConvSent{}, errors.New("this installation does not speak for a member of that conversation")
	}
	var peerID string
	for _, mem := range root.Members {
		if mem.Person != me.info.Person {
			peerID = mem.Person
		}
	}
	if m.Kind == "" {
		m.Kind = envelope.KindMessage
	}
	if m.Origin == "" {
		m.Origin = envelope.OriginUI
	}
	if m.ReplyTo != "" { // a reply stays within its own conversation
		if c, err := a.store.convOf(m.ReplyTo); err != nil {
			return ConvSent{}, err
		} else if c != conv {
			return ConvSent{}, fmt.Errorf("message %s is not in this conversation: a reply stays within its conversation", m.ReplyTo)
		}
	}
	if len(m.Files) > 0 {
		if m.sub != "" || m.PID != "" || m.claim != nil || m.selfJob {
			return ConvSent{}, errors.New("files go only with a message, question or task a person sends")
		}
		if len(m.Files) > envelope.MaxAttachments {
			return ConvSent{}, fmt.Errorf("at most %d attachments per message", envelope.MaxAttachments)
		}
	} else if m.Body == "" && m.sub == "" {
		return ConvSent{}, errors.New("nothing to send: no text and no files")
	}
	// Fresh evidence first: newer roster steps of both persons decide the
	// devices, and each device's profile what it can read.
	feats, ferr := a.relayFeatures(ctx)
	if ferr == nil {
		for _, id := range []string{peerID, me.info.Person} {
			if _, err := a.refreshPerson(ctx, id, false); errors.Is(err, errPersonConflict) {
				return ConvSent{}, errPersonConflict
			} else if err != nil && !retryable(err) {
				a.Logf("person %s: %v", id, err)
			}
		}
	}
	if me, ok, err = a.store.selfPerson(a.Address); err != nil || !ok {
		return ConvSent{}, errors.New("this installation no longer speaks for a member of that conversation")
	}
	peer, ok, err := a.store.personByID(peerID)
	if err != nil {
		return ConvSent{}, err
	}
	if !ok {
		return ConvSent{}, errors.New("the other member's person record is not pinned here")
	}
	if peer.info.State == personConflict {
		return ConvSent{}, errPersonConflict
	}
	fan := []envelope.Fan{{Person: me.info.Person, Roster: me.info.Roster}, {Person: peer.info.Person, Roster: peer.info.Roster}}
	var devices []identity.Public
	own := map[string]bool{}
	for _, d := range peer.roster.Devices {
		devices = append(devices, d)
	}
	for _, d := range me.roster.Devices {
		if d.Address != a.Address {
			devices, own[d.Address] = append(devices, d), true
		}
	}
	lid := protocol.NewID()
	var copies []outCopy
	if len(m.Files) > 0 {
		// Files go with a turn a person sends, encrypted to each device; the
		// ciphertext waits in the private spool until the Hub holds the
		// message (files.go). Cleanup must not see it before the outbox
		// refers to it.
		release, err := lockfile.Wait(a.spoolLockPath())
		if err != nil {
			return ConvSent{}, err
		}
		stored := false
		defer func() {
			if !stored {
				for _, c := range copies {
					a.releaseSpool(envelope.Envelope{ID: c.in.ID, Blobs: blobsOf(c.in.Attachments)})
				}
			}
			release()
		}()
		m.stored = func() { stored = true; release() } // idempotent release, before delivery
	}
	for _, dev := range devices {
		key, err := a.sendKey(ctx, dev.Address)
		if err == nil && key.Fingerprint() != dev.Fingerprint() {
			err = fmt.Errorf("%s's key is not the one its person's roster names", dev.Address)
		}
		if err != nil {
			a.Logf("conversation copy for %s not sent: %v", dev.Address, err)
			continue
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ConvSent{}, err
		}
		target := m.Target != nil && m.Target.Address == dev.Address && m.Target.Fingerprint == dev.Fingerprint()
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(),
			Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Conv: conv, LID: lid, Root: raw, Replica: own[dev.Address] && !target,
			Origin: m.Origin, Emotion: m.Emotion, Target: m.Target, PID: m.PID, Sub: m.sub, Status: m.status, Fan: fan}
		copies = append(copies, outCopy{in: in}) // listed before spooling, so a failure releases what it spooled
		c := &copies[len(copies)-1]
		for _, f := range m.Files {
			att, err := a.spoolNamed(f, recipient)
			if err != nil {
				return ConvSent{}, err
			}
			c.in.Attachments = append(c.in.Attachments, att)
		}
		supported, why, notify := false, "cannot reach the Hub", false
		if ferr == nil {
			supported, why, notify = a.convSupport(ctx, dev.Address, key, feats)
		} else {
			why += ": " + ferr.Error()
		}
		if notify && !own[dev.Address] && asksAttention(c.in) {
			c.env, err = envelope.SealAttention(c.in, a.id.Sign, recipient, protocol.NotifyChannel(conv, dev.Fingerprint()))
		} else {
			c.env, err = envelope.Seal(c.in, a.id.Sign, recipient)
		}
		if err != nil {
			return ConvSent{}, err
		}
		c.state, c.why = stateQueued, ""
		if !supported {
			c.state, c.why = stateConvWaiting, why
		}
	}
	if len(copies) == 0 && !m.selfJob {
		return ConvSent{}, errors.New("no device of the conversation can be sent a copy now")
	}
	if now, _, err := a.store.personByID(peerID); err != nil {
		return ConvSent{}, err
	} else if now.info.State == personConflict { // a profile just showed a different record
		return ConvSent{}, errPersonConflict
	}
	jobKey := ""
	if m.selfJob {
		jobKey = me.info.Fingerprint
	}
	local := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: a.Address, TS: time.Now().Unix(),
		Kind: m.Kind, Body: m.Body, Conv: conv, LID: lid, Origin: m.Origin, Target: m.Target, PID: m.PID, Fan: fan}
	if err := a.store.addConvOutbox(copies, local, m.claim, jobKey); err != nil {
		return ConvSent{}, err
	}
	if m.stored != nil {
		m.stored()
	}
	defer notifyDaemon(a.home)
	sent := ConvSent{LID: lid, State: protocol.StateDelivered}
	if len(copies) > 0 {
		sent.ID = copies[0].env.ID
	} else {
		sent.ID = local.ID
	}
	for _, c := range copies {
		cp := ConvCopy{ID: c.env.ID, To: c.in.To, State: c.state, Detail: c.why}
		if c.state == stateQueued {
			res, err := a.deliver(ctx, c.env, nil)
			switch {
			case err == nil:
				cp.State, cp.Detail = res.State, res.Detail
			case retryable(err):
				cp.Detail = err.Error()
			default:
				cp.State, cp.Detail = stateFailed, err.Error()
			}
		}
		sent.Copies = append(sent.Copies, cp)
		if rank(cp.State) < rank(sent.State) {
			sent.State, sent.Detail = cp.State, cp.Detail
		}
	}
	return sent, nil
}

// rank orders copy states from least to most advanced.
func rank(state string) int {
	switch state {
	case stateFailed:
		return 0
	case stateConvWaiting:
		return 1
	case stateQueued:
		return 2
	case protocol.StateCustody:
		return 3
	}
	return 4 // delivered, or a later state
}

// personSendable reports whether the person on the device at address is
// pinned and not frozen (and, if roster is given, still that roster).
func (a *Agent) personSendable(address, roster string) bool {
	p, ok, err := a.store.personByAddress(address)
	return err == nil && ok && (p.info.State == personPinned || p.info.State == personSelf) && (roster == "" || p.info.Roster == roster)
}

// releaseConv queues waiting conversation messages whose recipient can now
// read them; the sync's outbox flush sends them. Agent outputs that may no
// longer go out are held back first.
func (a *Agent) releaseConv(ctx context.Context, feats []string) {
	if _, err := a.holdEndedOutputs(""); err != nil {
		a.Logf("agent outputs: %v", err)
		return
	}
	waiting, err := a.store.convWaiting()
	if err != nil || len(waiting) == 0 {
		return
	}
	checked := map[string]bool{}
	for id, to := range waiting {
		ok, seen := checked[to]
		if !seen {
			key, _, found, err := a.store.peer(to)
			if err == nil && found {
				ok, _, _ = a.convSupport(ctx, to, key, feats)
			}
			// Support alone is not enough: the profile just read may have
			// frozen the person, and a frozen person gets nothing.
			ok = ok && a.personSendable(to, "")
			checked[to] = ok
		}
		if ok {
			if err := a.store.releaseWaiting(id); err != nil {
				a.Logf("conversation message %s: %v", id, err)
			}
		}
	}
}

// retryProof looks again at one page of messages held for conversation
// proof, continuing from where the previous page ended, and reports whether
// more follow. Messages still without proof stay held, in place.
func (a *Agent) retryProof(ctx context.Context) (more bool) {
	a.convWork.mu.Lock()
	pos := a.convWork.pos
	a.convWork.mu.Unlock()
	envs, next, err := a.store.heldAfter(reasonProof, pos, proofPage)
	if err != nil {
		a.Logf("held conversation messages: %v", err)
		return false
	}
	more = len(envs) == proofPage
	if !more {
		next = heldPos{} // the end: the next look starts from the beginning
	}
	a.convWork.mu.Lock()
	a.convWork.pos = next
	a.convWork.mu.Unlock()
	for _, env := range envs {
		sender, _, found, err := a.store.peer(env.From)
		if err != nil || !found {
			continue
		}
		in, err := envelope.Open(env, a.id, a.Address, sender)
		if err != nil || in.V != envelope.Version2 {
			continue
		}
		if err := a.admitConv(ctx, env, in, sender, true); err != nil && retryable(err) {
			a.convWork.due(convRetry) // the Hub is out of reach: look again from the start next time
			return false
		}
	}
	return more
}

// admitConv admits a verified version 2 message, or holds it (quarantine)
// with the reason it cannot be admitted yet. fromQuarantine: env is held
// already, and is released when admitted.
func (a *Agent) admitConv(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, fromQuarantine bool) error {
	hold := func(reason, why string) error {
		a.Logf("conversation message %s from %s held (%s): %s", env.ID, env.From, reason, why)
		return a.store.holdAs(env, reason)
	}
	// personErr holds the message for a person problem; a failure to ask
	// the Hub is returned instead, so the message is delivered again.
	personErr := func(err error) error {
		var he *HubError
		switch {
		case errors.Is(err, errPersonConflict):
			return hold(reasonConflict, err.Error())
		case errors.Is(err, ErrNoPerson), errors.Is(err, errPersonRecord), errors.As(err, &he) && !retryable(err):
			return hold(reasonProof, err.Error())
		}
		return err
	}
	root, err := protocol.ParseConvRoot(in.Root)
	if err != nil || root.ID() != in.Conv {
		return hold(reasonInvalid, "its conversation root does not match the conversation")
	}
	if env.Attn && env.Chan != protocol.NotifyChannel(in.Conv, a.id.Public(a.Address).Fingerprint()) {
		// Only a hint for the Hub's alerts; nothing here routes by it.
		a.Logf("conversation message %s from %s: its notification channel is not this conversation's", env.ID, env.From)
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return hold(reasonInvalid, "this installation has no person")
	}
	if r, member := root.Member(me.info.Person); !member {
		return hold(reasonInvalid, "this installation's person is not a member")
	} else if ok, err := a.boundIn(ctx, me.info.Person, r); err != nil {
		return personErr(err)
	} else if !ok {
		return hold(reasonInvalid, "the root binds this person to a roster step it never had")
	}
	sp, err := a.personOfKey(ctx, env.From, sender)
	if err != nil {
		return personErr(err)
	}
	if r, member := root.Member(sp.info.Person); !member {
		return hold(reasonInvalid, "the sender is not a member of this conversation")
	} else if ok, err := a.boundIn(ctx, sp.info.Person, r); err != nil {
		return personErr(err)
	} else if !ok {
		return hold(reasonInvalid, "the root binds the sender's person to a roster step its chain does not have")
	}
	if in.Sub == envelope.SubHistory && sp.info.Person != me.info.Person {
		return hold(reasonInvalid, "history comes only from a device of this installation's own person")
	}
	if _, _, found, err := a.store.conversation(in.Conv); err != nil {
		return err
	} else if !found {
		// Any member device may bring the root; it must verify under the
		// creator device's key as its person's chain lists it at the step
		// the root names.
		if err := a.verifyRoot(ctx, root, sp); errors.Is(err, errRootInvalid) {
			return hold(reasonInvalid, err.Error())
		} else if err != nil {
			return personErr(err)
		}
		// Every member pinned, bound as the root says: a root another
		// device of this person brought may name someone never seen here.
		for _, m := range root.Members {
			if ok, err := a.boundIn(ctx, m.Person, m.Roster); err != nil {
				return personErr(err)
			} else if !ok {
				return hold(reasonInvalid, "the root binds a member to a roster step its chain does not have")
			}
		}
		peer := root.Members[0].Person
		if peer == me.info.Person {
			peer = root.Members[1].Person
		}
		if err := a.store.addConversation(root, in.Root, peer); err != nil {
			return err
		}
	}
	if in.Sub == envelope.SubHistory {
		return a.admitHistory(ctx, env, in, root, hold, fromQuarantine)
	}
	if in.ReplyTo != "" { // a reply may name only a message of its own conversation
		c, err := a.store.convOf(in.ReplyTo)
		if err != nil {
			return err
		}
		if known, err := a.store.knownMessage(in.ReplyTo); err != nil {
			return err
		} else if known && c != in.Conv {
			return hold(reasonInvalid, "it replies to a message outside its conversation")
		}
	}
	var also func(*sql.Tx) error
	if in.Sub == envelope.SubEvent { // a participation record, stored with its message or not at all
		ev, err := checkParticipationEvent(in, sender.Fingerprint(), sender.SignKey)
		if err != nil {
			return hold(reasonInvalid, err.Error())
		}
		raw := []byte(in.Body)
		also = func(tx *sql.Tx) error { return insertParticipationEvent(tx, ev, raw) }
	}
	now, event := time.Now(), also
	forward := a.forwardStale(me, in, sender.Fingerprint(), in.Root) // for this person's devices its sender did not know
	also = func(tx *sql.Tx) error {                                  // with the message, or not at all
		if event != nil {
			if err := event(tx); err != nil {
				return err
			}
		}
		if err := insertCopies(tx, forward); err != nil {
			return err
		}
		if sp.info.Person == me.info.Person {
			return nil // this person's own message, from another of its devices: never an alert
		}
		return queueAlert(tx, in, sender.Fingerprint(), now)
	}
	state := ""
	if in.Kind == envelope.KindQuestion || in.Kind == envelope.KindTask {
		switch t := in.Target; {
		case in.PID == "":
			state = stateConvHeld // for the person: nothing runs it
		case !in.Replica && t != nil && t.Address == a.Address && t.Fingerprint == a.id.Public(a.Address).Fingerprint():
			state = stateAgentWaiting // for this device's agent: the worker decides when it may run (agentjob.go)
		default:
			// A request to another device's agent: history here.
		}
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), state, fromQuarantine, also)
	if errors.Is(err, errTooManyEvents) {
		return hold(reasonInvalid, err.Error())
	}
	if err != nil {
		return err
	}
	if res == admitConflict {
		return hold(reasonDuplicate, "a message with the same key and logical id but other content is stored")
	}
	if res == admitted {
		a.wakeWorker() // a request, or an event that may let one run or stop
		a.wakeAlerts()
		if len(forward) > 0 {
			a.kickNow() // the stream's worker sends them now, not at the next ping
		}
	}
	return nil
}

// boundIn reports whether hash, the roster step a root binds person to, is
// in person's pinned chain, fetching newer steps once if it is not.
func (a *Agent) boundIn(ctx context.Context, person, hash string) (bool, error) {
	if a.store.inChain(person, hash) {
		return true, nil
	}
	if _, err := a.refreshPerson(ctx, person, false); err != nil {
		return false, err
	}
	return a.store.inChain(person, hash), nil
}

// verifyRoot checks root under its creator device's key, as the creator
// person's pinned chain lists that device at the step the root names; the
// creator person is pinned first if needed (sender is the device that
// brought the root, pinned already).
func (a *Agent) verifyRoot(ctx context.Context, root protocol.ConvRoot, sender personRow) error {
	c := root.Creator
	if sender.info.Person != c.Person {
		if _, ok, err := a.store.personByID(c.Person); err != nil {
			return err
		} else if !ok {
			key, err := a.sendKey(ctx, c.Address)
			if err != nil {
				return err
			}
			if _, err := a.personOfKey(ctx, c.Address, key); err != nil {
				return err
			}
		}
	}
	if ok, err := a.boundIn(ctx, c.Person, c.Roster); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("%w: the root's creator step is not in its person's chain", errRootInvalid)
	}
	step, _, err := a.store.chainStep(c.Person, c.Roster)
	if err != nil {
		return err
	}
	dev, ok := step.Device(c.Fingerprint)
	if !ok || dev.Address != c.Address {
		return fmt.Errorf("%w: the root's creator device is not in that step", errRootInvalid)
	}
	if err := root.Verify(dev.SignKey); err != nil {
		return fmt.Errorf("%w: %v", errRootInvalid, err)
	}
	return nil
}

// errRootInvalid means a conversation root does not verify.
var errRootInvalid = errors.New("conversation root invalid")

// ownDevices names every device of this installation's person, in any
// step of its pinned chain.
func (a *Agent) ownDevices() map[string]bool {
	own := map[string]bool{}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return own
	}
	rows, err := a.store.db.Query(`SELECT record FROM person_chain WHERE person = ?`, me.info.Person)
	if err != nil {
		return own
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var r protocol.PersonRoster
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &r) == nil {
			for _, d := range r.Devices {
				own[d.Address] = true
			}
		}
	}
	return own
}
