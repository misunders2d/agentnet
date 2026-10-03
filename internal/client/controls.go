package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Message controls (MEL-476, MEL-477, R08): a reaction, a revision of the
// text or a retraction of one earlier message, sent as a version 3
// envelope (envelope.go) to the devices that can read one and stored like
// any other message (inbox/outbox rows with ref_id and ref_fp), so
// custody, receipts, replicas and history sync need nothing new.
//
// A control is resolved when a message is shown, never when it arrives:
// its target is the row with exactly that id (a device message) or
// logical id (a conversation turn) AND that sender key, in the same thread
// or conversation. A control whose target is not held yet does nothing
// until the target arrives; it can never apply to another message with the
// same id. Who may do what is checked at that moment, against the keys and
// persons pinned here now:
//
//   - react: the other party of a device thread, or a current device of a
//     member person of the conversation (the sender's own devices count);
//   - edit, delete: the target's author only: in a device thread the very
//     key that sent it; in a conversation any device of the same person.
//
// What a control cannot do: it never becomes a job, a decision, a chat
// turn or an alert; a revision never changes what was admitted for
// execution (inbox.body stays the original, the worker reads only that); a
// retraction never cancels or reruns anything and recalls nothing already
// read, saved or given to an agent. It hides the text and files here and
// drops this device's cached ciphertext of the target's files (saved
// files are the person's).

// Controls is what controls did to one message, as this device resolves
// them now.
type Controls struct {
	Reactions []ReactionView `json:"reactions,omitempty"`
	Edited    bool           `json:"edited,omitempty"`
	Revision  int64          `json:"revision,omitempty"`
	Text      string         `json:"text,omitempty"` // the current text when edited; Body keeps what was sent or admitted
	Deleted   bool           `json:"deleted,omitempty"`
	Can       []string       `json:"can,omitempty"` // of CanReact, CanEdit, CanDelete: what this device may do to it
}

// What a device may do to a message.
const (
	CanReact  = "react"
	CanEdit   = "edit"
	CanDelete = "delete"
)

// ReactionView is one emoji on a message and who put it there.
type ReactionView struct {
	Emoji string    `json:"emoji"`
	By    []Reactor `json:"by"`             // one per person (conversation) or device (device thread)
	Mine  bool      `json:"mine,omitempty"` // this person (or device) reacted
}

// Reactor is who reacted: a stable id (the person id in a conversation,
// the device address in a device thread) and the label to show. Two
// reactors with one label stay two.
type Reactor struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Assistant bool   `json:"assistant,omitempty"` // an assistant's own reaction, never its host person's
	// An assistant's exact identity, for a label from its host's catalog:
	// its host device, its agent (named) and its participation, if any.
	Host    string `json:"host,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
	PID     string `json:"pid,omitempty"`
}

// Shown is the text to show for m: its current revision, or its body.
func (c Controls) Shown(body string) string {
	if c.Deleted {
		return ""
	}
	if c.Edited {
		return c.Text
	}
	return body
}

// ControlRef names the message a control is about, exactly.
type ControlRef struct {
	Conv        string `json:"conv,omitempty"` // "" for a device message
	ID          string `json:"id"`             // envelope id (device message) or logical id (conversation turn)
	Fingerprint string `json:"fingerprint"`    // the key that sent it, as verified here
}

// ControlSent is what became of a control: the first copy's id and state,
// and the devices that got none because they cannot read controls yet.
type ControlSent struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Detail  string   `json:"detail,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
}

// ErrNoControls means the recipient cannot read controls.
var ErrNoControls = errors.New("cannot read reactions, edits or deletions yet")

