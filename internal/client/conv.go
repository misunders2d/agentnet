package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Human DMs, first checkpoint: this installation's person, two-person
// conversations with a signed root (E0), and version 2 messages admitted
// once per (verifying key, logical id). Nothing here runs anything: a
// conversation question or task is held for the person (stateConvHeld)
// until agent participation exists, and no conversation message enters the
// address-approved legacy worker.
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

// ErrNoPerson means an installation has not created a person.
var ErrNoPerson = errors.New("that installation has not created a person (agentnet person create)")

// errPersonRecord means a published person record did not verify.
var errPersonRecord = errors.New("person record does not verify")

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
		if err := a.publishOwn(ctx, feats); err != nil {
			a.Logf("publishing person and capabilities: %v", err)
			if retryable(err) {
				a.convWork.due(convPublish)
			}
		}
	}
	if work&convPersons != 0 { // before retrying held messages: a conflict must hold them
		a.checkPersons(a.MemberView().Members)
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
		rec := protocol.CapsRecord{Address: a.Address, Session: a.session, Caps: []string{protocol.CapEnv2}, TS: time.Now().Unix()}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(ctx, "PUT", "/v1/caps", rec, nil); err != nil {
			return err
		}
	}
	if slices.Contains(feats, protocol.FeaturePerson) {
		return a.publishPerson(ctx, false)
	}
	return nil
}

// publishPerson sends this installation's person record to the Hub, unless
// this exact record was sent already (or force).
func (a *Agent) publishPerson(ctx context.Context, force bool) error {
	me, ok, err := a.store.selfPerson()
	if err != nil || !ok {
		return err
	}
	if done, _ := a.store.config("person_published"); done == me.info.Roster && !force {
		return nil
	}
	if err := a.hub.doBytes(ctx, "PUT", "/v1/person", me.raw, nil); err != nil {
		return err
	}
	return a.store.setConfig(map[string]string{"person_published": me.info.Roster})
}

// Person returns this installation's person, if one was created here.
func (a *Agent) Person() (PersonInfo, bool, error) {
	me, ok, err := a.store.selfPerson()
	return me.info, ok, err
}

// CreatePerson creates this installation's person, with label as the name
// it shows (its own claim), and publishes it. It is the only way a person
// comes to exist: never from an enrollment, address, label or migration.
// There is at most one per installation. A returned ErrNotPublished means
// the person exists but the Hub does not hold it yet.
func (a *Agent) CreatePerson(ctx context.Context, label string) (PersonInfo, error) {
	if me, ok, err := a.store.selfPerson(); err != nil {
		return PersonInfo{}, err
	} else if ok {
		return me.info, fmt.Errorf("this installation already speaks for %q (%s); a second person is not created", me.info.Label, me.info.Person)
	}
	pub := a.id.Public(a.Address)
	r := protocol.PersonRoster{Person: protocol.NewID(), Label: label, Devices: []protocol.RosterDevice{{Address: a.Address, Fingerprint: pub.Fingerprint()}}}
	if err := r.Validate(); err != nil {
		return PersonInfo{}, err
	}
	r.Sign(a.id.Sign)
	raw, _ := json.Marshal(r)
	if len(raw) > protocol.MaxPersonRecord {
		return PersonInfo{}, errors.New("person: the record is too large; use a shorter label")
	}
	if err := a.store.setSelfPerson(r, raw); err != nil {
		return PersonInfo{}, err
	}
	me, _, err := a.store.selfPerson()
	if err != nil {
		return PersonInfo{}, err
	}
	feats, err := a.relayFeatures(ctx)
	if err == nil && !slices.Contains(feats, protocol.FeaturePerson) {
		err = errors.New("the Hub does not hold persons (it needs an update)")
	}
	if err == nil {
		err = a.publishPerson(ctx, true)
	}
	if err != nil {
		return me.info, fmt.Errorf("%w: %v", ErrNotPublished, err)
	}
	return me.info, nil
}

// personOfKey returns the person that the device at address speaks for,
// pinning its published record first if needed, verified against key, the
// device's verified key. A record that does not name that key is refused.
func (a *Agent) personOfKey(ctx context.Context, address string, key identity.Public) (personRow, error) {
	p, ok, err := a.store.personByAddress(address)
	if err != nil {
		return p, err
	}
	if ok {
		if p.info.State == personConflict {
			return p, errPersonConflict
		}
		return p, nil
	}
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return p, err
	}
	var prof protocol.Profile
	err = a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof)
	var he *HubError
	if errors.As(err, &he) && he.Status == 404 {
		return p, ErrNoPerson
	}
	if err != nil {
		return p, err
	}
	if len(prof.Person) == 0 {
		return p, ErrNoPerson
	}
	r, err := protocol.ParsePersonRoster(prof.Person)
	if err == nil {
		err = r.Verify(key.SignKey)
	}
	if err == nil && (r.Devices[0].Address != address || r.Devices[0].Fingerprint != key.Fingerprint()) {
		err = errors.New("person: the record does not name this device's key")
	}
	if err != nil {
		return p, fmt.Errorf("%w: %s: %v", errPersonRecord, address, err)
	}
	if err := a.store.pinPerson(r, prof.Person, key); err != nil {
		return p, err
	}
	p, _, err = a.store.personByAddress(address)
	return p, err
}

