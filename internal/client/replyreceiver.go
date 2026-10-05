package client

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const replyReceiverSchema = `
CREATE TABLE reply_receivers (
 id TEXT PRIMARY KEY, conv TEXT NOT NULL, request_ref TEXT NOT NULL,
 receiver TEXT NOT NULL, executor TEXT, return_person TEXT,
 created_at INTEGER NOT NULL, canceled_at INTEGER,
 UNIQUE(conv,request_ref)
);
ALTER TABLE outbox ADD COLUMN reply_receiver TEXT REFERENCES reply_receivers(id);
CREATE TABLE reply_receiver_inputs (
 binding TEXT NOT NULL REFERENCES reply_receivers(id),
 inbox_id TEXT NOT NULL UNIQUE REFERENCES inbox(id),
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','accepted','completed')),
 accepted_at INTEGER, completed_at INTEGER,
 PRIMARY KEY(binding,inbox_id)
);
`

// ManagedReplyHandoff is explicit local closed-session delegation. Derived
// preset, origin and closure proof are private persisted state, never choices.
type ManagedReplyHandoff struct {
	AgentID      string `json:"agent_id"`
	Instructions string `json:"instructions"`
	Mode         string `json:"mode"`
}

func (r *ManagedReplyHandoff) UnmarshalJSON(data []byte) error {
	type plain ManagedReplyHandoff
	var v plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	*r = ManagedReplyHandoff(v)
	return nil
}
func sameOnClose(a, b *ManagedReplyHandoff) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ReplyReceiver is an explicit local return destination, independent of the
// remote executor and author. Cross-device choices travel only in an encrypted
// delegation to the selected linked host. Active registration is not liveness.
type ReplyReceiver struct {
	Host          *ReplyReceiverHost   `json:"host,omitempty"`
	OnClose       *ManagedReplyHandoff `json:"on_close,omitempty"`
	Kind          string               `json:"kind"` // human, managed_agent or live_session
	AgentID       string               `json:"agent_id,omitempty"`
	SessionHandle string               `json:"session_handle,omitempty"`
	Instructions  string               `json:"instructions,omitempty"` // original local delegation, never reply text
	Mode          string               `json:"mode,omitempty"`         // question or task under the selected harness's normal permissions
	BindingID     string               `json:"binding_id,omitempty"`   // local follow-up only, retaining frozen context
	Preset        string               `json:"preset,omitempty"`       // derived locally, never a caller-provided execution choice
}

var receiverHandle = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type replyBinding struct {
	receiver ReplyReceiver
	resolve  func(dbq) (*ExecutorStamp, error)
	person   string
	existing string
	guard    func(dbq) error
	handoff  *closedHandoffState
	setup    *outCopy
	remote   *receiverRemoteState
}
type boundOutgoing struct {
	binding     *replyBinding
	fingerprint string
}

func checkNewReplyHandoff(r ReplyReceiver, registered replySessionRecord) error {
	if r.BindingID == "" && r.Kind == "live_session" && r.OnClose != nil && registered.Harness == "claude" {
		return errors.New("Claude closed-session backup is unavailable: native closure cannot authorize handoff")
	}
	return nil
}

