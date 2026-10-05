package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A cursor describes seen metadata, not a native owner or an uncertain
// delivery. This registry and claim reuse the existing correlated input rows.
const replySessionSchema = `
CREATE TABLE reply_sessions(handle TEXT PRIMARY KEY, record TEXT NOT NULL);
ALTER TABLE reply_receiver_inputs ADD COLUMN live_claim TEXT;
`

// ReplySessionView is safe local picker metadata. Active means registered,
// not proof that a native process survived a crash. Paths/credentials stay private.
type ReplySessionView struct {
	Handle  string `json:"handle"`
	Harness string `json:"harness"`
	Label   string `json:"label"`
	Active  bool   `json:"active"`
}
type ReplySessionOwner struct {
	ReplySessionView
	Generation int64  `json:"generation"`
	OwnerToken string `json:"owner_token"`
}
type ReplySessionRegistration struct {
	Harness   string `json:"harness"`
	SessionID string `json:"session_id"`
	File      string `json:"file"`
	Leaf      string `json:"leaf,omitempty"`
	Handle    string `json:"handle,omitempty"` // restored nonsecret native marker
	Label     string `json:"label,omitempty"`
}
type replySessionRecord struct {
	Codex  *codexNativeRoute  `json:"codex,omitempty"`
	Claude *claudeNativeRoute `json:"claude,omitempty"`
	ReplySessionOwner
	SessionID       string `json:"session_id"`
	File            string `json:"file"`
	Anchor          string `json:"anchor,omitempty"`
	CloseReason     string `json:"close_reason,omitempty"`
	CloseGeneration int64  `json:"close_generation,omitempty"`
	// ChannelGeneration is the generation in which AgentNet's Claude channel
	// started draining this registration (claudeReplyChannelOwner): only
	// then is the session a receiver a question may name (nativeorigin.go).
	ChannelGeneration int64  `json:"channel_generation,omitempty"`
	Key               string `json:"key"`
	Realm             string `json:"realm"`
}
type ReplySessionCall struct {
	Handle      string `json:"handle"`
	Generation  int64  `json:"generation"`
	OwnerToken  string `json:"owner_token"`
	SessionID   string `json:"session_id"`
	File        string `json:"file"`
	Leaf        string `json:"leaf,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`
}
type liveInputClaim struct {
	ID         string `json:"id"`
	Token      string `json:"token"`
	Generation int64  `json:"generation"`
	// Offset is the native file's size when the input was first claimed:
	// its receipt can only be written after it, so receipts are read from
	// there (claudeNativeReceipt, codexNativeReceipt).
	Offset int64 `json:"offset,omitempty"`
}
type ReplyReceiverDelivery struct {
	BindingID     string  `json:"binding_id"`
	InputID       string  `json:"input_id"`
	ClaimID       string  `json:"claim_id"`
	InputToken    string  `json:"input_token"`
	Conv          string  `json:"conv,omitempty"`
	RequestRef    string  `json:"request_ref"`
	RequestBody   string  `json:"request_body"` // original locally authored request, not remote instructions
	ReconcileOnly bool    `json:"reconcile_only"`
	Message       Message `json:"message"`
}
type ReplyReceiverAck struct {
	ReplySessionCall
	BindingID  string `json:"binding_id"`
	InputID    string `json:"input_id"`
	ClaimID    string `json:"claim_id"`
	InputToken string `json:"input_token"`
}

// Only Claude's route-confined callers allow a lazy missing parent; all other
// native registration/owner callers retain the existing strict default.
func canonicalNativeFile(file string, allowMissingParent ...bool) (string, error) {
	if file == "" || !filepath.IsAbs(file) {
		return "", errors.New("native session requires an absolute file")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(file))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && len(allowMissingParent) == 1 && allowMissingParent[0] {
			return filepath.Clean(file), nil // caller must verify exact rooted Claude path
		}
		return "", err
	}
	file = filepath.Join(parent, filepath.Base(file))
	if real, e := filepath.EvalSymlinks(file); e == nil {
		return real, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	return file, nil
}