// RefOf turns a message as shown (its conversation or "", its id and its
// direction) into the exact reference a control needs. A message whose
// sender key is not known here cannot be referred to.
func (a *Agent) RefOf(conv, id, dir string) (ControlRef, error) {
	if !protocol.ValidID(id) || (dir != "in" && dir != "out") {
		return ControlRef{}, errors.New("a control names a message by its id and direction")
	}
	self := a.Self().Fingerprint()
	if conv == "" {
		if dir == "out" {
			var n int
			if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL`, id).Scan(&n); err != nil {
				return ControlRef{}, err
			}
			if n == 0 {
				return ControlRef{}, ErrNoMessage
			}
			return ControlRef{ID: id, Fingerprint: self}, nil
		}
		var fp string
		err := a.store.db.QueryRow(`SELECT coalesce(verified_by, '') FROM inbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL AND local = 0`, id).Scan(&fp)
		if errors.Is(err, sql.ErrNoRows) {
			return ControlRef{}, ErrNoMessage
		}
		if err != nil {
			return ControlRef{}, err
		}
		if fp == "" {
			return ControlRef{}, errors.New("that message's sender key is not recorded here: it cannot be referred to")
		}
		return ControlRef{ID: id, Fingerprint: fp}, nil
	}
	var lid, fp string
	var err error
	if dir == "out" {
		err = a.store.db.QueryRow(`SELECT lid FROM outbox WHERE id = ? AND conv = ? AND ref_id IS NULL`, id, conv).Scan(&lid)
		fp = self
	} else {
		err = a.store.db.QueryRow(`SELECT lid, coalesce(verified_by, claimed_fp, '') FROM inbox WHERE id = ? AND conv = ? AND ref_id IS NULL`, id, conv).Scan(&lid, &fp)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ControlRef{}, ErrNoMessage
	}
	if err != nil {
		return ControlRef{}, err
	}
	if fp == "" {
		return ControlRef{}, errors.New("that message's sender key is not recorded here: it cannot be referred to")
	}
	return ControlRef{Conv: conv, ID: lid, Fingerprint: fp}, nil
}

// React adds (or, with remove, removes) emoji on the message ref names. A
// removal takes off this person's (in a device thread, this device's) own
// reaction only, so one that is not there is refused, not sent.
func (a *Agent) React(ctx context.Context, ref ControlRef, emoji string, remove bool) (ControlSent, error) {
	if !envelope.OneEmoji(emoji) {
		return ControlSent{}, errors.New("a reaction is one emoji")
	}
	if a.deleted(ref) {
		return ControlSent{}, errors.New("that message was deleted: it takes no reactions")
	}
	op := "add"
	if remove {
		op = "remove"
		c, err := a.controlsOf(ref)
		if err != nil {
			return ControlSent{}, err
		}
		if !slices.ContainsFunc(c.Reactions, func(v ReactionView) bool { return v.Emoji == emoji && v.Mine }) {
			return ControlSent{}, fmt.Errorf("there is no %s reaction of yours on that message to remove", emoji)
		}
	}
	n, err := a.store.nextCounter(ref, envelope.SubReaction, emoji)
	if err != nil {
		return ControlSent{}, err
	}
	body, _ := json.Marshal(envelope.Reaction{Emoji: emoji, Op: op, N: n})
	return a.sendControl(ctx, ref, envelope.SubReaction, string(body))
}

// Revise replaces the shown text of a message this device's person sent.
// The message keeps what it was: a question or task already admitted runs
// (or ran) with its original text, shown alongside the edit.
func (a *Agent) Revise(ctx context.Context, ref ControlRef, text string) (ControlSent, error) {
	if strings.TrimSpace(text) == "" || len(text) > envelope.MaxRevisionBytes {
		return ControlSent{}, fmt.Errorf("an edit is 1 to %d bytes of text", envelope.MaxRevisionBytes)
	}
	if err := a.mayAuthor(ref); err != nil {
		return ControlSent{}, err
	}
	if a.deleted(ref) {
		return ControlSent{}, errors.New("that message was deleted: it cannot be edited")
	}
	rev, err := a.store.nextCounter(ref, envelope.SubRevision, "")
	if err != nil {
		return ControlSent{}, err
	}
	body, _ := json.Marshal(envelope.Revision{Rev: rev, Text: text})
	return a.sendControl(ctx, ref, envelope.SubRevision, string(body))
}

// Retract marks a message this device's person sent as deleted wherever it
// is held. It stops nothing: a job it started keeps running, and what was
// read, saved or given to an agent stays with whoever has it.
func (a *Agent) Retract(ctx context.Context, ref ControlRef, reason string) (ControlSent, error) {
	if len(reason) > envelope.MaxReasonBytes {
		return ControlSent{}, fmt.Errorf("a reason is at most %d bytes", envelope.MaxReasonBytes)
	}
	if err := a.mayAuthor(ref); err != nil {
		return ControlSent{}, err
	}
	if a.deleted(ref) {
		return ControlSent{}, errors.New("that message was deleted already")
	}
	body, _ := json.Marshal(envelope.Retraction{Reason: reason})
	sent, err := a.sendControl(ctx, ref, envelope.SubRetraction, string(body))
	if err == nil {
		a.dropKept(ref)
		a.redactRetracted(ref) // this device keeps no copy of the deleted text either
	}
	return sent, err
}

// deleted reports whether the message ref names was deleted by its author,
// as held here: it shows nothing more to react to, edit or delete (its
// view offers none of these).
func (a *Agent) deleted(ref ControlRef) bool {
	return retractedRef(a.store.db, ref.Conv, ref.ID, ref.Fingerprint)
}

// mayAuthor refuses an edit or deletion of a message this device's person
// did not send.
func (a *Agent) mayAuthor(ref ControlRef) error {
	if ref.Conv == "" {
		if ref.Fingerprint != a.Self().Fingerprint() {
			return errors.New("only the sender edits or deletes a message")
		}
		return nil
	}
	m, err := controlMembers(a.store.db, ref.Conv)
	if err != nil {
		return err
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || a.personOfKeyIn(m, ref.Fingerprint) != me.info.Person {
		return errors.New("only the sender's person edits or deletes a message")
	}
	return nil
}

// nextCounter is the author's next counter for a control on ref: one more
// than the highest this device holds (from any device of this person), so
// devices seeing the controls in any order agree.
func (s *store) nextCounter(ref ControlRef, sub, emoji string) (int64, error) {
	rows, err := s.db.Query(`SELECT body FROM inbox WHERE ref_id = ? AND ref_fp = ? AND sub = ?
		UNION ALL SELECT body FROM outbox WHERE ref_id = ? AND ref_fp = ? AND sub = ?`, ref.ID, ref.Fingerprint, sub, ref.ID, ref.Fingerprint, sub)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var max int64
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return 0, err
		}
		switch sub {
		case envelope.SubReaction:
			var r envelope.Reaction
			if json.Unmarshal([]byte(body), &r) == nil && r.Emoji == emoji && r.N > max {
				max = r.N
			}
		case envelope.SubRevision:
			var r envelope.Revision
			if json.Unmarshal([]byte(body), &r) == nil && r.Rev > max {
				max = r.Rev
			}
		case envelope.SubStatus:
			var r envelope.Status
			if json.Unmarshal([]byte(body), &r) == nil && r.N > max {
				max = r.N
			}
		}
	}
	return max + 1, rows.Err()
}

// ctlSupport reports whether a control can go to the device at address:
// the relay carries version 3 and the device's signed capabilities include
// controls. Otherwise why says so, for the person.
func (a *Agent) ctlSupport(ctx context.Context, address string, key identity.Public, feats []string) (bool, string) {
	return a.capSupport(ctx, address, key, feats, protocol.CapControl)
}

// capSupport is ctlSupport for one named capability of a version 3
// control (protocol.CapControl or CapHeadless): each is checked by name.
func (a *Agent) capSupport(ctx context.Context, address string, key identity.Public, feats []string, cap string) (bool, string) {
	if !slices.Contains(feats, protocol.FeatureEnv3) || !slices.Contains(feats, protocol.FeatureCaps) {
		return false, "your Hub cannot carry reactions, edits or deletions (it needs an update)"
	}
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return false, err.Error()
	}
	var prof protocol.Profile
	if err := a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil {
		return false, "cannot ask the Hub what " + address + " can read: " + err.Error()
	}
	if !prof.Supports(address, key.SignKey, cap) {
		what := "reactions, edits or deletions"
		if cap == protocol.CapHeadless {
			what = "execution status or operator decisions"
		}
		return false, address + " cannot read " + what + " yet (an older program, or it has not connected since updating)"
	}
	return true, ""
}

// sendControl seals and sends one control about ref: to the other party of
// a device thread, or one copy per device of both persons of a
// conversation, each only if it can read controls. Nothing is ever sent as
// an older version instead.
func (a *Agent) sendControl(ctx context.Context, ref ControlRef, sub, body string) (ControlSent, error) {
	return a.sendControlAs(ctx, ref, sub, body, protocol.CapControl)
}

// sendControlAs is sendControl for a control that needs capability cap on
// the receiving device (protocol.CapControl for message controls,
// protocol.CapHeadless for status and decisions).
func (a *Agent) sendControlAs(ctx context.Context, ref ControlRef, sub, body, cap string) (ControlSent, error) {
	feats, err := a.relayFeatures(ctx)
	if err != nil {
		return ControlSent{}, err
	}
	eref := &envelope.Ref{ID: ref.ID, Fingerprint: ref.Fingerprint}
	if ref.Conv == "" {
		peer, err := a.threadPeer(ref)
		if err != nil {
			return ControlSent{}, err
		}
		key, err := a.sendKey(ctx, peer)
		if err != nil {
			return ControlSent{}, err
		}
		if ok, why := a.capSupport(ctx, peer, key, feats, cap); !ok {
			return ControlSent{}, fmt.Errorf("%w: %s", ErrNoControls, why)
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ControlSent{}, err
		}
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: a.Address, To: peer, TS: time.Now().Unix(),
			Kind: envelope.KindMessage, Sub: sub, Body: body, Ref: eref}
		env, err := envelope.Seal(in, a.id.Sign, recipient)
		if err != nil {
			return ControlSent{}, err
		}
		var selected *ReplyReceiverHost
		if sub == envelope.SubRetraction {
			selected, err = a.receiverRetractionHost(a.store.db, ref)
			if err != nil {
				return ControlSent{}, err
			}
		}
		var extra *outCopy
		if selected != nil && selected.Address != peer {
			selectedKey, e := replyReceiverHostIn(a.store.db, *selected)
			if e != nil {
				return ControlSent{}, e
			}
			if ok, why := a.capSupport(ctx, selected.Address, selectedKey, feats, cap); !ok {
				return ControlSent{}, fmt.Errorf("%w: %s", ErrNoControls, why)
			}
			copy, e := a.receiverReturnCopy(ctx, in, nil, *selected)
			if e != nil {
				return ControlSent{}, e
			}
			extra = &copy
		}
		if extra != nil {
			guard := func(tx *sql.Tx, _ string) error {
				current, e := a.receiverRetractionHost(tx, ref)
				if e != nil {
					return e
				}
				if current == nil || *current != *selected {
					return errors.New("selected deletion destination changed before commit")
				}
				return nil
			}
			err = a.store.addConvOutbox([]outCopy{{env: env, in: in, state: stateQueued}, *extra}, envelope.Inner{}, guard, "")
		} else {
			err = a.store.addControlOutbox(env, in)
		}
		if err != nil {
			return ControlSent{}, err
		}
		defer notifyDaemon(a.home)
		if extra != nil {
			if _, e := a.deliver(ctx, extra.env, nil); e != nil && !retryable(e) {
				return ControlSent{}, e
			}
		}
		res, err := a.deliver(ctx, env, nil)
		return ControlSent{ID: env.ID, State: res.State, Detail: res.Detail}, err
	}
	// A conversation: the same device set as a turn (SendConv), every copy
	// under one logical id; devices that cannot read controls are skipped
	// and named, never sent something else.
	root, _, found, err := a.store.conversation(ref.Conv)
	if err != nil {
		return ControlSent{}, err
	}
	if !found {
		return ControlSent{}, fmt.Errorf("no conversation %s here", ref.Conv)
	}
	var devices []identity.Public
	own := map[string]bool{}
	var fan []envelope.Fan
	var group *GroupContext
	if root.Kind == protocol.ConvKindGroup {
		if !(groupControlSub(sub) && cap == protocol.CapControl || sub == envelope.SubStatus && cap == protocol.CapHeadless) {
			return ControlSent{}, errors.New("group: this control requires its own addressed authority")
		}
		packet, e := groupTurnPacketIn(a.store.db, ref.Conv)
		if e != nil {
			return ControlSent{}, e
		}
		for _, member := range packet.State.Members {
			if _, e = a.refreshPerson(ctx, member.Person, false); e != nil {
				return ControlSent{}, e
			}
		}
		members, e := controlMembers(a.store.db, ref.Conv)
		if e != nil {
			return ControlSent{}, e
		}
		if sub == envelope.SubStatus {
			if _, e := a.groupStatusScope(a.store.db, ref, a.Address, a.Self().Fingerprint()); e != nil {
				return ControlSent{}, e
			}
		} else if !members.device(a.Address, a.Self().Fingerprint()) {
			return ControlSent{}, errors.New("group: control sender is not a current member")
		}

		me, _, e := a.store.selfPerson(a.Address)
		if e != nil {
			return ControlSent{}, e
		}
		fan = []envelope.Fan{{Person: me.roster.Person, Roster: me.roster.Hash()}}
		for _, person := range members.persons {
			for _, d := range person.roster.Devices {
				if d.Address != a.Address {
					devices = append(devices, d)
					own[d.Address] = person.roster.Person == me.roster.Person
				}
			}
		}
		group = &packet
	} else {
		me, ok, err := a.store.selfPerson(a.Address)
		if err != nil {
			return ControlSent{}, err
		}
		if r, member := root.Member(me.info.Person); !ok || !member || !a.store.inChain(me.info.Person, r) {
			return ControlSent{}, errors.New("this installation does not speak for a member of that conversation")
		}
		var peerID string
		for _, mem := range root.Members {
			if mem.Person != me.info.Person {
				peerID = mem.Person
			}
		}
		peer, ok, err := a.store.personByID(peerID)
		if err != nil {
			return ControlSent{}, err
		}
		if !ok || peer.info.State == personConflict {
			return ControlSent{}, errPersonConflict
		}
		devices = append(devices, peer.roster.Devices...)
		own = map[string]bool{}
		for _, d := range me.roster.Devices {
			if d.Address != a.Address {
				devices = append(devices, d)
				own[d.Address] = true
			}
		}
		// The rosters these copies go to, as a turn names them: a receiver that
		// knows a newer roster of its own person forwards the control to the
		// devices the sender did not know (forwardStale), so linked devices
		// converge whatever roster the sender held.
		fan = []envelope.Fan{{Person: me.info.Person, Roster: me.info.Roster}, {Person: peer.info.Person, Roster: peer.info.Roster}}
	}
	lid := protocol.NewID()
	var copies []outCopy
	sent := ControlSent{State: protocol.StateDelivered}
	peerCan := false
	for _, dev := range devices {
		key, err := a.sendKey(ctx, dev.Address)
		if err == nil && key.Fingerprint() != dev.Fingerprint() {
			err = fmt.Errorf("%s's key is not the one its person's roster names", dev.Address)
		}
		if err != nil {
			sent.Skipped = append(sent.Skipped, dev.Address+": "+err.Error())
			continue
		}
		if ok, why := a.capSupport(ctx, dev.Address, key, feats, cap); !ok {
			sent.Skipped = append(sent.Skipped, why)
			continue
		}
		if group != nil {
			if err := a.requireParticipationCaps(ctx, key, protocol.CapGroup); err != nil {
				sent.Skipped = append(sent.Skipped, dev.Address+": "+err.Error())
				continue
			}
		}
		recipient, err := key.Recipient()
		if err != nil {
			return ControlSent{}, err
		}
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(),
			Kind: envelope.KindMessage, Sub: sub, Body: body, Ref: eref, Conv: ref.Conv, LID: lid, Replica: own[dev.Address], Fan: fan}
		if group != nil {
			members, e := controlMembers(a.store.db, ref.Conv)
			if e != nil {
				return ControlSent{}, e
			}
			in.Fan = append([]envelope.Fan{}, fan...)
			for _, p := range members.persons {
				if p.has(dev.Address, key.Fingerprint()) && !own[dev.Address] {
					in.Fan = append(in.Fan, envelope.Fan{Person: p.roster.Person, Roster: p.roster.Hash()})
				}
			}
		}
		env, err := envelope.Seal(in, a.id.Sign, recipient)
		if err != nil {
			return ControlSent{}, err
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued})
		if group != nil {
			fence, e := groupControlEpochFence(a.store.db, *group, a.Address, a.Self().Fingerprint(), dev.Address, key.Fingerprint())
			if sub == envelope.SubStatus {
				adm, statusErr := groupMemberAdmission(a.store.db, *group, dev.Address, key.Fingerprint())
				e = statusErr
				if e == nil {
					fence = adm.Hash()
				}
			}
			if e != nil {
				return ControlSent{}, e
			}
			copy := &copies[len(copies)-1]
			copy.required = protocol.CapGroup
			copy.recipientFP = key.Fingerprint()
			copy.groupAdmission = fence
		}
		if !own[dev.Address] {
			peerCan = true
		}
	}
	if !peerCan {
		return sent, fmt.Errorf("%w: %s", ErrNoControls, strings.Join(sent.Skipped, "; "))
	}
	local := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: a.Address, To: a.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Sub: sub, Body: body, Ref: eref, Conv: ref.Conv, LID: lid, Fan: fan}
	var guard func(*sql.Tx, string) error
	if group != nil {
		guard = func(tx *sql.Tx, _ string) error {
			current, e := groupTurnPacketIn(tx, ref.Conv)
			if e != nil {
				return e
			}
			if current.State.Hash() != group.State.Hash() {
				return ErrGroupContextPending
			}
			if _, e = controlMembers(tx, ref.Conv); e != nil {
				return e
			}
			if sub == envelope.SubStatus {
				if _, e := a.groupStatusScope(tx, ref, a.Address, a.Self().Fingerprint()); e != nil {
					return e
				}
			}
			for _, copy := range copies {
				fence, e := groupControlEpochFence(tx, current, a.Address, a.Self().Fingerprint(), copy.env.To, copy.recipientFP)
				if sub == envelope.SubStatus {
					adm, statusErr := groupMemberAdmission(tx, current, copy.env.To, copy.recipientFP)
					e = statusErr
					if e == nil {
						fence = adm.Hash()
					}
				}
				if e != nil {
					return e
				}
				if fence != copy.groupAdmission {
					return ErrGroupContextPending
				}
			}
			return nil
		}
	}
	if err := a.store.addConvOutbox(copies, local, guard, ""); err != nil {
		return ControlSent{}, err
	}
	defer notifyDaemon(a.home)
	sent.ID = copies[0].env.ID
	for _, c := range copies {
		state := c.state
		res, err := a.deliver(ctx, c.env, nil)
		switch {
		case err == nil:
			state = res.State
		case retryable(err):
			sent.Detail = err.Error()
		default:
			state, sent.Detail = stateFailed, err.Error()
		}
		if rank(state) < rank(sent.State) {
			sent.State = state
		}
	}
	return sent, nil
}

// threadPeer is the other party of the device message ref names.
func (a *Agent) threadPeer(ref ControlRef) (string, error) {
	var peer string
	var err error
	if ref.Fingerprint == a.Self().Fingerprint() {
		err = a.store.db.QueryRow(`SELECT recipient FROM outbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL`, ref.ID).Scan(&peer)
	} else {
		err = a.store.db.QueryRow(`SELECT sender FROM inbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL AND verified_by = ? AND local = 0`, ref.ID, ref.Fingerprint).Scan(&peer)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoMessage
	}
	return peer, err
}

// addControlOutbox stores a sent device-thread control.
func (s *store) addControlOutbox(env envelope.Envelope, in envelope.Inner) error {
	data, _ := json.Marshal(env)
	now := time.Now()
	_, err := s.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, kind, sub, created_ms, ref_id, ref_fp)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		env.ID, env.To, in.Body, string(data), stateQueued, now.Unix(), in.Kind, in.Sub, now.UnixMilli(), in.Ref.ID, in.Ref.Fingerprint)
	return s.done(err)
}

// addControlInbox stores a received device-thread control: state "" (it
// is nothing to decide or run), never a turn. fromQuarantine releases the
// held envelope in the same step.
func (s *store) addControlInbox(in envelope.Inner, verifiedBy string, fromQuarantine bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := addControlInboxIn(tx, in, verifiedBy, fromQuarantine); err != nil {
		return err
	}
	return s.done(tx.Commit())
}

func addControlInboxIn(tx *sql.Tx, in envelope.Inner, verifiedBy string, fromQuarantine bool) error {
	in = tombstoned(tx, in, verifiedBy) // an edit of a deleted message keeps no text
	now := time.Now()
	if _, err := tx.Exec(`INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, received_at, state, verified_by, sub, received_ms, ref_id, ref_fp, read_at, agent_id, origin)
		VALUES(?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''))`,
		in.ID, in.From, in.TS, in.Kind, in.Body, now.Unix(), verifiedBy, in.Sub, now.UnixMilli(), in.Ref.ID, in.Ref.Fingerprint, now.Unix(), in.AgentID, in.Origin); err != nil {
		return err
	}
	if fromQuarantine {
		if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
			return err
		}
	}
	return nil
}

// admitControl admits a verified version 3 control, or holds it: a
// device-thread control until its target message from that very thread is
// here; a conversation control until the conversation and the sender's
// membership are pinned (as turns are). Authority is not decided here: it
// is checked whenever the target is shown (resolveControls).
func (a *Agent) admitControl(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, fromQuarantine bool) error {
	hold := func(reason, why string) error {
		a.Logf("control %s from %s held (%s): %s", env.ID, env.From, reason, why)
		return a.store.holdAs(env, reason)
	}
	if envelope.AssistantReaction(in) { // the assistant's, decided as its reply would be
		return a.admitAssistantReaction(ctx, env, in, sender, fromQuarantine, hold)
	}
	if in.Sub == envelope.SubClear { // this person's own deletion: no conversation act (convclear.go)
		return a.admitClear(ctx, env, in, sender, fromQuarantine, hold)
	}
	if in.Conv != "" {
		if root, _, found, e := a.store.conversation(in.Conv); e != nil {
			return e
		} else if found && root.Kind == protocol.ConvKindGroup && in.Sub == envelope.SubStatus {
			if ok, why := a.statusAllowed(in, env.From, sender.Fingerprint()); !ok {
				return hold(reasonInvalid, why)
			}
			return a.admitGroupParticipationStatus(ctx, env, in, sender, fromQuarantine, hold)
		} else if found && root.Kind == protocol.ConvKindGroup && !groupControlSub(in.Sub) {
			return hold(reasonInvalid, "group: this control requires its own addressed authority")
		}
	}
	switch in.Sub {
	case envelope.SubDecision: // an operator deciding here: applied or answered, never held
		if fromQuarantine {
			a.store.db.Exec(`DELETE FROM quarantine WHERE id = ?`, env.ID)
		}
		return a.admitDecision(ctx, env, in, sender)
	case envelope.SubStatus: // only the device that holds the request speaks for it
		if ok, why := a.statusAllowed(in, env.From, sender.Fingerprint()); !ok {
			return hold(reasonInvalid, why)
		}
		if in.Conv == "" {
			return a.store.addControlInbox(in, sender.Fingerprint(), fromQuarantine)
		}
	}
	if in.Conv == "" {
		var n int
		if err := a.store.db.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL AND sender = ? AND verified_by = ?)
			+ (SELECT count(*) FROM outbox WHERE id = ? AND conv IS NULL AND ref_id IS NULL AND recipient = ? AND ? = ?)`,
			in.Ref.ID, env.From, in.Ref.Fingerprint, in.Ref.ID, env.From, in.Ref.Fingerprint, a.Self().Fingerprint()).Scan(&n); err != nil {
			return err
		}
		if n == 0 && in.Sub == envelope.SubRetraction {
			ok, e := a.importedReceiverControl(in, sender.Fingerprint())
			if e != nil {
				return e
			}
			if ok {
				n = 1
			}
		}
		if n == 0 {
			return hold(reasonProof, "no message of this thread with that id and key is here (yet)")
		}
		// In a device thread the author is the very key that sent the target:
		// a revision or retraction from any other key is not stored at all.
		if in.Sub != envelope.SubReaction && in.Ref.Fingerprint != sender.Fingerprint() {
			return hold(reasonInvalid, "only the key that sent a message edits or deletes it")
		}
		if err := a.store.addControlInbox(in, sender.Fingerprint(), fromQuarantine); err != nil {
			return err
		}
		if in.Sub == envelope.SubRetraction {
			ref := ControlRef{ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint}
			a.dropCache(ref)
			a.redactRetracted(ref)
		}
		return nil
	}
	root, _, found, err := a.store.conversation(in.Conv)
	if err != nil {
		return err
	}
	if !found {
		return hold(reasonProof, "the conversation is not here (yet)")
	}
	sp, err := a.personOfKey(ctx, env.From, sender)
	if err != nil {
		var he *HubError
		switch {
		case errors.Is(err, errPersonConflict):
			return hold(reasonConflict, err.Error())
		case errors.Is(err, ErrNoPerson), errors.Is(err, errPersonRecord), errors.As(err, &he) && !retryable(err):
			return hold(reasonProof, err.Error())
		}
		return err
	}
	var group *GroupContext
	var groupAdmission string
	checkGroup := func(q dbq) error {
		current, e := groupTurnPacketIn(q, in.Conv)
		if e != nil {
			return e
		}
		if group != nil && current.State.Hash() != group.State.Hash() {
			return ErrGroupContextPending
		}
		if e = groupTurnCheck(q, current, a.Address, a.Self().Fingerprint()); e != nil {
			return e
		}
		if e = groupTurnCheck(q, current, sender.Address, sender.Fingerprint()); e != nil {
			return e
		}
		m, e := controlMembers(q, in.Conv)
		if e != nil {
			return e
		}
		if reason, why := a.controlAuthorized(m, in, sp.info.Person); reason != "" {
			if reason == reasonProof {
				return ErrGroupContextPending
			}
			return errors.New(why)
		}
		return a.groupControlTarget(q, in)
	}
	if root.Kind == protocol.ConvKindGroup {
		packet, e := groupTurnPacketIn(a.store.db, in.Conv)
		if e != nil {
			return hold(reasonProof, e.Error())
		}
		for _, member := range packet.State.Members {
			if _, e = a.refreshPerson(ctx, member.Person, false); e != nil {
				return e
			}
		}
		group = &packet
		if e = checkGroup(a.store.db); e != nil {
			if errors.Is(e, ErrGroupContextPending) {
				return hold(reasonProof, e.Error())
			}
			return hold(reasonInvalid, e.Error())
		}
		admission, e := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
		if e != nil {
			return e
		}
		groupAdmission = admission.Hash()
	} else {
		if r, member := root.Member(sp.info.Person); !member {
			return hold(reasonInvalid, "the sender is not a member of this conversation")
		} else if ok, err := a.boundIn(ctx, sp.info.Person, r); err != nil {
			return err
		} else if !ok {
			return hold(reasonInvalid, "the root binds the sender's person to a roster step its chain does not have")
		}
	}
	// Authority is decided here, before anything is stored or removed: a
	// revision or retraction counts only from a device of the very person
	// whose device sent the target (as the pinned rosters say now); a key
	// no member ever had waits for evidence; another person's is refused.
	m, err := controlMembers(a.store.db, in.Conv)
	if err != nil {
		return err
	}
	if reason, why := a.controlAuthorized(m, in, sp.info.Person); reason != "" {
		return hold(reason, why)
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return hold(reasonInvalid, "this installation has no person")
	}
	_, raw, _, err := a.store.conversation(in.Conv)
	if err != nil {
		return err
	}
	var forward []outCopy
	if group == nil {
		forward = a.forwardStale(me, in, sender.Fingerprint(), raw)
	} else {
		forward = a.forwardStaleWithControlProof(me, in, sender.Fingerprint(), raw, &groupControlIngressProof{in: in, key: sender.Fingerprint(), admission: groupAdmission}, groupAdmission)
	}
	also := func(tx *sql.Tx) error {
		if group != nil {
			if e := checkGroup(tx); e != nil {
				return e
			}
			current, e := groupTurnPacketIn(tx, in.Conv)
			if e != nil {
				return e
			}
			admission, e := groupMemberAdmission(tx, current, a.Address, a.Self().Fingerprint())
			if e != nil {
				return e
			}
			if admission.Hash() != groupAdmission {
				return ErrGroupContextPending
			}
			if _, e = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, groupAdmission, in.ID); e != nil {
				return e
			}
			for _, copy := range forward {
				var item HistoryItem
				if decodeStrict([]byte(copy.in.Body), &item) != nil {
					return errors.New("group: malformed forwarded control")
				}
				if copy.groupAdmission != groupAdmission {
					return ErrGroupContextPending
				}
				if e = a.groupControlHistoryCheck(tx, current.Root, a.Self(), item); e != nil {
					return e
				}
				if e = groupTurnCheck(tx, current, copy.env.To, copy.recipientFP); e != nil {
					return e
				}
				me, ok, e := scanPersonIn(tx, "state = ?", personSelf)
				if e != nil {
					return e
				}
				if !ok || !me.has(copy.env.To, copy.recipientFP) {
					return errors.New("group: forwarded control target left own roster")
				}
			}
		}
		return insertCopies(tx, forward)
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, also)
	if err != nil {
		return err
	}
	if res != admitted {
		return nil
	}
	if len(forward) > 0 {
		a.kickNow()
	}
	a.applyRetraction(in)
	return nil
}