func (a *Agent) prepareReplyReceiver(selected *ReplyReceiver) (*replyBinding, error) {
	if selected == nil {
		return nil, nil
	}
	r := *selected // a later picker change cannot mutate an existing send
	if r.OnClose != nil {
		copy := *r.OnClose
		r.OnClose = &copy
	}
	var handoff *closedHandoffState
	var remote *receiverRemoteState
	if r.BindingID != "" {
		old, err := replyReceiverIn(a.store.db, r.BindingID)
		if err != nil {
			return nil, err
		}
		if r.Kind != "" && (r.Kind != old.Receiver.Kind || r.AgentID != old.Receiver.AgentID) || r.SessionHandle != "" && r.SessionHandle != old.Receiver.SessionHandle {
			return nil, errors.New("reply binding cannot redirect its receiver")
		}
		if r.Host != nil && (old.Receiver.Host == nil || *r.Host != *old.Receiver.Host) {
			return nil, errors.New("reply binding cannot redirect its receiver host")
		}
		if r.OnClose != nil && !sameOnClose(r.OnClose, old.Receiver.OnClose) {
			return nil, errors.New("reply binding cannot replace closed-session delegation")
		}
		if r.Mode != "" && r.Mode != old.Receiver.Mode || r.Instructions != "" && r.Instructions != old.Receiver.Instructions {
			return nil, errors.New("reply binding cannot replace original mode or instructions")
		}
		r = old.Receiver
		handoff = old.handoff
		remote = old.remote
		r.BindingID = old.ID
	}
	if r.Host != nil {
		key, err := replyReceiverHostIn(a.store.db, *r.Host)
		if err != nil {
			return nil, err
		}
		if key.Address != a.Address {
			return a.prepareRemoteReplyReceiver(r)
		}
		r.Host = nil // exact self selection uses the existing local path
	}
	if r.OnClose != nil {
		if r.Kind != "live_session" && !(r.Kind == "managed_agent" && r.BindingID != "" && handoff != nil && handoff.Applied) {
			return nil, errors.New("closed-session delegation requires an exact live receiver")
		}
		r.OnClose.Instructions = strings.TrimSpace(r.OnClose.Instructions)
		if !protocol.ValidID(r.OnClose.AgentID) || r.OnClose.Instructions == "" || len(r.OnClose.Instructions) > maxFollowUp || r.OnClose.Mode != envelope.KindQuestion && r.OnClose.Mode != envelope.KindTask {
			return nil, errors.New("closed-session delegation requires exact managed agent, local instructions and question/task mode")
		}
		if r.BindingID == "" {
			if r.Preset != "" {
				return nil, errors.New("closed-session preset is derived locally")
			}
			handoff = &closedHandoffState{OriginSession: r.SessionHandle}
		}
	}
	switch r.Kind {
	case "human":
		if r.AgentID != "" || r.SessionHandle != "" || r.Mode != "" || r.Instructions != "" {
			return nil, errors.New("human reply receiver has no agent or session")
		}
	case "managed_agent":
		if !protocol.ValidID(r.AgentID) || r.SessionHandle != "" {
			return nil, ErrUnknownAgent
		}
		r.Instructions = strings.TrimSpace(r.Instructions)
		if r.Instructions == "" || len(r.Instructions) > maxFollowUp || r.Mode != envelope.KindQuestion && r.Mode != envelope.KindTask {
			return nil, errors.New("managed reply receiver requires explicit local continuation instructions and question/task mode")
		}
	case "live_session":
		if r.AgentID != "" || r.Mode != "" || r.Instructions != "" || !receiverHandle.MatchString(r.SessionHandle) {
			return nil, errors.New("reply receiver requires an opaque local session handle")
		}
		registered, e := replySessionIn(a.store.db, r.SessionHandle)
		if e != nil {
			return nil, errors.New("selected native reply session is not registered")
		}
		if e = a.checkReplySession(a.store.db, registered); e != nil {
			return nil, e
		}
		if e = checkNewReplyHandoff(r, registered); e != nil {
			return nil, e
		}
	default:
		return nil, errors.New("unknown local reply receiver kind")
	}
	b := &replyBinding{receiver: r, existing: r.BindingID, handoff: handoff, remote: remote}
	if b.existing != "" {
		b.guard = func(q dbq) error {
			old, e := replyReceiverIn(q, b.existing)
			if e != nil {
				return e
			}
			if old.State == "canceled" {
				return errors.New("reply binding canceled")
			}
			if e = groupReceiverBinding(q, old); e != nil {
				return e
			}
			if old.Receiver.Kind == "managed_agent" {
				return a.receiverSnapshot(q, old)
			}
			if old.Receiver.Kind == "live_session" {
				_, e = a.liveReplyBinding(q, old)
				return e
			}
			return nil
		}
	}
	b.resolve = func(q dbq) (*ExecutorStamp, error) {
		if r.Kind == "live_session" {
			registered, e := replySessionIn(q, r.SessionHandle)
			if e != nil {
				return nil, e
			}
			if e = a.checkReplySession(q, registered); e != nil {
				return nil, e
			}
			if e = checkNewReplyHandoff(r, registered); e != nil {
				return nil, e
			}
			if r.OnClose != nil {
				return a.ResolveExecutorIn(q, r.OnClose.AgentID, nil)
			}
			return nil, nil
		}
		if r.Kind != "managed_agent" {
			return nil, nil
		}
		return a.ResolveExecutorIn(q, r.AgentID, nil) // never default
	}
	stamp, err := b.resolve(a.store.db)
	if err != nil {
		return nil, err
	}
	if stamp != nil {
		if b.existing != "" {
			old, e := replyReceiverIn(a.store.db, b.existing)
			if e != nil {
				return nil, e
			}
			if old.Receiver.Kind == "managed_agent" {
				e = a.receiverSnapshot(a.store.db, old)
			} else {
				e = a.closedHandoffSnapshot(a.store.db, old)
			}
			if e != nil {
				return nil, e
			}
		} else {
			if r.OnClose != nil {
				b.handoff.Preset = receiverPreset(stamp, r.OnClose.Mode)
			} else {
				b.receiver.Preset = receiverPreset(stamp, r.Mode)
			}
		}
	}
	return b, nil
}