// nativeBranch reads metadata only into memory; no native text is copied into
// AgentNet. A lazy new native session may not have a file yet. The file is
// streamed, with no cap on its size or record count (nativescan.go): ids and
// parents are kept, and only the active branch's records are read again.
func nativeBranch(file, sid, leaf string, missingOK bool) (map[string]json.RawMessage, error) {
	type node struct {
		parent string
		offset int64
	}
	entries := map[string]node{}
	header := false
	err := nativeRecords(file, 0, func(rec nativeRecord) error {
		if rec.Oversize {
			return nil // never part of a branch this code can read
		}
		var entry struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Parent string `json:"parentId"`
		}
		if json.Unmarshal(rec.Line, &entry) != nil {
			return errors.New("native session metadata is invalid")
		}
		if entry.Type == "session" {
			if header || entry.ID != sid {
				return errors.New("native session header does not match registered identity")
			}
			header = true
			return nil
		}
		if entry.ID == "" {
			return nil
		}
		if _, exists := entries[entry.ID]; exists {
			return errors.New("native session has duplicate entry identity")
		}
		entries[entry.ID] = node{entry.Parent, rec.Offset}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !header {
		return nil, errors.New("native session header absent")
	}
	want := map[int64]string{}
	seen := map[string]bool{}
	for id := leaf; id != ""; {
		if seen[id] {
			return nil, errors.New("native branch is cyclic")
		}
		entry, ok := entries[id]
		if !ok {
			return nil, errors.New("native active branch is not yet persisted")
		}
		seen[id] = true
		want[entry.offset] = id
		id = entry.parent
	}
	branch := map[string]json.RawMessage{}
	if len(want) == 0 {
		return branch, nil
	}
	err = nativeRecords(file, 0, func(rec nativeRecord) error {
		id, ok := want[rec.Offset]
		if !ok || rec.Oversize {
			return nil
		}
		var entry struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(rec.Line, &entry) != nil || entry.ID != id {
			return errors.New("native session changed while it was read")
		}
		branch[id] = append(json.RawMessage(nil), rec.Line...)
		if len(branch) == len(want) {
			return errNativeStop
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(branch) != len(want) {
		return nil, errors.New("native session changed while it was read")
	}
	return branch, nil
}
func nativeHas(branch map[string]json.RawMessage, kind, field, value string) bool {
	return nativeHasFields(branch, kind, map[string]string{field: value})
}
func nativeHasFields(branch map[string]json.RawMessage, kind string, fields map[string]string) bool {
	for _, raw := range branch {
		var e struct {
			CustomType string         `json:"customType"`
			Details    map[string]any `json:"details"`
			Data       map[string]any `json:"data"`
			Message    *struct {
				CustomType string         `json:"customType"`
				Details    map[string]any `json:"details"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &e) != nil {
			continue
		}
		if e.Message != nil {
			e.CustomType, e.Details = e.Message.CustomType, e.Message.Details
		}
		if e.Details == nil {
			e.Details = e.Data
		}
		if e.CustomType != kind {
			continue
		}
		match := true
		for field, value := range fields {
			if e.Details[field] != value {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
func replySessionIn(q dbq, handle string) (replySessionRecord, error) {
	var r replySessionRecord
	var raw string
	err := q.QueryRow(`SELECT record FROM reply_sessions WHERE handle=?`, handle).Scan(&raw)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &r)
	}
	if err == nil && r.Handle != handle {
		err = errors.New("native receiver record does not match handle")
	}
	return r, err
}
func (a *Agent) checkReplySession(q dbq, r replySessionRecord) error {
	var realm string
	err := q.QueryRow(`SELECT v FROM config WHERE k='realm_id'`).Scan(&realm)
	if err != nil {
		return err
	}
	if r.Key != a.Self().Fingerprint() || r.Realm != realm {
		return errors.New("native receiver belongs to a different enrolled home or realm")
	}
	return nil
}
func saveReplySession(tx *sql.Tx, r replySessionRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO reply_sessions(handle,record) VALUES(?,?) ON CONFLICT(handle) DO UPDATE SET record=excluded.record`, r.Handle, string(raw))
	return err
}

// RegisterReplySession is trusted local lifecycle registration, not process
// attestation or permission to execute a remote task. Renewing fences old owners.
func (a *Agent) RegisterReplySession(in ReplySessionRegistration) (ReplySessionOwner, error) {
	if in.Harness != "pi" && in.Harness != "omp" || in.SessionID == "" || len(in.SessionID) > 128 || len(in.Label) > 160 {
		return ReplySessionOwner{}, errors.New("unsupported or invalid native receiver")
	}
	file, err := canonicalNativeFile(in.File)
	if err != nil {
		return ReplySessionOwner{}, err
	}
	branch, err := nativeBranch(file, in.SessionID, in.Leaf, in.Handle == "")
	if err != nil {
		return ReplySessionOwner{}, err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return ReplySessionOwner{}, err
	}
	defer tx.Rollback()
	var r replySessionRecord
	if in.Handle != "" {
		r, err = replySessionIn(tx, in.Handle)
		if err != nil {
			return ReplySessionOwner{}, err
		}
		if err = a.checkReplySession(tx, r); err != nil {
			return ReplySessionOwner{}, err
		}
		if r.Harness != in.Harness || r.SessionID != in.SessionID || r.File != file || !nativeHas(branch, "agentnet-receiver-session", "handle", r.Handle) {
			return ReplySessionOwner{}, errors.New("native resume marker is absent from exact registered branch")
		}
		if r.Anchor != "" {
			if _, ok := branch[r.Anchor]; !ok {
				return ReplySessionOwner{}, errors.New("native resume diverged from registered branch")
			}
		}
	} else {
		var realm string
		if err = tx.QueryRow(`SELECT v FROM config WHERE k='realm_id'`).Scan(&realm); err != nil {
			return ReplySessionOwner{}, err
		}
		r = replySessionRecord{SessionID: in.SessionID, File: file, Key: a.Self().Fingerprint(), Realm: realm}
		// Native session managers allocate in-memory leaves before lazy JSONL
		// creation. Only a verified durable ancestor can constrain later branches.
		if _, verified := branch[in.Leaf]; verified {
			r.Anchor = in.Leaf
		}
		r.Handle, r.Harness = protocol.NewID(), in.Harness
	}
	r.Label = in.Label
	if strings.TrimSpace(r.Label) == "" {
		r.Label = in.Harness + " " + in.SessionID
	}
	r.Generation++
	r.OwnerToken = protocol.NewID()
	r.Active = true
	r.CloseReason = ""
	r.CloseGeneration = 0
	if in.Handle != "" {
		// A resumed session is a new generation: what the previous one left
		// undelivered goes to this computer's inbox.
		if err = releaseEndedInputs(tx, r.Handle); err != nil {
			return ReplySessionOwner{}, err
		}
	}
	if err = saveReplySession(tx, r); err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ReplySessionOwner{}, err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return r.ReplySessionOwner, nil
}
func (a *Agent) replySessionOwner(q dbq, in ReplySessionCall) (replySessionRecord, error) {
	r, err := replySessionIn(q, in.Handle)
	if err != nil {
		return r, err
	}
	if err = a.checkReplySession(q, r); err != nil {
		return r, err
	}
	file, err := canonicalNativeFile(in.File, r.Harness == "claude" && r.Claude != nil)
	if err != nil {
		return r, err
	}
	if !r.Active || r.Generation != in.Generation || r.OwnerToken == "" || r.OwnerToken != in.OwnerToken || r.SessionID != in.SessionID || r.File != file {
		return r, errors.New("native receiver owner or session generation changed")
	}
	if r.Harness == "codex" {
		if r.Codex == nil {
			return r, errors.New("native Codex route unavailable")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = r.Codex.verify(ctx, false)
		cancel()
		if err != nil {
			return r, err
		}
	} else if r.Harness == "claude" {
		if r.Claude == nil {
			return r, errors.New("native Claude route unavailable")
		}
		if err = r.Claude.verify(false); err != nil {
			return r, err
		}
		if err = r.Claude.checkFile(r.File, r.SessionID); err != nil {
			return r, err
		}
	}
	return r, nil
}
func (a *Agent) CloseReplySession(in ReplySessionCall) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := a.replySessionOwner(tx, in)
	if err != nil {
		return err
	}
	r.Active = false
	r.Generation++
	r.OwnerToken = ""
	r.CloseReason = "detached"
	if in.CloseReason == "shutdown" {
		r.CloseReason = "shutdown"
	}
	r.CloseGeneration = r.Generation
	if err = releaseEndedInputs(tx, r.Handle); err != nil {
		return err
	}
	if err = saveReplySession(tx, r); err == nil {
		err = tx.Commit()
	}
	if err == nil {
		a.store.changed()
		notifyDaemon(a.home)
		// Closure is durable first: one refused/failed binding cannot leave native
		// ownership active. A later daemon wake retries only this exact close proof.
		if e := a.reconcileClosedReplyReceiverJobs(); e != nil {
			a.Logf("closed receiver handoff deferred: %v", e)
		}
	}
	return err
}
func (a *Agent) ReplySessions() ([]ReplySessionView, error) {
	rows, err := a.store.db.Query(`SELECT record FROM reply_sessions ORDER BY handle`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReplySessionView
	for rows.Next() {
		var raw string
		var r replySessionRecord
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		out = append(out, r.ReplySessionView)
	}
	return out, rows.Err()
}
func (a *Agent) CurrentReplySession(handle, home string, generation int64) (*ReplyReceiver, error) {
	canonical, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	own, err := filepath.EvalSymlinks(a.home)
	if err != nil {
		return nil, err
	}
	if canonical != own {
		return nil, errors.New("inherited native receiver is for another AgentNet home")
	}
	r, err := replySessionIn(a.store.db, handle)
	if err != nil {
		return nil, err
	}
	if err = a.checkReplySession(a.store.db, r); err != nil {
		return nil, err
	}
	if !r.Active || r.Generation != generation {
		return nil, errors.New("inherited native receiver is inactive or stale")
	}
	return &ReplyReceiver{Kind: "live_session", SessionHandle: handle}, nil
}
func (a *Agent) liveReplyBinding(q dbq, b ReplyReceiverBinding) (replySessionRecord, error) {
	if b.remote != nil && b.remote.Role == "origin" {
		return replySessionRecord{}, errors.New("selected native receiver belongs to another linked host")
	}
	if b.State == "canceled" {
		return replySessionRecord{}, errors.New("selected reply binding canceled")
	}
	r, err := replySessionIn(q, b.Receiver.SessionHandle)
	if err != nil {
		return r, err
	}
	return r, a.checkReplySession(q, r)
}
func liveInputKey(q dbq, id string) error {
	var sender, key string
	var pending sql.NullString
	var local, replica bool
	err := q.QueryRow(`SELECT sender,coalesce(verified_by,''),local,replica FROM inbox WHERE id=?`, id).Scan(&sender, &key, &local, &replica)
	if err != nil {
		return err
	}
	pub, ok, err := pinnedKey(q, sender)
	if err != nil {
		return err
	}
	if err = q.QueryRow(`SELECT pending FROM peers WHERE address=?`, sender).Scan(&pending); err != nil {
		return err
	}
	if local || replica || !ok || pub.Fingerprint() != key || pending.Valid {
		return errors.New("selected input signing key is no longer trusted")
	}
	return nil
}
func (a *Agent) TakeReplyReceiverInput(in ReplySessionCall) (*ReplyReceiverDelivery, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := a.replySessionOwner(tx, in)
	if err != nil {
		return nil, err
	}
	// Owner, route and path are checked above; the whole Claude or Codex
	// file was validated at registration, so a take never rereads it. Its
	// receipt is read from the size it has now (liveInputClaim.Offset).
	if r.Codex == nil && r.Claude == nil && r.Anchor != "" {
		branch, e := nativeBranch(r.File, r.SessionID, in.Leaf, false)
		if e != nil {
			return nil, e
		}
		if _, ok := branch[r.Anchor]; !ok {
			return nil, errors.New("native take diverged from registered branch")
		}
	}
	var d ReplyReceiverDelivery
	var raw string
	err = tx.QueryRow(`SELECT x.binding,x.inbox_id,coalesce(x.live_claim,'') FROM reply_receiver_inputs x JOIN reply_receivers b ON b.id=x.binding JOIN inbox i ON i.id=x.inbox_id WHERE x.state='pending' AND b.canceled_at IS NULL AND json_extract(b.receiver,'$.kind')='live_session' AND json_extract(b.receiver,'$.session_handle')=? ORDER BY i.arrival LIMIT 1`, r.Handle).Scan(&d.BindingID, &d.InputID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b, err := replyReceiverIn(tx, d.BindingID)
	if err != nil {
		return nil, err
	}
	if _, err = a.liveReplyBinding(tx, b); err != nil {
		return nil, err
	}
	if err = groupReceiverInput(tx, b, d.InputID); err != nil {
		return nil, err
	}
	if err = liveInputKey(tx, d.InputID); err != nil {
		return nil, err
	}
	claim := liveInputClaim{ID: protocol.NewID(), Token: protocol.NewID(), Generation: r.Generation}
	d.ReconcileOnly = raw != ""
	if raw != "" {
		claim = liveInputClaim{} // the first claim's own values, its offset included
		if err = json.Unmarshal([]byte(raw), &claim); err != nil {
			return nil, err
		}
		claim.Generation = r.Generation
	} else if r.Codex != nil || r.Claude != nil {
		if info, e := os.Stat(r.File); e == nil {
			claim.Offset = info.Size()
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
	data, _ := json.Marshal(claim)
	if string(data) != raw {
		if _, err = tx.Exec(`UPDATE reply_receiver_inputs SET live_claim=? WHERE binding=? AND inbox_id=? AND state='pending'`, string(data), d.BindingID, d.InputID); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	d.ClaimID, d.InputToken, d.Conv, d.RequestRef = claim.ID, claim.Token, b.Conv, b.RequestRef
	if d.RequestBody, err = receiverOriginalBody(a.store.db, b); err != nil {
		return nil, err
	}
	msg, err := a.store.inboxMessage(d.InputID)
	if err != nil {
		return nil, err
	}
	if msg == nil {
		return nil, errors.New("selected native input unavailable")
	}
	d.Message = *msg
	return &d, nil
}

// AckReplyReceiverInput records native durable acceptance, never completion of
// tools or effects. Both active branch and physical JSONL must contain the token.
func (a *Agent) AckReplyReceiverInput(in ReplyReceiverAck) (bool, error) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	r, err := a.replySessionOwner(tx, in.ReplySessionCall)
	if err != nil {
		return false, err
	}
	b, err := replyReceiverIn(tx, in.BindingID)
	if err != nil {
		return false, err
	}
	if b.Receiver.Kind != "live_session" || b.Receiver.SessionHandle != r.Handle {
		return false, errors.New("input belongs to another native receiver")
	}
	if _, err = a.liveReplyBinding(tx, b); err != nil {
		return false, err
	}
	if err = groupReceiverInput(tx, b, in.InputID); err != nil {
		return false, err
	}
	if err = liveInputKey(tx, in.InputID); err != nil {
		return false, err
	}
	var raw, state string
	if err = tx.QueryRow(`SELECT live_claim,state FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, in.BindingID, in.InputID).Scan(&raw, &state); err != nil {
		return false, err
	}
	var claim liveInputClaim
	if err = json.Unmarshal([]byte(raw), &claim); err != nil {
		return false, err
	}
	if claim.ID != in.ClaimID || claim.Token != in.InputToken || claim.Generation != r.Generation {
		return false, errors.New("native input claim or owner changed")
	}
	var branch map[string]json.RawMessage
	if r.Codex != nil {
		var body string
		if body, err = receiverOriginalBody(tx, b); err != nil {
			return false, err
		}
		var msg Message
		if err = tx.QueryRow(`SELECT sender,kind,body FROM inbox WHERE id=?`, in.InputID).Scan(&msg.From, &msg.Kind, &msg.Body); err != nil {
			return false, err
		}
		accepted, e := codexNativeReceipt(r.File, r.SessionID, codexInputText(&ReplyReceiverDelivery{BindingID: in.BindingID, InputID: in.InputID, ClaimID: claim.ID, InputToken: claim.Token, RequestBody: body, Message: msg}), claim.Offset)
		if e != nil || !accepted {
			return false, e
		}
	} else if r.Claude != nil {
		accepted, e := claudeNativeReceipt(r.File, r.SessionID, r.Claude.Source, in, claim.Offset)
		if e != nil || !accepted {
			return false, e
		}
	} else {
		branch, err = nativeBranch(r.File, r.SessionID, in.Leaf, false)
		if err != nil {
			return false, err
		}
		if !nativeHasFields(branch, "agentnet-receiver", map[string]string{"input_token": claim.Token, "binding_id": in.BindingID, "input_id": in.InputID, "claim_id": claim.ID}) {
			return false, nil
		}
		if r.Anchor != "" {
			if _, ok := branch[r.Anchor]; !ok {
				return false, errors.New("native input is on a different registered branch")
			}
		}
	}
	if state == "accepted" {
		return true, nil
	}
	if state != "pending" {
		return false, errors.New("native input is no longer pending")
	}
	res, err := tx.Exec(`UPDATE reply_receiver_inputs SET state='accepted',accepted_at=? WHERE binding=? AND inbox_id=? AND state='pending' AND live_claim=?`, time.Now().UnixNano(), in.BindingID, in.InputID, raw)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, fmt.Errorf("native input claim lost")
	}
	// Keep the accepted native branch as the resume fence. A later tree switch
	// cannot reconcile this binding merely by inheriting an older marker.
	r.Anchor = in.Leaf
	if err = saveReplySession(tx, r); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	a.store.changed()
	notifyDaemon(a.home)
	return true, nil
}

// releaseEndedInputs hands what a native session left undelivered to this
// computer's inbox when its registration ends or starts a new generation
// (MEL-537): its bound inputs still pending with no live claim leave their
// binding and get a fresh arrival number, so the next session's hooks
// announce them as ordinary arrivals (attention.go). They never enter
// review. Claimed or accepted inputs stay bound and are never redelivered
// silently (agentnet receivers lists them), and a binding with an explicit
// closed-session handoff (--on-close-agent) keeps its inputs for it.
func releaseEndedInputs(tx *sql.Tx, handle string) error {
	rows, err := tx.Query(`SELECT x.binding, x.inbox_id FROM reply_receiver_inputs x JOIN reply_receivers b ON b.id=x.binding JOIN inbox i ON i.id=x.inbox_id
		WHERE x.state='pending' AND x.live_claim IS NULL AND json_extract(b.receiver,'$.kind')='live_session' AND json_extract(b.receiver,'$.session_handle')=?
		  AND coalesce(json_type(b.receiver,'$.on_close'),'')!='object' AND coalesce(json_extract(b.receiver,'$.remote.role'),'')!='origin'
		ORDER BY i.arrival`, handle)
	if err != nil {
		return err
	}
	type input struct{ binding, id string }
	var inputs []input
	for rows.Next() {
		var in input
		if err = rows.Scan(&in.binding, &in.id); err != nil {
			rows.Close()
			return err
		}
		inputs = append(inputs, in)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, in := range inputs {
		if _, err = tx.Exec(`DELETE FROM reply_receiver_inputs WHERE binding=? AND inbox_id=? AND state='pending' AND live_claim IS NULL`, in.binding, in.id); err != nil {
			return err
		}
		if err = renumberArrival(tx, in.id); err != nil {
			return err
		}
	}
	return nil
}

// renumberArrival gives inbox row id the next arrival number, from the
// counter the inbox_arrival trigger uses (store.go), so every session's
// attention cursor sees it as new.
func renumberArrival(tx *sql.Tx, id string) error {
	if _, err := tx.Exec(`UPDATE config SET v = CAST(v AS INTEGER) + 1 WHERE k = 'arrival'`); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE inbox SET arrival = (SELECT CAST(v AS INTEGER) FROM config WHERE k = 'arrival') WHERE id = ?`, id)
	return err
}

// endedLiveSession reports whether binding id selects a native session of
// this home whose registration has ended, with no closed-session handoff:
// an input arriving for it now goes to the plain inbox instead
// (bindReplyReceiverInput), as releaseEndedInputs sends earlier ones.
func endedLiveSession(q dbq, id string) (bool, error) {
	var receiver string
	if err := q.QueryRow(`SELECT receiver FROM reply_receivers WHERE id=?`, id).Scan(&receiver); err != nil {
		return false, err
	}
	var b struct {
		Kind    string          `json:"kind"`
		Handle  string          `json:"session_handle"`
		OnClose json.RawMessage `json:"on_close"`
		Remote  *struct {
			Role string `json:"role"`
		} `json:"remote"`
	}
	if err := json.Unmarshal([]byte(receiver), &b); err != nil {
		return false, err
	}
	if b.Kind != "live_session" || len(b.OnClose) > 0 && string(b.OnClose) != "null" || b.Remote != nil && b.Remote.Role == "origin" {
		return false, nil
	}
	r, err := replySessionIn(q, b.Handle)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !r.Active, nil
}