// inScope matches the inbox rows (alias i) a reference names: the exact
// message in a device thread, or the logical id within that conversation,
// under that key. Equal ids elsewhere never match.
func inScope(ref ControlRef) (string, []any) {
	if ref.Conv == "" {
		return `i.conv IS NULL AND i.id = ? AND coalesce(i.verified_by, i.claimed_fp, '') = ?`, []any{ref.ID, ref.Fingerprint}
	}
	return `i.conv = ? AND i.lid = ? AND coalesce(i.verified_by, i.claimed_fp, '') = ?`, []any{ref.Conv, ref.ID, ref.Fingerprint}
}

// outScope is inScope for outbox rows (alias o): this device's own copies
// of the message, so only when the reference names this device's key. A
// received message with the same id (ids are the sender's choice) never
// reaches a sent one.
func outScope(ref ControlRef, selfFP string) (string, []any) {
	if ref.Fingerprint != selfFP {
		return `0`, nil
	}
	if ref.Conv == "" {
		return `o.conv IS NULL AND o.id = ?`, []any{ref.ID}
	}
	return `o.conv = ? AND o.lid = ?`, []any{ref.Conv, ref.ID}
}

// dropCache removes this device's cached ciphertext of the files of a
// retracted received message, exactly the one the reference names (its
// scope, id and key): what the person saved stays where they put it, and
// the manifest rows stay for the record. Nothing is fetched for it again
// (prefetchFiles). Called only after the retraction's authority was checked.
func (a *Agent) dropCache(ref ControlRef) {
	where, args := inScope(ref)
	rows, err := a.store.db.Query(`SELECT a.blob_id FROM attachments a JOIN inbox i ON i.id = a.message_id WHERE i.local = 0 AND `+where, args...)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var blob string
		if rows.Scan(&blob) == nil && !strings.HasPrefix(blob, historyBlob) {
			os.Remove(a.downloadPath(blob))
			os.Remove(a.downloadPath(blob) + ".part")
		}
	}
}