// bindReplyReceiver is part of the same transaction as the outgoing copies.
// It links eligible original request copies to one local obligation, not jobs.
func bindReplyReceiver(tx *sql.Tx, b *replyBinding, copies []outCopy) error {
	if b == nil {
		return nil
	}
	if len(copies) == 0 {
		return errors.New("reply receiver requires a deliverable original request")
	}
	first := copies[0].in
	if first.Sub != "" || first.AgentID != "" || first.Kind != envelope.KindMessage && first.Kind != envelope.KindQuestion && first.Kind != envelope.KindTask {
		return errors.New("reply receiver binds an original message, question or task")
	}
	ref := first.ID
	if first.Conv != "" {
		ref = first.LID
	}
	stamp, err := b.resolve(tx)
	if err != nil {
		return err
	}
	if b.guard != nil {
		if err = b.guard(tx); err != nil {
			return err
		}
	}
	var executor any
	if stamp != nil {
		data, _ := json.Marshal(stamp)
		executor = string(data)
	}
	initialSelected, _ := encodeReceiverBinding(ReplyReceiverBinding{Receiver: b.receiver, handoff: b.handoff, remote: b.remote})
	id := b.existing
	if id != "" {
		old, e := replyReceiverIn(tx, id)
		if e != nil {
			return e
		}
		if old.State == "canceled" || old.Conv != first.Conv || old.Receiver.Kind != b.receiver.Kind || old.Receiver.AgentID != b.receiver.AgentID || old.Receiver.SessionHandle != b.receiver.SessionHandle || old.Receiver.Instructions != b.receiver.Instructions || old.Receiver.Mode != b.receiver.Mode || !sameOnClose(old.Receiver.OnClose, b.receiver.OnClose) {
			return errors.New("reply binding is canceled or differs from the selected local context")
		}
		if old.Conv == "" {
			if e = originalRecipients(tx, old, copies); e != nil {
				return e
			}
		}
	} else {
		id = protocol.NewID()
		preset, mode := b.receiver.Preset, b.receiver.Mode
		if b.receiver.OnClose != nil && b.remote == nil {
			preset, mode = b.handoff.Preset, b.receiver.OnClose.Mode
		}
		if stamp != nil && receiverPreset(stamp, mode) != preset {
			return errors.New("selected receiver preset changed before binding")
		}
		if _, err = tx.Exec(`INSERT INTO reply_receivers(id,conv,request_ref,receiver,executor,return_person,created_at) VALUES(?,?,?,?,?,nullif(?,''),?)`, id, first.Conv, ref, string(initialSelected), executor, b.person, time.Now().Unix()); err != nil {
			return err
		}
	}
	old, err := replyReceiverIn(tx, id)
	if err != nil {
		return err
	}
	if b.existing == "" {
		old.ID = ""
	}
	scopes, err := snapshotGroupReceiver(tx, first, copies, old)
	if err != nil {
		return err
	}
	selected, _ := encodeReceiverBinding(ReplyReceiverBinding{Receiver: b.receiver, handoff: b.handoff, group: scopes, remote: b.remote})
	if _, err = tx.Exec(`UPDATE reply_receivers SET receiver=? WHERE id=?`, string(selected), id); err != nil {
		return err
	}
	eligible := 0
	for _, c := range copies {
		in := c.in
		if in.Conv != first.Conv || first.Conv != "" && in.LID != ref || in.PID != first.PID || targetJSON(in.Target) != targetJSON(first.Target) {
			return errors.New("reply receiver copies do not share one original request")
		}
		if in.Replica || in.Target != nil && (in.To != in.Target.Address || c.recipientFP != in.Target.Fingerprint) {
			continue
		}
		if !protocol.ValidFingerprint(c.recipientFP) {
			return errors.New("reply receiver lacks original exact recipient key")
		}
		res, e := tx.Exec(`UPDATE outbox SET reply_receiver=?,recipient_fp=? WHERE id=?`, id, c.recipientFP, in.ID)
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("reply receiver request copy is missing")
		}
		eligible++
	}
	if eligible == 0 {
		return errors.New("reply receiver has no eligible original recipient copy")
	}
	return nil
}

