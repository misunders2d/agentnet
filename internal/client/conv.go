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
	convServe                        // answer a file request from another device of this person (historyfiles.go)
	convFetch                        // keep received conversation files (historyfiles.go)
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
		a.recoverHumanExcerpts(ctx)
		a.discloseHumanAudience(ctx)
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
		a.kickNow()
	}
	if work&convServe != 0 && a.serveFiles(ctx) {
		a.convWork.due(convServe) // one file per sync
		a.kickNow()
	}
	if work&convFetch != 0 && a.prefetchFiles(ctx) {
		a.convWork.due(convFetch) // one file per sync
		a.kickNow()
	}
}

// relayFeatures asks the Hub what it supports.
func (a *Agent) relayFeatures(ctx context.Context) ([]string, error) {
	var v protocol.VersionInfo
	err := a.hub.do(ctx, "GET", "/v1/version", nil, &v)
	return v.Features, err
}

// ownCaps is what a session of this program reads (sorted, as the relay
// requires). Every session of a device advertises the same list: the
// relay takes a device to support only what ALL its live sessions do, so a
// session that said less (link.go's waiting one) would keep senders
// holding controls, Drive records and statuses for it. It is at most
// protocol.MaxAdvertisedCaps long; rm1 (protocol.CapRoom) says this program
// enforces every room reader rule (ROOM_V1 §2.1).
var ownCaps = []string{protocol.CapAgentIdentity, protocol.CapAgentReaction, protocol.CapExternalParticipation, protocol.CapConvClear, protocol.CapControl, protocol.CapDriveSpace, protocol.CapEnv2, protocol.CapGroup, protocol.CapHeadless, protocol.CapHumanParticipation, protocol.CapNotify, protocol.CapPerson, protocol.CapProgress, protocol.CapReplyReceiver, protocol.CapRoom, protocol.CapTyping}

// publishOwn publishes this run's capability record and, once per roster,
// this installation's person.
func (a *Agent) publishOwn(ctx context.Context, feats []string) error {
	if slices.Contains(feats, protocol.FeatureCaps) && a.session != "" {
		rec := protocol.CapsRecord{Address: a.Address, Session: a.session, Caps: ownCaps, TS: time.Now().Unix()}
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
		return false, WaitServerUpdate + "your Hub cannot carry conversations (it needs an update)", false
	}
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return false, err.Error(), false
	}
	var prof protocol.Profile
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		return false, WaitServerUnavailable + "cannot ask the Hub what " + address + " can read: " + err.Error(), false
	}
	if r, err := protocol.ParsePersonRoster(prof.Person); err == nil { // fresh evidence, checked before anything is sent
		a.observeRef(ctx, &protocol.PersonRef{ID: r.Person, Seq: r.Seq, Hash: r.Hash()})
	}
	if !prof.Supports(address, key.SignKey, protocol.CapEnv2) || !prof.Supports(address, key.SignKey, protocol.CapPerson) {
		return false, WaitPeerUpdate + address + " needs to update AgentNet before it can take part in conversations (an older program, or it has not connected since updating)", false
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
func (a *Agent) Conversations() ([]ConversationInfo, error) {
	rows, err := a.store.conversations()
	if err != nil {
		return nil, err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		root, _, found, err := a.store.conversation(rows[i].ID)
		if err != nil {
			return nil, err
		}
		if !found || !externalDM(root) {
			continue
		}
		rows[i].Role = "visitor"
		if _, member := root.Member(me.info.Person); ok && member {
			rows[i].Role = "member"
			continue
		}
		// A guest or visitor host sees both original people, as pinned and
		// verified against the unchanged root (dmMembers), never a guess
		// from the stored peer or an inviter. Display only: no authority.
		m, err := a.dmMembers(rows[i].ID)
		if err != nil {
			return nil, err
		}
		for _, mem := range root.Members {
			if p, ok := m.persons[mem.Person]; ok {
				rows[i].Members = append(rows[i].Members, p.info)
			}
		}
	}
	out, err := a.projectGroupConversations(rows)
	if err != nil {
		return nil, err
	}
	selfFP := a.Self().Fingerprint()
	for i := range out {
		out[i].Deleted = a.store.convDeleted(out[i].ID, selfFP)
	}
	return out, nil
}

// ConversationMessages lists a conversation's messages here, oldest first.
func (a *Agent) ConversationMessages(conv string) ([]ConvMessage, error) {
	msgs, err := a.store.convMessages(conv, a.Address, a.id.Public(a.Address).Fingerprint(), a.ownDevices())
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		dir := msgs[i].Dir
		if dir == "out" && msgs[i].Via != "" {
			dir = "in"
		}
		h, e := storedHuman(a.store.db, dir, msgs[i].ID)
		if e != nil {
			return nil, e
		}
		msgs[i].Human = h
	}
	msgs = a.showExcerpts(msgs)
	a.verifyAgents(conv, msgs)
	for i := range msgs {
		// A turn sent from this device opens from its kept copy; one sent
		// from another device of this person (Via) is a received copy.
		a.markOpenable(msgs[i].Attachments, msgs[i].Dir == "out" && msgs[i].Via == "")
	}
	return msgs, a.decorateConv(conv, msgs)
}