// retractedOut matches outbox rows (alias o) this device retracted itself.
const retractedOut = `EXISTS (SELECT 1 FROM outbox c WHERE c.sub = 'retraction' AND ((o.conv IS NULL AND c.conv IS NULL AND c.ref_id = o.id) OR (o.conv IS NOT NULL AND c.conv = o.conv AND c.ref_id = o.lid)))`

// dropKept removes this device's kept copies of the files of a message it
// sent and retracted, unless a sent message that is not retracted still
// shares a file: the last retraction of a shared file removes it.
func (a *Agent) dropKept(ref ControlRef) {
	where, args := outScope(ref, a.Self().Fingerprint())
	rows, err := a.store.db.Query(`SELECT s.sha256 FROM sent_attachments s JOIN outbox o ON o.id = s.message_id WHERE `+where, args...)
	if err != nil {
		return
	}
	var shas []string
	for rows.Next() {
		var sha string
		if rows.Scan(&sha) == nil {
			shas = append(shas, sha)
		}
	}
	rows.Close()
	for _, sha := range shas {
		var n int
		a.store.db.QueryRow(`SELECT count(*) FROM sent_attachments s JOIN outbox o ON o.id = s.message_id
			WHERE s.sha256 = ? AND NOT (`+where+`) AND NOT `+retractedOut, append([]any{sha}, args...)...).Scan(&n)
		if n == 0 {
			os.Remove(a.keptPath(sha))
		}
	}
}