// checkPersons compares the person records in a member list with the
// persons pinned here (observePerson); members not pinned here are left
// alone: listing someone never pins or trusts them.
func (a *Agent) checkPersons(ms protocol.Members) {
	for _, m := range ms.Members {
		if len(m.Person) > 0 {
			a.observePerson(m.Address, m.Person)
		}
	}
}

// observePerson checks a person record published for address against the
// person pinned for it. Only a record that verifies against the very key
// the pinned person was verified with, and names that device, counts as
// evidence; if it then differs, the pinned person is frozen as a conflict
// (it keeps its identity; nothing is replaced). A record that does not
// verify, or nothing pinned, changes nothing: it is no proof of a conflict.
func (a *Agent) observePerson(address string, blob json.RawMessage) {
	p, ok, err := a.store.personByAddress(address)
	if err != nil || !ok || p.info.State != personPinned {
		return
	}
	pub := p.pub
	if pub == nil { // pinned before keys were kept: the address's pinned key
		pinned, _, found, err := a.store.peer(address)
		if err != nil || !found || pinned.Fingerprint() != p.info.Fingerprint {
			return
		}
		pub = &pinned
	}
	r, err := protocol.ParsePersonRoster(blob)
	if err != nil || r.Verify(pub.SignKey) != nil {
		return
	}
	if d := r.Devices[0]; d.Address != address || d.Fingerprint != p.info.Fingerprint {
		return
	}
	if r.Hash() == p.info.Roster {
		return
	}
	if err := a.store.pinPerson(r, blob, *pub); errors.Is(err, errPersonConflict) {
		a.Logf("%s published a different person record than the one pinned here: frozen (conversations with %q hold)", address, p.info.Label)
	} else if err != nil {
		a.Logf("person record of %s: %v", address, err)
	}
}

// convSupport reports whether a conversation message can go to the device
// at address now: the relay carries version 2 and the device's signed
// capabilities, for every session the relay lists, include it.
func (a *Agent) convSupport(ctx context.Context, address string, key identity.Public, feats []string) (bool, string) {
	if !slices.Contains(feats, protocol.FeatureEnv2) || !slices.Contains(feats, protocol.FeatureCaps) {
		return false, "your Hub cannot carry conversations (it needs an update)"
	}
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return false, err.Error()
	}
	var prof protocol.Profile
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		return false, "cannot ask the Hub what " + address + " can read: " + err.Error()
	}
	if len(prof.Person) > 0 {
		a.observePerson(address, prof.Person) // fresh evidence, checked before anything is sent
	}
	if !prof.Supports(address, key.SignKey, protocol.CapEnv2) {
		return false, address + "'s AgentNet cannot read conversations now (an older program, or it has not connected since updating)"
	}
	return true, ""
}

