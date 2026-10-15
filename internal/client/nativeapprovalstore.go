package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const nativeApprovalSchema = `
CREATE TABLE native_requests(
 token TEXT PRIMARY KEY,
 request TEXT NOT NULL REFERENCES inbox(id) ON DELETE CASCADE,
 attempt INTEGER NOT NULL,
 deadline INTEGER NOT NULL,
 rpc TEXT NOT NULL,
 method TEXT NOT NULL,
 params TEXT NOT NULL,
 job TEXT NOT NULL,
 view TEXT NOT NULL,
 state TEXT NOT NULL,
 response TEXT NOT NULL DEFAULT '',
 response_hash TEXT NOT NULL DEFAULT '',
 decision TEXT NOT NULL DEFAULT '',
 actor TEXT NOT NULL DEFAULT '',
 actor_key TEXT NOT NULL DEFAULT '',
 UNIQUE(request,attempt,rpc));
CREATE TRIGGER end_native_requests AFTER UPDATE OF state,body ON inbox
 WHEN NEW.state!='running' OR NEW.body='' BEGIN
 UPDATE native_requests SET state='ended',view='',params='',response=''
 WHERE request=NEW.id AND state!='ended'; END;
`

// Narrower than an operator/steward grant: current owner HUMAN, exact actual
// pinned roster key, no pending replacement, freeze or authenticated revoke.
func (a *Agent) nativeOwnerHumanIn(q dbq, address, key string) (bool, error) {
	if ok, e := a.ownHumanAuthor(q, address, key); e != nil || !ok {
		return false, e
	}
	revoked, e := revokedDeviceKnown(q, address, key)
	return !revoked, e
}

func (a *Agent) nativeJobCurrentIn(q dbq, j job, attempt int64) bool {
	var current bool
	if q.QueryRow(`SELECT state=? AND attempts=? AND replica=0 AND receiver_route IS NULL
 AND kind IN ('question','task') AND verified_by=? AND sender=? AND coalesce(conv,'')=? AND coalesce(pid,'')=?
 AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id)
 FROM inbox WHERE id=?`, stateRunning, attempt, j.Key, j.From, j.Conv, j.PID, j.ID).Scan(&current) != nil || !current {
		return false
	}
	if j.Target != nil && !a.nativeFollowupExecutor(q, j) {
		return false
	}
	if j.Target != nil && (j.Target.Address != a.Address || j.Target.Fingerprint != a.Self().Fingerprint()) {
		return false
	}
	if revoked, e := revokedDeviceKnown(q, a.Address, a.Self().Fingerprint()); e != nil || revoked {
		return false
	}
	if revoked, e := revokedDeviceKnown(q, j.From, j.Key); e != nil || revoked {
		return false
	}
	if j.From != a.Address || j.Key != a.Self().Fingerprint() {
		key, found, e := pinnedKey(q, j.From)
		var pending sql.NullString
		if e != nil || !found || key.Fingerprint() != j.Key || q.QueryRow(`SELECT pending FROM peers WHERE address=?`, j.From).Scan(&pending) != nil || pending.Valid {
			return false
		}
	}
	if j.PID != "" {
		v, _, e := agentVerdict(q, j.agentReq(stateRunning), a.Address, a.Self().Fingerprint(), true, map[string]*partView{})
		if e != nil || v != verdictRun {
			return false
		}
	}
	if j.PermissionPerson != "" {
		person, e := permissionPersonIn(q, j.From, j.Key)
		if e != nil || person != j.PermissionPerson {
			return false
		}
	}
	if j.OwnHumanRequest {
		ok, e := a.nativeOwnerHumanIn(q, j.From, j.Key)
		if e != nil || !ok {
			return false
		}
	}
	return true
}

func (a *Agent) nativeRequestChanged(request string) {
	a.noteNativeWait(request)
	// Keep recipient coverage so ending sends a settled snapshot, including
	// after restart. A fresh token needs a fresh snapshot of this same item.
	_, _ = a.store.db.Exec(`UPDATE reported SET sent_at=0 WHERE item=?`, request)
	a.store.changed()
	a.wakeWorker()
}