// originalRecipients keeps a follow-up under an existing device binding
// (one outside a conversation, which would otherwise hold it to its members)
// with the devices the original request went to: the binding's own sent
// copies, or for an imported delegation the request it carries. Reusing a
// binding never sends the local user's delegated work to anyone else.
func originalRecipients(q dbq, old ReplyReceiverBinding, copies []outCopy) error {
	allowed := map[string]bool{}
	rows, err := q.Query(`SELECT DISTINCT recipient FROM outbox WHERE reply_receiver=?`, old.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var to string
		if err = rows.Scan(&to); err != nil {
			rows.Close()
			return err
		}
		allowed[targetAddress(to)] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if r := old.remote; r != nil && r.Role == "imported" {
		if r.Request.To != "" {
			allowed[targetAddress(r.Request.To)] = true
		}
		if r.Request.Target != nil {
			allowed[r.Request.Target.Address] = true
		}
	}
	for _, c := range copies {
		if !allowed[targetAddress(c.env.To)] {
			return fmt.Errorf("reply binding %s continues only with its original request's recipient, not %s", old.ID, c.env.To)
		}
	}
	return nil
}

// targetAddress is the agent address of ADDRESS or ADDRESS#SESSION.
func targetAddress(to string) string {
	if addr, _, err := protocol.SplitTarget(to); err == nil {
		return addr
	}
	return to
}

// bindReplyReceiverInput runs only after the existing verified direct/DM
// admission. It retains inbox content, attachments and independent Q/T grants;
// correlated clarification is data, not acceptance of a new remote task.
func bindReplyReceiverInput(tx *sql.Tx, in envelope.Inner, fp string) error {
	if in.ReplyTo == "" || in.Sub != "" || in.Replica || isResponderProgress(in) || !protocol.ValidFingerprint(fp) {
		return nil
	}
	rows, err := tx.Query(`SELECT o.reply_receiver,o.recipient,coalesce(o.recipient_fp,''),coalesce(o.pid,''),coalesce(o.target,''),coalesce(r.return_person,''),r.canceled_at FROM outbox o JOIN reply_receivers r ON r.id=o.reply_receiver WHERE (o.id=? OR o.lid=?) AND coalesce(o.conv,'')=?`, in.ReplyTo, in.ReplyTo, in.Conv)
	if err != nil {
		return err
	}
	type candidate struct {
		id, address, key, pid, target, person string
		canceled                              sql.NullInt64
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.address, &c.key, &c.pid, &c.target, &c.person, &c.canceled); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	imported, e := importedReceiverOriginals(tx, in)
	if e != nil {
		return e
	}
	for _, b := range imported {
		ok, e := importedReceiverMatches(tx, b, in, fp)
		if e != nil {
			return e
		}
		if ok {
			candidates = append(candidates, candidate{id: b.ID, address: in.From, key: fp, pid: in.PID, target: targetJSON(b.remote.Request.Target)})
		}
	}
	var binding string
	for _, c := range candidates {
		if binding != "" && binding != c.id {
			return nil
		} // ambiguous ref
		binding = c.id
	}
	if binding == "" {
		return nil
	}
	eligible := false
	for _, c := range candidates {
		if c.pid != in.PID {
			continue
		}
		if c.target != "" {
			var target envelope.Target
			if json.Unmarshal([]byte(c.target), &target) != nil || target.Address != in.From || target.Fingerprint != fp {
				continue
			}
			// Named author exists only on answer/result. A clarification without
			// that field proves the original host, never an invented agent author.
			if in.AgentID != "" {
				if in.AgentID != target.AgentID || in.Kind != envelope.KindAnswer && in.Kind != envelope.KindResult {
					continue
				}
			} // Empty author is a legitimate human takeover by this exact host.
		} else if in.AgentID != "" {
			continue
		}
		if c.person != "" {
			p, ok, e := scanPersonIn(tx, `person IN (SELECT person FROM person_devices WHERE address=?)`, in.From)
			if e != nil {
				return e
			}
			if !ok || p.roster.Person != c.person || !p.has(in.From, fp) || p.info.State != personPinned && p.info.State != personSelf {
				continue
			}
		} else if c.address != in.From || c.key != fp {
			continue
		}
		eligible = true
	}
	if !eligible {
		return nil
	}
	// The asking session has ended: its answer waits in this computer's
	// inbox, where the next session's hooks announce it (MEL-537), never
	// bound to a session that cannot take it. A question or task in reply
	// stays bound, as releaseEndedInputs keeps it: never the person's OK
	// item nor answered automatically.
	if in.Kind != envelope.KindQuestion && in.Kind != envelope.KindTask {
		if ended, e := endedLiveSession(tx, binding); e != nil {
			return e
		} else if ended {
			return nil
		}
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO reply_receiver_inputs(binding,inbox_id) VALUES(?,?)`, binding, in.ID)
	if err != nil {
		return err
	}
	b, e := replyReceiverIn(tx, binding)
	if e != nil {
		return e
	}
	if e = groupReceiverInput(tx, b, in.ID); e != nil {
		_, err = tx.Exec(`UPDATE inbox SET state=?,detail=? WHERE id=?`, stateNotRun, e.Error(), in.ID)
	}
	return err
}

// ReplyReceiverBinding is a local obligation. Accepted input and completed
// local work remain distinct from transport delivery and remote task execution.
type ReplyReceiverBinding struct {
	HandoffState string `json:"handoff_state,omitempty"`
	handoff      *closedHandoffState
	group        groupReceiverScopes
	remote       *receiverRemoteState
	ID           string               `json:"id"`
	Conv         string               `json:"conv,omitempty"`
	RequestRef   string               `json:"request_ref"`
	Receiver     ReplyReceiver        `json:"receiver"`
	Executor     *ExecutorStamp       `json:"executor,omitempty"`
	State        string               `json:"state"`
	Detail       string               `json:"detail,omitempty"`
	Inputs       []ReplyReceiverInput `json:"inputs,omitempty"`
}
type ReplyReceiverInput struct {
	ID     string `json:"id"` // existing verified inbox row; content/files stay there
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

func (a *Agent) ReplyReceiverBindings() ([]ReplyReceiverBinding, error) {
	rows, err := a.store.db.Query(`SELECT id,conv,request_ref,receiver,coalesce(executor,''),canceled_at FROM reply_receivers ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	var result []ReplyReceiverBinding
	for rows.Next() {
		var b ReplyReceiverBinding
		var receiver, executor string
		var canceled sql.NullInt64
		if err = rows.Scan(&b.ID, &b.Conv, &b.RequestRef, &receiver, &executor, &canceled); err != nil {
			rows.Close()
			return nil, err
		}
		if err = decodeReceiverBinding(receiver, &b); err != nil {
			rows.Close()
			return nil, err
		}
		if executor != "" {
			if err = json.Unmarshal([]byte(executor), &b.Executor); err != nil {
				rows.Close()
				return nil, err
			}
		}
		b.State = "pending"
		b.HandoffState = handoffView(b.handoff)
		if canceled.Valid {
			b.State = "canceled"
		}
		result = append(result, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		b := &result[i]
		if b.State != "canceled" {
			if e := groupReceiverBinding(a.store.db, *b); e != nil {
				b.State, b.Detail = "refused", e.Error()
			}
		}
		if b.State == "pending" && b.remote != nil && b.remote.Role == "origin" {
			b.Detail = "selected reply receiver belongs to linked host " + b.remote.Route.Host
			if !b.remote.Ready {
				b.Detail = "awaiting exact linked-host delegation approval; original request is not deliverable"
			}
			if b.remote.Refusal != "" {
				b.Detail = b.remote.Refusal + "; original remains unsent, no default chosen"
			}
		} else if b.State == "pending" && b.Receiver.Kind == "managed_agent" {
			if e := a.receiverSnapshot(a.store.db, *b); e != nil {
				b.State = "refused"
				b.Detail = e.Error()
			}
		} else if b.State == "pending" && b.Receiver.Kind == "live_session" {
			r, e := a.liveReplyBinding(a.store.db, *b)
			if e != nil {
				b.State = "refused"
				b.Detail = e.Error()
			} else if !r.Active {
				b.Detail = "selected native adapter is inactive; original binding retained"
				if b.handoff != nil && b.handoff.HeldReason != "" {
					b.State = "refused"
					b.Detail = b.handoff.HeldReason
				}
			}
		}
		inputs, e := a.store.db.Query(`SELECT x.inbox_id,x.state,CASE WHEN x.state='pending' AND x.live_claim IS NOT NULL THEN 'native delivery claimed; reconcile persisted token before any redispatch' ELSE coalesce(i.detail,'') END FROM reply_receiver_inputs x JOIN inbox i ON i.id=x.inbox_id WHERE x.binding=? ORDER BY i.arrival`, b.ID)
		if e != nil {
			return nil, e
		}
		for inputs.Next() {
			var in ReplyReceiverInput
			if e = inputs.Scan(&in.ID, &in.State, &in.Detail); e != nil {
				inputs.Close()
				return nil, e
			}
			b.Inputs = append(b.Inputs, in)
			if b.State == "pending" && in.State == "accepted" {
				b.State = "accepted"
			}
		}
		e = inputs.Err()
		inputs.Close()
		if e != nil {
			return nil, e
		}
		if b.State == "pending" && len(b.Inputs) > 0 {
			complete := true
			for _, in := range b.Inputs {
				if in.State != "completed" {
					complete = false
				}
			}
			if complete {
				var waiting int
				e = a.store.db.QueryRow(`SELECT count(*) FROM outbox o WHERE o.reply_receiver=? AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs x JOIN inbox i ON i.id=x.inbox_id WHERE x.binding=o.reply_receiver AND i.reply_to IN (o.id,coalesce(o.lid,o.id)))`, b.ID).Scan(&waiting)
				if e != nil {
					return nil, e
				}
				if waiting == 0 {
					b.State = "completed"
				} else {
					b.Detail = "awaiting a correlated reply to the selected follow-up"
				}
			}
		}
	}
	return result, nil
}