// redactRetracted removes the text of a deleted ordinary message from this
// device: the target's body (received copies and this device's own sent
// copies) and the text of every edit of it, in that exact scope. A
// question or task keeps its admitted body: that is what an agent ran or
// runs, disclosed as such. Rows, receipts, ids and links stay, so nothing
// is delivered or run twice. Copies already delivered elsewhere, saved or
// given to an agent are not reached: nothing here claims otherwise.
func (a *Agent) redactRetracted(ref ControlRef) {
	inWhere, inArgs := inScope(ref)
	outWhere, outArgs := outScope(ref, a.Self().Fingerprint())
	a.store.db.Exec(`UPDATE inbox SET body = '' WHERE id IN (SELECT i.id FROM inbox i WHERE `+inWhere+`) AND kind = ?`, append(inArgs, envelope.KindMessage)...)
	a.store.db.Exec(`UPDATE outbox SET body = '' WHERE id IN (SELECT o.id FROM outbox o WHERE `+outWhere+`) AND coalesce(kind, json_extract(envelope, '$.kind')) = ?`, append(outArgs, envelope.KindMessage)...)
	// Edits of it, whatever kind it was: their text was never admitted for anything.
	convWhere, convArg := `conv IS NULL`, []any{}
	if ref.Conv != "" {
		convWhere, convArg = `conv = ?`, []any{ref.Conv}
	}
	revArgs := append(append([]any{}, convArg...), ref.ID, ref.Fingerprint, envelope.SubRevision)
	a.store.db.Exec(`UPDATE inbox SET body = '' WHERE `+convWhere+` AND ref_id = ? AND ref_fp = ? AND sub = ?`, revArgs...)
	a.store.db.Exec(`UPDATE outbox SET body = '' WHERE `+convWhere+` AND ref_id = ? AND ref_fp = ? AND sub = ?`, revArgs...)
	a.redactGroupHistoryCopies(ref)
	a.redactReceiverDelegations(ref)
	a.store.done(nil)
}