// Called only by the owned bridge at the real callback. Wait on existing local
// changes, not the relay stream. The existing worker deadline remains the limit.
func (a *Agent) awaitNativeRequest(ctx context.Context, j job, c nativeCallback) (json.RawMessage, error) {
	deadline, ok := ctx.Deadline()
	var attempt int64
	if a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=? AND state=?`, j.ID, stateRunning).Scan(&attempt) != nil || !a.nativeJobCurrentIn(a.store.db, j, attempt) {
		return nil, errors.New("native run is no longer admitted")
	}
	c.View.Token = protocol.NewID()
	c.View.Expires = 0
	if ok {
		c.View.Expires = deadline.UnixMilli()
	}
	c.View.Host = a.Address
	c.View.Request = j.ID
	c.View.Key = j.Key
	c.View.Attempt = attempt
	if j.Target != nil {
		c.View.AgentID = j.Target.AgentID
	}
	view, _ := json.Marshal(c.View)
	// Capture only existing authority fields, never another copy of the body/files.
	bound := job{ID: j.ID, From: j.From, Key: j.Key, Kind: j.Kind, Conv: j.Conv, PID: j.PID, Local: j.Local, Target: j.Target, PermissionPerson: j.PermissionPerson, OwnHumanRequest: j.OwnHumanRequest}
	boundJSON, _ := json.Marshal(bound)
	if _, e := a.store.db.Exec(`INSERT INTO native_requests(token,request,attempt,deadline,rpc,method,params,job,view,state) VALUES(?,?,?,?,?,?,?,?,?, 'pending')`, c.View.Token, j.ID, attempt, c.View.Expires, string(c.ID), c.View.Method, string(c.Params), string(boundJSON), string(view)); e != nil {
		return nil, e
	}
	_, _ = a.store.db.Exec(`UPDATE inbox SET notified=0 WHERE id=?`, j.ID)
	a.nativeRequestChanged(j.ID)
	ready := false
	defer func() {
		if !ready {
			a.endNativeCallback(j, c)
		}
	}()
	for {
		_, changed := a.Changed()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !a.nativeJobCurrentIn(a.store.db, j, attempt) {
			return nil, errors.New("native run authority ended")
		}
		var state, text, actor, key string
		if e := a.store.db.QueryRow(`SELECT state,response,actor,actor_key FROM native_requests WHERE token=?`, c.View.Token).Scan(&state, &text, &actor, &key); e != nil {
			return nil, e
		}
		if state == "answered" {
			r, e := parseNativeResponse(text)
			if e != nil {
				return nil, e
			}
			// Recheck the same actor immediately before handing anything to Codex.
			if actor != "local-owner" {
				valid, e := a.nativeOwnerHumanIn(a.store.db, actor, key)
				if e != nil || !valid {
					return nil, errors.New("native owner device authority ended")
				}
			}
			result, e := nativeCallbackResult(c, r)
			if e != nil {
				return nil, e
			}
			if result == nil { // input has no native cancel response: stop this worker
				_, e = a.store.db.Exec(`UPDATE inbox SET state=? WHERE id=? AND state=? AND attempts=?`, stateCancelReq, j.ID, stateRunning, attempt)
				a.store.changed()
				a.wakeWorker()
				return nil, errors.New("native input cancelled by owner")
			}
			res, e := a.store.db.Exec(`UPDATE native_requests SET state='handed',response='' WHERE token=? AND state='answered'`, c.View.Token)
			if e != nil {
				return nil, e
			}
			if n, _ := res.RowsAffected(); n != 1 {
				return nil, errors.New("native response already consumed")
			}
			ready = true
			return result, nil
		}
		if state != "pending" {
			return nil, errors.New("native callback ended")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (a *Agent) endNativeCallback(j job, c nativeCallback) {
	_, _ = a.store.db.Exec(`UPDATE native_requests SET state='ended',view='',params='',response='' WHERE request=? AND rpc=?`, j.ID, string(c.ID))
	a.nativeRequestChanged(j.ID)
}

func (a *Agent) nativeHandoverCurrent(j job, c nativeCallback) bool {
	var attempt int64
	var actor, key string
	if a.store.db.QueryRow(`SELECT attempt,actor,actor_key FROM native_requests WHERE request=? AND rpc=? AND method=? AND state='handed' AND (deadline=0 OR deadline>?)`, j.ID, string(c.ID), c.View.Method, time.Now().UnixMilli()).Scan(&attempt, &actor, &key) != nil || !a.nativeJobCurrentIn(a.store.db, j, attempt) {
		return false
	}
	if actor == "local-owner" {
		return true
	}
	valid, e := a.nativeOwnerHumanIn(a.store.db, actor, key)
	return e == nil && valid
}

func (a *Agent) nativeRequestFor(request string) *NativeRequest {
	var body string
	if a.store.db.QueryRow(`SELECT n.view FROM native_requests n JOIN inbox i ON i.id=n.request WHERE n.request=? AND n.state='pending' AND (n.deadline=0 OR n.deadline>?) AND i.state='running' AND i.attempts=n.attempt ORDER BY n.rowid DESC LIMIT 1`, request, time.Now().UnixMilli()).Scan(&body) != nil {
		return nil
	}
	var v NativeRequest
	if json.Unmarshal([]byte(body), &v) != nil {
		return nil
	}
	return &v
}

// Local native cards use the same report DTO and decision validator, scoped to
// the authenticated local owner page. They are never sent as conversation work.
func (a *Agent) LocalNativeReport() Report {
	r := Report{V: 2, At: time.Now().Unix(), Host: a.Address, Items: []ReportItem{}}
	rows, e := a.store.db.Query(`SELECT request FROM native_requests WHERE state='pending' AND (deadline=0 OR deadline>?) ORDER BY rowid`, time.Now().UnixMilli())
	if e != nil {
		return r
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if n := a.nativeRequestFor(id); n != nil {
			r.Items = append(r.Items, ReportItem{ID: id, Key: n.Key, Kind: "question", State: stateRunning, Blocker: BlockerNeedsHuman, Attempt: n.Attempt, Native: n})
		}
	}
	return r
}

func (a *Agent) claimNativeResponse(tx *sql.Tx, id, key string, attempt int64, text, decision, actor, actorKey, report string, local bool) error {
	r, e := parseNativeResponse(text)
	if e != nil || !protocol.ValidID(r.Token) || !protocol.ValidID(decision) {
		return errors.New("choose the exact current native response")
	}
	if local {
		actor = "local-owner"
		actorKey = ""
	} else {
		valid, err := a.nativeOwnerHumanIn(tx, actor, actorKey)
		if err != nil || !valid {
			return errors.New("only a current exact owner-human device can answer this native request")
		}
	}
	var request, params, view, rpc, method, state, priorHash, priorDecision, bound, priorActor, priorKey string
	var actualAttempt, deadline int64
	e = tx.QueryRow(`SELECT request,attempt,deadline,rpc,method,params,view,state,response_hash,decision,job,actor,actor_key FROM native_requests WHERE token=?`, r.Token).Scan(&request, &actualAttempt, &deadline, &rpc, &method, &params, &view, &state, &priorHash, &priorDecision, &bound, &priorActor, &priorKey)
	if e != nil || request != id || actualAttempt != attempt {
		return errors.New("the exact native callback has ended or expired")
	}
	digest := sha256.Sum256([]byte(text))
	responseHash := hex.EncodeToString(digest[:])
	// Receipt recovery uses only a digest of the exact claimed response. It
	// neither retains secret input nor reconstructs an ended native callback.
	if priorHash == responseHash && priorDecision == decision && priorActor == actor && priorKey == actorKey {
		var original job
		if json.Unmarshal([]byte(bound), &original) != nil || original.Key != key {
			return errors.New("native request binding differs")
		}
		return nil
	}
	if deadline != 0 && deadline <= time.Now().UnixMilli() {
		return errors.New("the exact native callback has ended or expired")
	}
	if state != "pending" {
		return errors.New("this native callback was already answered or ended")
	}
	var n NativeRequest
	if json.Unmarshal([]byte(view), &n) != nil || n.Key != key || n.Host != a.Address || n.Request != id || n.Attempt != attempt {
		return errors.New("native request binding differs")
	}
	if !local {
		valid, e := a.nativeOwnerHumanIn(tx, actor, actorKey)
		if e != nil || !valid {
			return errors.New("only a current exact owner-human device can answer this native request")
		}
		var body string
		if tx.QueryRow(`SELECT body FROM outbox WHERE id=? AND recipient=? AND recipient_fp=? AND status=? AND conv IS NULL`, report, actor, actorKey, envelope.StatusReviewNotice).Scan(&body) != nil {
			return errors.New("native response does not name this host's private report")
		}
		rp, ok := ParseReport(body)
		matched := false
		if ok {
			for _, it := range rp.Items {
				if it.ID == id && it.Key == key && it.State == stateRunning && it.Attempt == attempt && it.Native != nil && it.Native.Token == r.Token {
					matched = true
				}
			}
		}
		if !matched {
			return errors.New("private report does not name this exact native callback")
		}
	}
	var j job
	if json.Unmarshal([]byte(bound), &j) != nil || j.ID != id {
		return errors.New("native request binding is invalid")
	}
	if !a.nativeJobCurrentIn(tx, j, attempt) {
		return errors.New("native request is no longer running with current authority")
	}
	if _, e = nativeCallbackResult(nativeCallback{ID: json.RawMessage(rpc), Params: json.RawMessage(params), View: n}, r); e != nil {
		return e
	}
	res, e := tx.Exec(`UPDATE native_requests SET state='answered',response=?,response_hash=?,decision=?,actor=?,actor_key=? WHERE token=? AND state='pending'`, text, responseHash, decision, actor, actorKey, r.Token)
	if e != nil {
		return e
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return errors.New("native response already claimed")
	}
	return nil
}

func (a *Agent) RespondNativeRequest(id, key string, attempt int64, text, sendID string) error {
	tx, e := a.store.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = a.claimNativeResponse(tx, id, key, attempt, text, sendID, "", "", "", true); e != nil {
		return e
	}
	if e = a.store.done(tx.Commit()); e != nil {
		return e
	}
	a.nativeRequestChanged(id)
	notifyDaemon(a.home)
	return nil
}

// The domain-tagged running response never enters ContinueRequest, even when
// malformed. A received operator grant is deliberately irrelevant here.
func (a *Agent) admitNativeDecision(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, d envelope.Decision) error {
	if in.Ref == nil {
		return errors.New("native response must name its exact request")
	}
	var prior sql.NullString
	var author, authorKey, body, refID, refKey string
	if a.store.db.QueryRow(`SELECT detail,sender,verified_by,body,ref_id,ref_fp FROM inbox WHERE id=?`, in.ID).Scan(&prior, &author, &authorKey, &body, &refID, &refKey) == nil {
		if author != env.From || authorKey != sender.Fingerprint() || body != in.Body || refID != in.Ref.ID || refKey != in.Ref.Fingerprint {
			return errors.New("native response retry differs from its stored decision")
		}
		a.answerDecision(ctx, env.From, in, d, stateRunning, prior.String)
		return nil
	}
	tx, e := a.store.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	why := ""
	if d.Action != "continue" || d.Expect != stateRunning || in.Ref == nil {
		why = "choose the exact current native callback"
	} else if e = a.claimNativeResponse(tx, in.Ref.ID, in.Ref.Fingerprint, d.Attempt, d.Text, in.ID, env.From, sender.Fingerprint(), d.Report, false); e != nil {
		why = e.Error()
	}
	_, e = tx.Exec(`INSERT OR IGNORE INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,sub,received_ms,ref_id,ref_fp,read_at,detail) VALUES(?,?,?,?,?,?,'',?,?,?,?,?,?,nullif(?,''))`, in.ID, in.From, in.TS, in.Kind, in.Body, time.Now().Unix(), sender.Fingerprint(), in.Sub, time.Now().UnixMilli(), in.Ref.ID, in.Ref.Fingerprint, time.Now().Unix(), why)
	if e != nil {
		return e
	}
	if e = a.store.done(tx.Commit()); e != nil {
		return e
	}
	state := ""
	if why == "" {
		state = stateRunning
		a.nativeRequestChanged(in.Ref.ID)
	}
	a.answerDecision(ctx, env.From, in, d, state, why)
	return nil
}

func nativeDecision(d envelope.Decision) bool {
	return d.Action == "continue" && (d.Expect == stateRunning || strings.HasPrefix(d.Text, nativeResponseDomain))
}