// ConvOutgoing is a message to send in a conversation.
type ConvOutgoing struct {
	Kind          string // message (default), question or task
	Body          string
	ReplyTo       string
	Quote         string
	Origin        string           // envelope.OriginUI (default) or "agent:<harness>"
	Emotion       string           // required with an agent origin
	Target        *envelope.Target // the one execution recipient of a question or task, if any
	PID           string           // the agent participation (AskAgent sets it with the target)
	AgentID       string           // named executor author on an answer/result
	Files         []OutgoingFile   // files to attach (a turn only): encrypted to the recipient while sending
	ReplyReceiver *ReplyReceiver   // private local return selection

	stored  func()                                 // the message and its files are stored: the spool is theirs
	sub     string                                 // envelope.SubEvent for participation events (participation.go)
	status  string                                 // an agent output's status (agentjob.go)
	claim   func(tx *sql.Tx, replyID string) error // decides, with the outbox write, that it may be stored (agentjob.go)
	human   *envelope.HumanTurn
	selfJob bool // a request to this device's own agent: its job is recorded with it
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
	Person string `json:"person,omitempty"`
	Own    bool   `json:"own,omitempty"` // in a conversation's view: to another device of this person
}

// SentCopies lists the copies this device sent of the conversation message
// with logical id lid, as stored (none: no such message sent here).
func (a *Agent) SentCopies(lid string) ([]ConvCopy, error) {
	own := a.ownDevices() // before the rows: the store has one connection
	rows, err := a.store.db.Query(`SELECT id, recipient, state, coalesce(error, '') FROM outbox WHERE lid = ? AND conv IS NOT NULL ORDER BY rowid`, lid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConvCopy
	for rows.Next() {
		var c ConvCopy
		if err := rows.Scan(&c.ID, &c.To, &c.State, &c.Detail); err != nil {
			return nil, err
		}
		c.Own = own[c.To]
		out = append(out, c)
	}
	return out, rows.Err()
}

// outCopy is one device's copy being stored.
type outCopy struct {
	env            envelope.Envelope
	in             envelope.Inner
	state          string
	why            string
	required       string
	recipientFP    string // exact sealed ordinary group recipient; never inferred on retry
	groupAdmission string // ordinary: original own admission; history: frozen receiver grant epoch
}

// SendConv sends m in conversation conv: one copy to each current device of
// the other member and of this person's other devices, all with one
// logical id. A device that cannot read conversations now gets its copy
// kept as waiting, sent when it can; none is ever sent as version 1. Copies
// to this person's own devices are replicas (history: never executed),
// except the one to a request's execution target.
func (a *Agent) SendConv(ctx context.Context, conv string, m ConvOutgoing) (ConvSent, error) {
	binding, err := a.prepareReplyReceiver(m.ReplyReceiver)
	if err != nil {
		return ConvSent{}, err
	}
	if binding != nil {
		m.ReplyReceiver = &binding.receiver
	}
	root, raw, found, err := a.store.conversation(conv)
	if err != nil {
		return ConvSent{}, err
	}
	if !found {
		return ConvSent{}, fmt.Errorf("no conversation %s here", conv)
	}
	if root.Kind == protocol.ConvKindDM && m.sub == "" && (m.Kind == "" || m.Kind == envelope.KindMessage) && m.Target == nil {
		humanAuthor := m.PID == ""
		if m.PID != "" {
			p, e := a.Participation(m.PID)
			humanAuthor = e == nil && p.Role == protocol.RoleHuman
		} else if pid, e := a.ownHumanPID(conv); e == nil {
			m.PID = pid // an accepted guest's device: its own exact participation, as the page names it ("" for a member)
		}
		if humanAuthor {
			h, e := a.humanPlan(ctx, conv, m.PID)
			if e != nil {
				return ConvSent{}, e
			}
			if h != nil {
				return a.sendHumanTurn(ctx, root, raw, m, h, binding)
			}
		}
	}
	if root.Kind == protocol.ConvKindDM && m.sub == "" && m.PID != "" { // with guests present, the room sees addressed work too
		if x, e := a.participation(conv, m.PID); e == nil && x.Role != protocol.RoleHuman && x.Invite != "" {
			request := m.Target != nil && (m.Kind == envelope.KindQuestion || m.Kind == envelope.KindTask)
			output := m.Target == nil && (m.Kind == envelope.KindAnswer || m.Kind == envelope.KindResult || m.Kind == envelope.KindMessage && m.status == envelope.StatusProgress)
			if request || output {
				authorPID := ""
				if request {
					if authorPID, e = a.ownHumanPID(conv); e != nil {
						return ConvSent{}, e
					}
				}
				h, e := a.humanPlan(ctx, conv, authorPID)
				if e != nil {
					return ConvSent{}, e
				}
				if h != nil {
					return a.sendHumanTurn(ctx, root, raw, m, h, binding)
				}
			}
		}
	}
	if root.Kind == protocol.ConvKindGroup {
		if m.PID != "" {
			info, e := a.participation(conv, m.PID)
			if e != nil {
				return ConvSent{}, e
			}
			return a.sendExternalParticipation(ctx, root, raw, info, m)
		}
		if m.sub == "" && m.Target == nil && (m.Kind == "" || m.Kind == envelope.KindMessage) {
			infos, e := a.Participations(conv)
			if e != nil {
				return ConvSent{}, e
			}
			for _, p := range infos {
				if p.External && p.Following() {
					h, e := a.roomAudience(conv, "")
					if e != nil {
						return ConvSent{}, e
					}
					return a.sendHumanTurn(ctx, root, raw, m, h, binding)
				}
			}
		}
		return a.sendGroupTurn(ctx, conv, m, binding)
	}
	if m.PID != "" {
		info, err := a.participation(conv, m.PID)
		if err != nil {
			return ConvSent{}, err
		}
		if info.External {
			return a.sendExternalParticipation(ctx, root, raw, info, m)
		}
	}
	receiverStored := false
	defer func() {
		if !receiverStored && binding != nil && binding.setup != nil {
			a.releaseSpool(envelope.Envelope{Blobs: blobsOf(binding.setup.in.Attachments)})
		}
	}()
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
	if m.Origin == "" && m.sub != envelope.SubDriveSpace { // a dedicated record carries no origin
		m.Origin = envelope.OriginUI
	}
	if m.Quote != "" {
		parent, known, err := humanReplyParent(a.store.db, conv, m.Quote)
		if err != nil {
			return ConvSent{}, err
		}
		if !known {
			return ConvSent{}, errors.New("quote stays within its conversation")
		}
		m.Quote = parent
	}
	if m.ReplyTo != "" { // a reply stays within its own conversation
		if c, err := a.store.convOf(m.ReplyTo); err != nil {
			return ConvSent{}, err
		} else if c != conv {
			return ConvSent{}, fmt.Errorf("message %s is not in this conversation: a reply stays within its conversation", m.ReplyTo)
		}
	}
	if len(m.Files) > 0 {
		addressed := false
		if m.PID != "" && m.Target != nil && (m.Kind == envelope.KindQuestion || m.Kind == envelope.KindTask) {
			info, err := a.participation(conv, m.PID)
			if err != nil {
				return ConvSent{}, err
			}
			addressed = info.Claimable() && m.Target.Address == info.Host.Address && m.Target.Fingerprint == info.Host.Fingerprint && m.Target.AgentID == info.AgentID
		}
		if m.sub != "" || m.claim != nil || (m.PID != "" || m.selfJob) && !addressed {
			return ConvSent{}, errors.New("files go only with a message, question or task a person sends")
		}
		if len(m.Files) > envelope.MaxAttachments {
			return ConvSent{}, fmt.Errorf("at most %d attachments per message", envelope.MaxAttachments)
		}
	} else if envelope.Blank(m.Body) && m.sub == "" {
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
	for _, f := range m.Files { // this device's own copy, for its person's other devices to ask for later
		if err := a.keepSent(f.Path); err != nil {
			return ConvSent{}, err
		}
	}
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
	// A turn is for the other member: it fails, as a device send does, when
	// a key changed (until trusted) or none of their devices can get a copy.
	// Records go to the devices that can take them.
	turn := m.sub == "" && !m.selfJob
	var peerErr error // why a device of the other member got no copy
	for _, dev := range devices {
		key, err := a.sendKey(ctx, dev.Address)
		var changed *KeyChangedError
		if turn && errors.As(err, &changed) {
			return ConvSent{}, err
		}
		if err == nil && key.Fingerprint() != dev.Fingerprint() {
			err = fmt.Errorf("%s's key is not the one its person's roster names", dev.Address)
		}
		if err != nil {
			a.Logf("conversation copy for %s not sent: %v", dev.Address, err)
			if !own[dev.Address] && peerErr == nil {
				peerErr = fmt.Errorf("%s: %w", dev.Address, err)
			}
			continue
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ConvSent{}, err
		}
		target := m.Target != nil && m.Target.Address == dev.Address && m.Target.Fingerprint == dev.Fingerprint()
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(),
			Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Quote: m.Quote, Conv: conv, LID: lid, Root: raw, Replica: own[dev.Address] && !target,
			Origin: m.Origin, Emotion: m.Emotion, Target: m.Target, PID: m.PID, Sub: m.sub, Status: m.status, Fan: fan, AgentID: m.AgentID}
		if binding != nil && binding.receiver.Host != nil && target {
			in.ID = lid
		}
		copies = append(copies, outCopy{in: in}) // listed before spooling, so a failure releases what it spooled
		c := &copies[len(copies)-1]
		if binding != nil {
			c.recipientFP = key.Fingerprint()
		}
		for _, f := range m.Files {
			att, err := a.spoolNamed(f, recipient)
			if err != nil {
				return ConvSent{}, err
			}
			c.in.Attachments = append(c.in.Attachments, att)
		}
		supported, why, notify := false, WaitServerUnavailable+"cannot reach the Hub", false
		if ferr == nil {
			supported, why, notify = a.convSupport(ctx, dev.Address, key, feats)
			if supported && m.sub == envelope.SubDriveSpace { // a dedicated record: only devices that read it
				supported, why = a.capSupport(ctx, dev.Address, key, feats, protocol.CapDriveSpace)
			}
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
	if turn && !slices.ContainsFunc(copies, func(c outCopy) bool { return !own[c.in.To] }) {
		if peerErr == nil {
			peerErr = errors.New("the other member has no current device")
		}
		return ConvSent{}, fmt.Errorf("not sent: no device of the other member can get a copy: %w", peerErr)
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
		Kind: m.Kind, Body: m.Body, Conv: conv, LID: lid, Origin: m.Origin, Target: m.Target, PID: m.PID, Fan: fan, AgentID: m.AgentID}
	if binding != nil && m.Target == nil {
		binding.person = peerID
	}
	if err := a.prepareRemoteCopies(ctx, binding, copies, m.Files); err != nil {
		return ConvSent{}, err
	}
	if err := a.store.addConvOutbox(copies, local, m.claim, jobKey, binding); err != nil {
		return ConvSent{}, err
	}
	receiverStored = true
	if m.stored != nil {
		m.stored()
	}
	if binding != nil && binding.setup != nil {
		if _, e := a.deliver(ctx, binding.setup.env, nil); e != nil && !retryable(e) {
			return ConvSent{}, e
		}
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

// refreshRecipientPerson brings the person pinned for the device at address
// up to the Hub's current roster, once per person in done.
func (a *Agent) refreshRecipientPerson(ctx context.Context, address string, done map[string]error) error {
	p, pinned, err := a.store.personByAddress(address)
	if err != nil || !pinned {
		return err // not pinned: personSendable refuses it
	}
	err, seen := done[p.info.Person]
	if !seen {
		_, err = a.refreshPerson(ctx, p.info.Person, false)
		done[p.info.Person] = err
	}
	return err
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
	refreshed := map[string]error{} // each recipient's person, read fresh once per pass
	for id, w := range waiting {
		to := w.to
		progress := w.status == envelope.StatusProgress
		item, assistant := historyAssistant(w.body, w.conv) // as delivery decides it: agr1 besides the copy's own requirement
		// rm1 besides the copy's own requirement, for a room shape (ROOM_V1 §2.5)
		room, err := roomCopy(a.store.db, w.conv, w.sub, w.body, w.humanRaw)
		if err != nil {
			a.Logf("conversation message %s: %v", id, err)
			continue
		}
		cacheKey := to + "\x00" + w.sub + "\x00" + w.required + "\x00" + w.status + "\x00" + w.agentID + "\x00" + w.conv + "\x00" + w.pid + "\x00" + item.PID + "\x00" + item.AgentID
		if w.human {
			cacheKey += "\x00human"
		}
		if room {
			cacheKey += "\x00room"
		}
		ok, seen := checked[cacheKey]
		if !seen {
			key, _, found, err := a.store.peer(to)
			switch {
			case err != nil || !found:
			case w.required == protocol.CapAgentReaction && w.conv == "":
				// A device thread's assistant reaction: no person gate either.
				ok = a.requireParticipationCaps(ctx, key, w.required) == nil && a.assistantReactionCaps(ctx, key, "", "", w.agentID) == nil
			case w.required == protocol.CapProgress:
				// Version 1 progress: delivery has no person gate, only the
				// signed capability that marks it as an update (and a named
				// executor's identity capability).
				ok = a.requireParticipationCaps(ctx, key, w.required) == nil &&
					(w.agentID == "" || a.requireParticipationCaps(ctx, key, protocol.CapAgentIdentity) == nil)
			default:
				ok, _, _ = a.convSupport(ctx, to, key, feats)
				if ok && w.sub == envelope.SubDriveSpace {
					ok, _ = a.capSupport(ctx, to, key, feats, protocol.CapDriveSpace)
				}
				if ok && w.required != "" {
					ok = a.requireParticipationCaps(ctx, key, w.required) == nil
				}
				if ok && progress {
					ok = a.requireParticipationCaps(ctx, key, protocol.CapProgress) == nil
				}
				if ok && w.required == protocol.CapAgentReaction {
					ok = a.assistantReactionCaps(ctx, key, w.conv, w.pid, w.agentID) == nil
				}
				if ok && w.required == protocol.CapAgentReaction && w.human { // to a captured audience: as a human-audience turn
					ok = a.requireParticipationCaps(ctx, key, protocol.CapHumanParticipation) == nil
				}
				if ok && room && w.required != protocol.CapRoom {
					ok = a.requireParticipationCaps(ctx, key, protocol.CapRoom) == nil
				}
				if ok && w.sub == envelope.SubHistory && assistant {
					ok = (w.required == protocol.CapAgentReaction || a.requireParticipationCaps(ctx, key, protocol.CapAgentReaction) == nil) &&
						a.assistantReactionCaps(ctx, key, w.conv, item.PID, item.AgentID) == nil
				}
				// Support alone is not enough: the roster pinned when the copy
				// was made may list a device its person removed since (the Hub
				// revokes only linked devices, so an invite-joined one still
				// answers), and the profile just read may have frozen the
				// person, who then gets nothing. The person's current roster
				// decides; without it, nothing is released.
				if ok {
					ok = a.refreshRecipientPerson(ctx, to, refreshed) == nil
				}
				ok = ok && a.personSendable(to, "")
			}
			checked[cacheKey] = ok
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
		if err != nil || (in.V != envelope.Version2 && in.V != envelope.Version3) {
			continue
		}
		admit := a.admitConv
		if in.V == envelope.Version3 {
			admit = a.admitControl
		}
		if err := admit(ctx, env, in, sender, true); err != nil && retryable(err) {
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
	sp, err := a.personOfKey(ctx, env.From, sender)
	if err != nil {
		return personErr(err)
	}
	if in.Sub == envelope.SubGroupWithdrawal {
		return a.admitGroupWithdrawalCarrier(ctx, env, in, root, sender, fromQuarantine, hold)
	}
	if in.Sub == envelope.SubGroupInvite || in.Sub == envelope.SubGroupConsent {
		return a.admitGroupLifecycle(ctx, env, in, root, sender, fromQuarantine, hold)
	}
	if in.Sub == envelope.SubGroupProof || in.Sub == envelope.SubGroupContext {
		return a.admitGroupCarrier(ctx, env, in, root, sender, fromQuarantine, hold)
	}
	if root.Kind == protocol.ConvKindGroup && roomGroupTurn(in) { // a person guest's turn: its PID names its author, not an agent
		return a.admitGroupTurn(ctx, env, in, root, me, sp, sender, fromQuarantine, hold)
	}
	if root.Kind == protocol.ConvKindGroup && in.PID != "" && in.Human != nil {
		for _, e := range in.Human.Proof {
			if e.Role == protocol.RoleHuman {
				return hold(reasonInvalid, "group human guest execution audience is not enabled")
			}
		}
		return a.admitHumanTurn(ctx, env, in, root, sp, sender, fromQuarantine, hold)
	}
	if root.Kind == protocol.ConvKindGroup && in.PID != "" {
		if handled, e := a.admitGroupVisitorInvite(ctx, env, in, root, me, sp, sender, fromQuarantine, hold); handled {
			return e
		}
		if handled, e := a.admitExternalParticipation(ctx, env, in, root, me, sp, sender, fromQuarantine, hold); handled {
			return e
		}
	}
	if root.Kind == protocol.ConvKindGroup {
		return a.admitGroupTurn(ctx, env, in, root, me, sp, sender, fromQuarantine, hold)
	}
	if in.Human != nil {
		return a.admitHumanTurn(ctx, env, in, root, sp, sender, fromQuarantine, hold)
	}
	if handled, err := a.admitExternalParticipation(ctx, env, in, root, me, sp, sender, fromQuarantine, hold); handled {
		return err
	}
	if r, member := root.Member(me.info.Person); !member {
		return hold(reasonInvalid, "this installation's person is not a member")
	} else if ok, err := a.boundIn(ctx, me.info.Person, r); err != nil {
		return personErr(err)
	} else if !ok {
		return hold(reasonInvalid, "the root binds this person to a roster step it never had")
	}
	if r, member := root.Member(sp.info.Person); !member {
		return hold(reasonInvalid, "the sender is not a member of this conversation")
	} else if ok, err := a.boundIn(ctx, sp.info.Person, r); err != nil {
		return personErr(err)
	} else if !ok {
		return hold(reasonInvalid, "the root binds the sender's person to a roster step its chain does not have")
	}
	if (in.Sub == envelope.SubHistory || in.Sub == envelope.SubFile) && sp.info.Person != me.info.Person {
		return hold(reasonInvalid, "history and its files come only from a device of this installation's own person")
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
	switch in.Sub {
	case envelope.SubHistory:
		return a.admitHistory(ctx, env, in, root, hold, fromQuarantine)
	case envelope.SubFile:
		return a.admitFile(env, in, hold)
	case envelope.SubDriveSpace: // the conversation's Drive space record (drivespace_wire.go): applied under the sender's person, stored quietly
		return a.admitDriveControl(ctx, env, in, sender, fromQuarantine)
	}
	if reason, err := a.checkConversationAgent(in, sender, false); err != nil {
		if reason != "" {
			return hold(reason, err.Error())
		}
		return err
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
		if err := a.scopeMatchesInvite(ev); err != nil {
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
			// This person's own message, from another of its devices: never an
			// alert, and a turn of theirs answers what was held for them
			// before it was written there.
			return turnClosesHeld(tx, in, in.TS*1000)
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
		if in.Sub == envelope.SubEvent {
			a.convWork.due(convRetry)
			a.kickNow()
			a.trySelfConsent(ctx, in.PID) // an invite of this person's own agent, hosted here
		}
		a.wakeWorker() // a request, or an event that may let one run or stop
		a.wakeAlerts()
		if len(forward) > 0 {
			a.kickNow() // the stream's worker sends them now, not at the next ping
		}
		if len(in.Attachments) > 0 {
			a.convWork.due(convFetch) // keep its files here (historyfiles.go)
			a.kickNow()
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

// sentElsewhere reports whether id names no message sent from this device
// but a conversation message received here from another device of this
// person: views show it as sent (Dir "out", Via), and it is stored here as
// received.
func (a *Agent) sentElsewhere(id string) (bool, error) {
	var sent int
	var sender string
	if err := a.store.db.QueryRow(`SELECT (SELECT count(*) FROM outbox WHERE id = ?),
		coalesce((SELECT sender FROM inbox WHERE id = ? AND local = 0 AND conv IS NOT NULL), '')`, id, id).Scan(&sent, &sender); err != nil {
		return false, err
	}
	return sent == 0 && sender != "" && a.ownDevices()[sender], nil
}

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