// retractedRef reports whether an authorized retraction of the exact
// reference (a device message id, or a logical id within conv, under key)
// is pinned here: received (inbox) or this device's own (outbox). Only
// authorized retractions are ever stored (admitControl, admitHistory).
func retractedRef(q querier, conv, id, key string) bool {
	var n int
	if conv == "" {
		q.QueryRow(`SELECT (SELECT count(*) FROM inbox c WHERE c.sub = 'retraction' AND c.conv IS NULL AND c.ref_id = ? AND c.ref_fp = ?)
			+ (SELECT count(*) FROM outbox c WHERE c.sub = 'retraction' AND c.conv IS NULL AND c.ref_id = ? AND c.ref_fp = ?)`, id, key, id, key).Scan(&n)
	} else {
		q.QueryRow(`SELECT (SELECT count(*) FROM inbox c WHERE c.sub = 'retraction' AND c.conv = ? AND c.ref_id = ? AND c.ref_fp = ?)
			+ (SELECT count(*) FROM outbox c WHERE c.sub = 'retraction' AND c.conv = ? AND c.ref_id = ? AND c.ref_fp = ?)`, conv, id, key, conv, id, key).Scan(&n)
	}
	return n > 0
}

// tombstoned returns in with its text removed when a retraction of the
// message it is (an ordinary turn) or edits (a revision) is pinned here
// already: whatever order things arrive in, deleted ordinary text is never
// stored. A question or task keeps its admitted text; the sender key is
// the one the row is stored under (verified, or claimed for history).
func tombstoned(q querier, in envelope.Inner, key string) envelope.Inner {
	if key == "" || in.Body == "" {
		return in
	}
	switch {
	case in.Sub == "" && in.Kind == envelope.KindMessage:
		id := in.ID
		if in.Conv != "" {
			id = in.LID
		}
		if retractedRef(q, in.Conv, id, key) {
			in.Body = ""
		}
	case in.Sub == envelope.SubRevision && in.Ref != nil:
		if retractedRef(q, in.Conv, in.Ref.ID, in.Ref.Fingerprint) {
			in.Body = ""
		}
	}
	return in
}

// retractedClause matches inbox rows (alias i) whose message was retracted
// by a control held here; only an authorized retraction is ever held (admitControl).
const retractedClause = `EXISTS (SELECT 1 FROM inbox c WHERE c.ref_id IN (i.id, i.lid) AND c.ref_fp = coalesce(i.verified_by, i.claimed_fp, '') AND c.sub = 'retraction'
	AND ((i.conv IS NULL AND c.conv IS NULL AND c.ref_id = i.id) OR (i.conv IS NOT NULL AND c.conv = i.conv AND c.ref_id = i.lid)))`

