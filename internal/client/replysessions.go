package client

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	Key             string `json:"key"`
	Realm           string `json:"realm"`
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
// AgentNet. Bounds fail closed. A lazy new native session may not have a file yet.
func nativeBranch(file, sid, leaf string, missingOK bool) (map[string]json.RawMessage, error) {
	f, err := os.Open(file)
	if errors.Is(err, os.ErrNotExist) && missingOK {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries := map[string]struct {
		Parent string
		Raw    json.RawMessage
	}{}
	scan := bufio.NewScanner(io.LimitReader(f, (32<<20)+1))
	scan.Buffer(make([]byte, 4096), 2<<20)
	total, count := 0, 0
	header := false
	for scan.Scan() {
		line := scan.Bytes()
		total += len(line) + 1
		count++
		if total > 32<<20 || count > 100000 {
			return nil, errors.New("native session metadata exceeds bound")
		}
		var entry struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			Parent string `json:"parentId"`
		}
		if err = json.Unmarshal(line, &entry); err != nil {
			return nil, errors.New("native session metadata is invalid")
		}
		if entry.Type == "session" {
			if header || entry.ID != sid {
				return nil, errors.New("native session header does not match registered identity")
			}
			header = true
			continue
		}
		if entry.ID == "" {
			continue
		}
		if _, exists := entries[entry.ID]; exists {
			return nil, errors.New("native session has duplicate entry identity")
		}
		entries[entry.ID] = struct {
			Parent string
			Raw    json.RawMessage
		}{entry.Parent, append(json.RawMessage(nil), line...)}
	}
	if err = scan.Err(); err != nil {
		return nil, err
	}
	if !header {
		return nil, errors.New("native session header absent")
	}
	branch := map[string]json.RawMessage{}
	for id := leaf; id != ""; {
		if _, cycle := branch[id]; cycle {
			return nil, errors.New("native branch is cyclic")
		}
		entry, ok := entries[id]
		if !ok {
			return nil, errors.New("native active branch is not yet persisted")
		}
		branch[id] = entry.Raw
		id = entry.Parent
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
	if r.Codex != nil {
		if _, e := codexNativeEntries(r.File, r.SessionID); e != nil {
			return nil, e
		}
	} else if r.Claude != nil {
		if _, e := claudeNativeScan(r.File, r.SessionID, true, nil); e != nil {
			return nil, e
		}
	} else if r.Anchor != "" {
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
		if err = json.Unmarshal([]byte(raw), &claim); err != nil {
			return nil, err
		}
		claim.Generation = r.Generation
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
		accepted, e := codexNativeReceipt(r.File, r.SessionID, codexInputText(&ReplyReceiverDelivery{BindingID: in.BindingID, InputID: in.InputID, ClaimID: claim.ID, InputToken: claim.Token, RequestBody: body, Message: msg}))
		if e != nil || !accepted {
			return false, e
		}
	} else if r.Claude != nil {
		accepted, e := claudeNativeReceipt(r.File, r.SessionID, r.Claude.Source, in)
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