// CreateDM starts a new two-person conversation with the person that the
// device at address speaks for. Each call starts a separate conversation.
func (a *Agent) CreateDM(ctx context.Context, address string) (string, error) {
	me, ok, err := a.store.selfPerson()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("create your person first (agentnet person create NAME)")
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
	if ok, why := a.convSupport(ctx, address, key, feats); !ok {
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
	root := protocol.ConvRoot{V: 1, Kind: protocol.ConvKindDM,
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
	return a.store.convMessages(conv, a.Address)
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

	sub string // envelope.SubEvent for participation events (participation.go)
}

// ConvSent is what became of a conversation message.
type ConvSent struct {
	ID, LID string
	State   string // custody, delivered, queued, or waiting (kept: the recipient cannot read it now)
	Detail  string
}

// SendConv sends m in conversation conv. If the recipient's device cannot
// read conversations now, the message is kept as waiting and goes out when
// it can; it is never sent as version 1.
func (a *Agent) SendConv(ctx context.Context, conv string, m ConvOutgoing) (ConvSent, error) {
	root, raw, found, err := a.store.conversation(conv)
	if err != nil {
		return ConvSent{}, err
	}
	if !found {
		return ConvSent{}, fmt.Errorf("no conversation %s here", conv)
	}
	me, ok, err := a.store.selfPerson()
	if err != nil {
		return ConvSent{}, err
	}
	if r, member := root.Member(me.info.Person); !ok || !member || r != me.info.Roster {
		return ConvSent{}, errors.New("this installation does not speak for a member of that conversation")
	}
	var peerID string
	for _, mem := range root.Members {
		if mem.Person != me.info.Person {
			peerID = mem.Person
		}
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
	dev := peer.roster.Devices[0]
	key, err := a.sendKey(ctx, dev.Address)
	if err != nil {
		return ConvSent{}, err
	}
	if key.Fingerprint() != dev.Fingerprint {
		return ConvSent{}, fmt.Errorf("%s's key is no longer the one its person record names; the conversation is frozen", dev.Address)
	}
	recipient, err := key.Recipient()
	if err != nil {
		return ConvSent{}, err
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
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(),
		Kind: m.Kind, Body: m.Body, ReplyTo: m.ReplyTo, Conv: conv, LID: protocol.NewID(), Root: raw,
		Origin: m.Origin, Emotion: m.Emotion, Target: m.Target, PID: m.PID, Sub: m.sub}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return ConvSent{}, err
	}
	feats, ferr := a.relayFeatures(ctx)
	supported, why := false, ""
	if ferr != nil {
		why = "cannot reach the Hub: " + ferr.Error()
	} else {
		supported, why = a.convSupport(ctx, dev.Address, key, feats)
	}
	if now, _, err := a.store.personByID(peerID); err != nil {
		return ConvSent{}, err
	} else if now.info.State == personConflict { // the profile just showed a different record
		return ConvSent{}, errPersonConflict
	}
	state := stateQueued
	if !supported {
		state = stateConvWaiting
	}
	if err := a.store.addConvOutbox(env, in, state, why); err != nil {
		return ConvSent{}, err
	}
	defer notifyDaemon(a.home)
	if !supported {
		return ConvSent{ID: env.ID, LID: in.LID, State: stateConvWaiting, Detail: why}, nil
	}
	res, err := a.deliver(ctx, env, nil)
	if err != nil {
		if retryable(err) {
			return ConvSent{ID: env.ID, LID: in.LID, State: stateQueued, Detail: err.Error()}, nil
		}
		return ConvSent{ID: env.ID, LID: in.LID}, err
	}
	return ConvSent{ID: env.ID, LID: in.LID, State: res.State}, nil
}

// personSendable reports whether the person on the device at address is
// pinned and not frozen (and, if roster is given, still that roster).
func (a *Agent) personSendable(address, roster string) bool {
	p, ok, err := a.store.personByAddress(address)
	return err == nil && ok && p.info.State == personPinned && (roster == "" || p.info.Roster == roster)
}

// releaseConv queues waiting conversation messages whose recipient can now
// read them; the sync's outbox flush sends them.
func (a *Agent) releaseConv(ctx context.Context, feats []string) {
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
				ok, _ = a.convSupport(ctx, to, key, feats)
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
	me, ok, err := a.store.selfPerson()
	if err != nil {
		return err
	}
	if !ok {
		return hold(reasonInvalid, "this installation has no person")
	}
	if r, member := root.Member(me.info.Person); !member || r != me.info.Roster {
		return hold(reasonInvalid, "this installation's person is not a member")
	}
	if _, _, found, err := a.store.conversation(in.Conv); err != nil {
		return err
	} else if !found {
		// Only the creator's own device introduces a root.
		if env.From != root.Creator.Address || sender.Fingerprint() != root.Creator.Fingerprint {
			return hold(reasonProof, "the conversation is not known here yet, and only its creator's device can introduce it")
		}
		if err := root.Verify(sender.SignKey); err != nil {
			return hold(reasonInvalid, err.Error())
		}
		creator, err := a.personOfKey(ctx, env.From, sender)
		if err != nil {
			return personErr(err)
		}
		if creator.info.Person != root.Creator.Person || creator.info.Roster != root.Creator.Roster {
			return hold(reasonConflict, "the creator's person record differs from the one its root names")
		}
		if err := a.store.addConversation(root, in.Root, creator.info.Person); err != nil {
			return err
		}
	}
	sp, err := a.personOfKey(ctx, env.From, sender)
	if err != nil {
		return personErr(err)
	}
	if sp.info.Fingerprint != sender.Fingerprint() {
		return hold(reasonConflict, "the sender's key is not the one its person record names")
	}
	if r, member := root.Member(sp.info.Person); !member || r != sp.info.Roster {
		return hold(reasonInvalid, "the sender is not a member of this conversation")
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
	if in.Sub == envelope.SubEvent { // a participation record: stored, and the message kept as history
		if err := a.admitParticipationEvent(in, sender.Fingerprint(), sender.SignKey); err != nil {
			return hold(reasonInvalid, err.Error())
		}
	}
	state := ""
	if in.Kind == envelope.KindQuestion || in.Kind == envelope.KindTask {
		state = stateConvHeld // for the person; nothing runs a conversation request yet, participation or not
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), state, fromQuarantine)
	if err != nil {
		return err
	}
	if res == admitConflict {
		return hold(reasonDuplicate, "a message with the same key and logical id but other content is stored")
	}
	return nil
}