// retracted reports whether received message id was deleted by its sender
// (a retraction control held here).
func (s *store) retracted(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM inbox i WHERE i.id = ? AND `+retractedClause, id).Scan(&n)
	return n > 0, err
}

// controlRow is one control as stored. Its id breaks ties between controls
// with equal counters, so it is the same on every device: the logical id
// in a conversation (each device holds its own copy under its own envelope
// id), the envelope id in a device thread (one copy each side).
type controlRow struct {
	id, sub, author, authorFP, refID, refFP, body string
	ms                                            int64
	pid, agentID, origin                          string // an assistant reaction's reactor (assistantreaction.go)
}

// legacyControls loads the controls of the device thread with peer.
func (s *store) legacyControls(peer, self, selfFP string) ([]controlRow, error) {
	rows, err := s.db.Query(`SELECT id, sub, sender, coalesce(verified_by, ''), ref_id, ref_fp, body, coalesce(received_ms, received_at * 1000), coalesce(pid, ''), coalesce(agent_id, ''), coalesce(origin, '')
		  FROM inbox WHERE conv IS NULL AND ref_id IS NOT NULL AND sender = ?
		UNION ALL SELECT id, sub, ?, ?, ref_id, ref_fp, body, coalesce(created_ms, created_at * 1000), coalesce(pid, ''), coalesce(agent_id, ''), coalesce(origin, '')
		  FROM outbox WHERE conv IS NULL AND ref_id IS NOT NULL AND recipient = ?`, peer, self, selfFP, peer)
	if err != nil {
		return nil, err
	}
	return scanControls(rows)
}

// convControls loads the controls of conversation conv, once per logical
// id for copies this device sent, each under its logical id.
func (s *store) convControls(conv, self, selfFP string) ([]controlRow, error) {
	rows, err := s.db.Query(`SELECT coalesce(lid, id), sub, sender, coalesce(verified_by, claimed_fp, ''), ref_id, ref_fp, body, coalesce(received_ms, received_at * 1000), coalesce(pid, ''), coalesce(agent_id, ''), coalesce(origin, '')
		  FROM inbox WHERE conv = ? AND ref_id IS NOT NULL AND local = 0
		UNION ALL SELECT coalesce(o.lid, o.id), o.sub, ?, ?, o.ref_id, o.ref_fp, o.body, coalesce(o.created_ms, o.created_at * 1000), coalesce(o.pid, ''), coalesce(o.agent_id, ''), coalesce(o.origin, '')
		  FROM outbox o WHERE o.conv = ? AND o.ref_id IS NOT NULL AND o.rowid = (SELECT min(rowid) FROM outbox f WHERE f.conv = o.conv AND f.lid = o.lid)`,
		conv, self, selfFP, conv)
	if err != nil {
		return nil, err
	}
	return scanControls(rows)
}

func scanControls(rows *sql.Rows) ([]controlRow, error) {
	defer rows.Close()
	var out []controlRow
	for rows.Next() {
		var r controlRow
		if err := rows.Scan(&r.id, &r.sub, &r.author, &r.authorFP, &r.refID, &r.refFP, &r.body, &r.ms, &r.pid, &r.agentID, &r.origin); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// authority decides, for one control, who its author is for the record
// (label) and for merging (who: one value per person, or per device in a
// device thread), and whether the author may edit or delete the target.
type authority func(c controlRow, target ControlRef) (who, label string, mine, mayReact, mayAuthor bool)

// resolveControls folds the controls of one scope into what each target
// shows. Equal ids of different keys never meet: the map key is the ref.
func resolveControls(rows []controlRow, auth authority) map[ControlRef]Controls {
	type mark struct {
		op string
		n  int64
		id string
	}
	type target struct {
		reactions map[string]map[string]mark // emoji -> who -> newest
		labels    map[string]string          // who -> label
		actors    map[string]Reactor         // who -> an assistant's identity
		mine      map[string]bool
		rev       int64
		revID     string
		text      string
		deleted   bool
	}
	targets := map[ControlRef]*target{}
	for _, c := range rows {
		ref := ControlRef{ID: c.refID, Fingerprint: c.refFP}
		who, label, mine, mayReact, mayAuthor := auth(c, ref)
		t := targets[ref]
		if t == nil {
			t = &target{reactions: map[string]map[string]mark{}, labels: map[string]string{}, mine: map[string]bool{}, actors: map[string]Reactor{}}
			targets[ref] = t
		}
		switch c.sub {
		case envelope.SubReaction:
			var r envelope.Reaction
			if !mayReact || json.Unmarshal([]byte(c.body), &r) != nil {
				continue
			}
			byWho := t.reactions[r.Emoji]
			if byWho == nil {
				byWho = map[string]mark{}
				t.reactions[r.Emoji] = byWho
			}
			if cur, ok := byWho[who]; !ok || r.N > cur.n || (r.N == cur.n && c.id > cur.id) {
				byWho[who] = mark{r.Op, r.N, c.id}
			}
			t.labels[who], t.mine[who] = label, mine
			if _, ok := assistantWho(c); ok {
				t.actors[who] = Reactor{Assistant: true, Host: c.author, AgentID: c.agentID, PID: c.pid}
			}
		case envelope.SubRevision:
			var r envelope.Revision
			if !mayAuthor || json.Unmarshal([]byte(c.body), &r) != nil {
				continue
			}
			if r.Rev > t.rev || (r.Rev == t.rev && c.id > t.revID) {
				t.rev, t.revID, t.text = r.Rev, c.id, r.Text
			}
		case envelope.SubRetraction:
			if mayAuthor {
				t.deleted = true
			}
		}
	}
	out := map[ControlRef]Controls{}
	for ref, t := range targets {
		var c Controls
		emojis := make([]string, 0, len(t.reactions))
		for e := range t.reactions {
			emojis = append(emojis, e)
		}
		slices.Sort(emojis)
		for _, e := range emojis {
			v := ReactionView{Emoji: e}
			whos := make([]string, 0, len(t.reactions[e]))
			for who, m := range t.reactions[e] {
				if m.op == "add" {
					whos = append(whos, who)
				}
			}
			slices.Sort(whos)
			for _, who := range whos {
				r := t.actors[who]
				r.ID, r.Label = who, t.labels[who]
				v.By = append(v.By, r)
				v.Mine = v.Mine || t.mine[who]
			}
			if len(v.By) > 0 {
				c.Reactions = append(c.Reactions, v)
			}
		}
		if t.rev > 0 {
			c.Edited, c.Revision, c.Text = true, t.rev, t.text
		}
		c.Deleted = t.deleted
		out[ref] = c
	}
	return out
}

// display is what a legacy message shows after its controls.
type display struct {
	Deleted, Edited bool
	Text            string
}

// legacyDisplay resolves the controls on one device message (id, in the
// thread with peer), for contexts that read rows directly (threadText).
func (s *store) legacyDisplay(id, peer string) display {
	var fp string
	if err := s.db.QueryRow(`SELECT coalesce(verified_by, '') FROM inbox WHERE id = ? AND sender = ?`, id, peer).Scan(&fp); err != nil {
		return display{} // a sent message: its key is this device's, which the store does not know; the worker shows sent text as sent
	}
	rows, err := s.legacyControls(peer, "", "")
	if err != nil {
		return display{}
	}
	c := resolveControls(rows, legacyAuthority(peer, "", ""))[ControlRef{ID: id, Fingerprint: fp}]
	return display{Deleted: c.Deleted, Edited: c.Edited, Text: c.Text}
}

// legacyAuthority: in a device thread the two parties may react; the
// author is the very key that sent the target.
func legacyAuthority(peer, self, selfFP string) authority {
	return func(c controlRow, target ControlRef) (who, label string, mine, mayReact, mayAuthor bool) {
		mine = c.author == self && self != ""
		if who, ok := assistantWho(c); ok { // never its host device's own mark
			return who, assistantLabel(c.author, c.agentID), false, c.author == peer || mine, false
		}
		return c.author, c.author, mine, c.author == peer || mine, c.authorFP == target.Fingerprint && c.authorFP != ""
	}
}

// decorateLegacy fills Controls and Can on the messages of the thread with
// peer.
func (a *Agent) decorateLegacy(peer string, msgs []ConversationMessage) error {
	self, selfFP := a.Address, a.Self().Fingerprint()
	rows, err := a.store.legacyControls(peer, self, selfFP)
	if err != nil {
		return err
	}
	resolved := resolveControls(rows, legacyAuthority(peer, self, selfFP))
	a.labelOwnAssistants(resolved)
	execs := execViews(rows)
	for i := range msgs {
		m := &msgs[i]
		fp := selfFP
		if m.Dir == "in" {
			var got string
			if a.store.db.QueryRow(`SELECT coalesce(verified_by, '') FROM inbox WHERE id = ?`, m.ID).Scan(&got) != nil || got == "" {
				continue
			}
			fp = got
		} else if e, ok := execs[ControlRef{ID: m.ID, Fingerprint: selfFP}]; ok && e.Host == peer { // my request: the peer's word on it
			status, at := legacyAnswer(msgs, m.ID)
			e.settle(a.hostConnected(peer), status, at, time.Now().Unix())
			m.Exec = &e
		}
		m.Controls = resolved[ControlRef{ID: m.ID, Fingerprint: fp}]
		if m.Deleted && m.Kind == envelope.KindMessage {
			m.Body = "" // an ordinary message's text is gone; a request keeps what its agent ran
		}
		if !m.Deleted {
			m.Can = []string{CanReact}
			if m.Dir == "out" {
				m.Can = append(m.Can, CanEdit, CanDelete)
			}
		}
	}
	return nil
}

// controlAuthorized decides, for a conversation control from a device of
// person author, whether it may be stored: a reaction needs membership
// only (the caller checked); a revision or retraction needs the target's
// sender key to belong to the same person. why says what is missing.
func (a *Agent) controlAuthorized(m dmMembers, in envelope.Inner, author string) (reason, why string) {
	if in.Sub == envelope.SubReaction {
		return "", ""
	}
	switch owner := a.personOfKeyIn(m, in.Ref.Fingerprint); {
	case owner == "":
		return reasonProof, "the target's sender key is no member's (yet)"
	case owner != author:
		return reasonInvalid, "only the person who sent a message edits or deletes it"
	}
	return "", ""
}

// applyRetraction does what an authorized, stored retraction does here.
func (a *Agent) applyRetraction(in envelope.Inner) {
	if in.Sub != envelope.SubRetraction {
		return
	}
	ref := ControlRef{Conv: in.Conv, ID: in.Ref.ID, Fingerprint: in.Ref.Fingerprint}
	a.dropCache(ref)
	a.redactRetracted(ref)
}

// personOfKeyIn is the member person a device key belongs to, current or
// past, or "".
func (a *Agent) personOfKeyIn(m dmMembers, fp string) string {
	for id, p := range m.persons {
		for _, d := range p.roster.Devices {
			if d.Fingerprint() == fp {
				return id
			}
		}
	}
	for id, p := range m.persons {
		for _, d := range p.roster.Devices {
			if _, ok := a.store.deviceKey(id, d.Address, fp); ok {
				return id
			}
		}
	}
	// A device removed from the roster: search each chain by its known
	// addresses is above; unknown keys belong to no one.
	return ""
}

// controlsOf is what the controls held here did to the one message ref
// names, resolved as its view resolves them (decorateLegacy, decorateConv).
func (a *Agent) controlsOf(ref ControlRef) (Controls, error) {
	self, selfFP := a.Address, a.Self().Fingerprint()
	target := ControlRef{ID: ref.ID, Fingerprint: ref.Fingerprint}
	if ref.Conv == "" {
		peer, err := a.threadPeer(ref)
		if err != nil {
			return Controls{}, err
		}
		rows, err := a.store.legacyControls(peer, self, selfFP)
		if err != nil {
			return Controls{}, err
		}
		return resolveControls(rows, legacyAuthority(peer, self, selfFP))[target], nil
	}
	rows, err := a.store.convControls(ref.Conv, self, selfFP)
	if err != nil {
		return Controls{}, err
	}
	m, err := controlMembers(a.store.db, ref.Conv)
	if err != nil {
		return Controls{}, err
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return Controls{}, err
	}
	auth, _ := a.convAuthority(ref.Conv, m, me)
	return resolveControls(rows, auth)[target], nil
}

// convAuthority is how the controls of conversation conv resolve here: a
// reaction counts per member person (an assistant's as its own), an edit
// or deletion only from the target's person, as the members m pinned now
// say; me is this device's person. person is the member person of a key.
func (a *Agent) convAuthority(conv string, m dmMembers, me personRow) (auth authority, person func(fp string) string) {
	personOf := map[string]string{}
	person = func(fp string) string {
		if p, ok := personOf[fp]; ok {
			return p
		}
		p := a.personOfKeyIn(m, fp)
		personOf[fp] = p
		return p
	}
	auth = func(c controlRow, target ControlRef) (who, label string, mine, mayReact, mayAuthor bool) {
		if who, ok := assistantWho(c); ok { // admitted as the participation's own: never a person's mark
			host := c.author
			if p, e := a.participation(conv, c.pid); e == nil && p.Host.Label != "" {
				host = p.Host.Label
			}
			return who, assistantLabel(host, c.agentID), false, true, false
		}
		who = person(c.authorFP)
		if who == "" {
			return "", "", false, false, false // no member's key: nothing
		}
		label = m.persons[who].info.Label
		mine = who == me.info.Person
		owner := person(target.Fingerprint)
		return who, label, mine, true, owner != "" && owner == who
	}
	return auth, person
}

// decorateConv fills Controls and Can on the turns of conversation conv.
func (a *Agent) decorateConv(conv string, msgs []ConvMessage) error {
	if _, _, found, err := a.store.conversation(conv); err != nil || !found {
		return err // not held here (yet): nothing to resolve against
	}
	self, selfFP := a.Address, a.Self().Fingerprint()
	rows, err := a.store.convControls(conv, self, selfFP)
	if err != nil {
		return err
	}
	m, err := controlMembers(a.store.db, conv)
	if err != nil {
		root, _, found, e := a.store.conversation(conv)
		if e == nil && found && root.Kind == protocol.ConvKindGroup && (errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict)) {
			return nil // held current authority: retained history has no actions
		}
		return err
	}
	mayControl := m.root.Kind != protocol.ConvKindGroup || m.device(a.Address, selfFP)
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	auth, person := a.convAuthority(conv, m, me)
	resolved := resolveControls(rows, auth)
	a.labelOwnAssistants(resolved)
	execs := execViews(rows)
	for i := range msgs {
		msg := &msgs[i]
		if msg.Sub != "" {
			continue // events and history carriers are records, not turns
		}
		if msg.Target != nil && (msg.Kind == envelope.KindQuestion || msg.Kind == envelope.KindTask) {
			key := msg.Key
			if msg.Dir == "out" && msg.Via == "" {
				key = selfFP
			}
			if e, ok := execs[ControlRef{ID: msg.LID, Fingerprint: key}]; ok && e.Host == msg.Target.Address { // only the target device speaks for it (admission checked the key)
				e.settle(a.hostConnected(e.Host), "", 0, time.Now().Unix())
				msg.Exec = &e
			}
		}
		fp := msg.Key
		switch {
		case msg.Dir == "out" && msg.Via == "":
			fp = selfFP
		case fp == "":
			fp = msg.Claimed // history: the forwarding device's word, as the turn itself is
		}
		if fp == "" {
			continue
		}
		msg.Controls = resolved[ControlRef{ID: msg.LID, Fingerprint: fp}]
		if msg.Deleted && msg.Kind == envelope.KindMessage {
			msg.Body = ""
		}
		if !msg.Deleted && mayControl {
			msg.Can = []string{CanReact}
			if person(fp) == me.info.Person && envelope.AgentOrigin(msg.Origin) == false {
				msg.Can = append(msg.Can, CanEdit, CanDelete)
			} else if person(fp) == me.info.Person {
				msg.Can = append(msg.Can, CanDelete) // an agent's output: its person may delete, not rewrite
			}
		}
	}
	return nil
}
