package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) storedReceiverSetup(id string) (envelope.Inner, string, error) {
	var in envelope.Inner
	var fp, raw string
	err := a.store.db.QueryRow(`SELECT id,sender,ts,kind,body,coalesce(reply_to,''),verified_by,receiver_route FROM inbox WHERE id=? AND conv IS NULL AND receiver_route IS NOT NULL`, id).Scan(&in.ID, &in.From, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &fp, &raw)
	if err != nil {
		return in, fp, err
	}
	in.V = envelope.Version
	in.To = a.Address
	if err = json.Unmarshal([]byte(raw), &in.ReceiverRoute); err != nil {
		return in, fp, err
	}
	files, err := a.store.attachments(id)
	if err != nil {
		return in, fp, err
	}
	for _, f := range files {
		in.Attachments = append(in.Attachments, envelope.Attachment{Name: f.Name, Size: f.Size, SHA256: f.SHA256, Blob: envelope.Blob{ID: f.BlobID, Size: f.ctSize, SHA256: f.ctSHA256}})
	}
	return in, fp, envelope.ValidateReceiverRoute(in)
}
func (a *Agent) verifyReceiverOriginal(ctx context.Context, r envelope.ReceiverRequest) error {
	ref := r.ID
	if r.Conv != "" {
		ref = r.LID
	}
	if r.Kind == envelope.KindMessage && retractedRef(a.store.db, r.Conv, ref, r.FromKey) {
		return errors.New("original ordinary request was deleted before delegation acceptance")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Human != nil && r.Human.AuthorPID != "" {
		return errors.New("device-bound human guest participation grants no receiver delegation or execution authority")
	}
	if _, err := replyReceiverHostIn(a.store.db, ReplyReceiverHost{Address: r.From, Fingerprint: r.FromKey}); err != nil {
		return err
	}
	if r.Conv == "" {
		key, err := a.sendKey(ctx, r.To)
		if err != nil {
			return err
		}
		if key.Fingerprint() != r.ToKey {
			return errors.New("delegated original recipient differs from verified key")
		}
		return nil
	}
	root, err := protocol.ParseConvRoot(r.Root)
	if err != nil {
		return err
	}
	if root.Kind == protocol.ConvKindGroup {
		scope := groupReceiverRequest{From: r.From, Key: r.FromKey, Admission: r.GroupAdmission, Replies: map[string]string{}}
		for _, k := range r.GroupReplies {
			scope.Replies[k.Key] = k.Admission
		}
		m, e := groupReceiverCurrent(a.store.db, r.Conv, scope)
		if e != nil {
			return e
		}
		if !m.device(a.Address, a.Self().Fingerprint()) {
			return errors.New("selected host is not a current group member")
		}
		for _, k := range r.GroupReplies {
			if k.Admission != "" && m.keyEpoch(k.Key) != k.Admission {
				return errors.New("original group reply admission changed")
			}
		}
		if r.PID != "" {
			p, e := participationIn(a.store.db, r.Conv, r.PID, m, r.From)
			if e != nil {
				return e
			}
			original := receiverRequestInner(r)
			if e = externalTurn(original, p, m, r.From, r.FromKey); e != nil {
				return e
			}
		}
		return nil
	}
	own, found, err := a.store.selfPerson(a.Address)
	if err != nil || !found {
		return errors.New("local person proof unavailable")
	}
	if err = a.verifyRoot(ctx, root, own); err != nil {
		return err
	}
	peer := ""
	for _, member := range root.Members {
		ok, e := a.boundIn(ctx, member.Person, member.Roster)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("original conversation member proof unavailable")
		}
		if member.Person != own.info.Person {
			peer = member.Person
		}
	}
	if _, ok := root.Member(own.info.Person); !ok {
		return errors.New("original conversation does not include selected receiver person")
	}
	if _, raw, exists, e := a.store.conversation(r.Conv); e != nil {
		return e
	} else if exists && string(raw) != string(r.Root) {
		return errors.New("delegated conversation root conflicts with pinned original")
	}
	if err := a.store.addConversation(root, r.Root, peer); err != nil {
		return err
	}
	if r.Human != nil {
		if err := a.verifyHumanProof(ctx, root, r.Human); err != nil {
			return err
		}
		tx, err := a.store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := insertHumanProof(tx, r.Human); err != nil {
			return err
		}
		if err := humanAuthorization(tx, r.Conv, r.Human, r.From, r.FromKey, a.Address, a.Self().Fingerprint()); err != nil {
			return err
		}
		return a.store.done(tx.Commit())
	}
	return nil
}
func receiverRequestInner(r envelope.ReceiverRequest) envelope.Inner {
	v := envelope.Version
	if r.Conv != "" {
		v = envelope.Version2
	}
	return envelope.Inner{V: v, ID: r.ID, LID: r.LID, From: r.From, To: r.To, TS: r.TS, Kind: r.Kind, Body: r.Body, ReplyTo: r.ReplyTo, Conv: r.Conv, Root: r.Root, Origin: r.Origin, Emotion: r.Emotion, Target: r.Target, PID: r.PID, Attachments: r.Attachments, Human: r.Human}
}
func (a *Agent) acceptReceiverDelegation(ctx context.Context, in envelope.Inner, fp string) error {
	if err := a.receiverSetupSender(a.store.db, in, fp); err != nil {
		return err
	}
	var priorState string
	if err := a.store.db.QueryRow(`SELECT state FROM inbox WHERE id=?`, in.ID).Scan(&priorState); err != nil {
		return err
	}
	granted, err := taskGranted(a.store.db, in.From, fp)
	if err != nil {
		return err
	}
	if priorState != stateAccepted && !granted {
		return ErrNotPending
	}
	op, err := envelope.ParseReceiverOperation([]byte(in.Body), *in.ReceiverRoute, "", in.Attachments)
	if err != nil {
		return err
	}
	if op.Request.FromKey != fp {
		return errors.New("original author key differs from signed delegation")
	}
	if err = a.verifyReceiverOriginal(ctx, *op.Request); err != nil {
		return err
	}
	binding, err := a.prepareReplyReceiver(ptrReceiver(localReceiverChoice(*op.Receiver)))
	if err != nil {
		return err
	}
	key, err := replyReceiverHostIn(a.store.db, ReplyReceiverHost{Address: in.From, Fingerprint: fp})
	if err != nil {
		return err
	}
	recipient, err := key.Recipient()
	if err != nil {
		return err
	}
	route := *in.ReceiverRoute
	route.Op = "ready"
	body, _ := json.Marshal(envelope.ReceiverOperation{V: 1})
	reply := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: a.Address, To: in.From, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: string(body), ReplyTo: in.ID, ReceiverRoute: &route}
	env, err := envelope.Seal(reply, a.id.Sign, recipient)
	if err != nil {
		return err
	}
	readyCopies := []outCopy{{in: reply, env: env, state: stateQueued, required: protocol.CapReplyReceiver, recipientFP: fp}}
	deleted := false
	err = a.store.addConvOutbox(readyCopies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if e := a.receiverSetupSender(tx, in, fp); e != nil {
			return e
		}
		var state, stored, key string
		if e := tx.QueryRow(`SELECT state,body,verified_by FROM inbox WHERE id=? AND receiver_route=?`, in.ID, receiverRouteJSON(in.ReceiverRoute)).Scan(&state, &stored, &key); e != nil {
			return e
		}
		if stored != in.Body || key != fp {
			return errors.New("delegation changed before acceptance")
		}
		granted, e := taskGranted(tx, in.From, fp)
		if e != nil {
			return e
		}
		if state != stateAccepted && !granted {
			return ErrNotPending
		}
		var n int
		if e = tx.QueryRow(`SELECT count(*) FROM reply_receivers WHERE id=?`, in.ID).Scan(&n); e != nil {
			return e
		}
		if n != 0 {
			return errReceiverSetupDone
		}
		stamp, e := binding.resolve(tx)
		if e != nil {
			return e
		}
		if binding.guard != nil {
			if e = binding.guard(tx); e != nil {
				return e
			}
		}
		if stamp != nil {
			preset, mode := binding.receiver.Preset, binding.receiver.Mode
			if binding.receiver.OnClose != nil {
				preset, mode = binding.handoff.Preset, binding.receiver.OnClose.Mode
			}
			if receiverPreset(stamp, mode) != preset {
				return errors.New("selected local receiver preset changed before delegation acceptance")
			}
		}
		remote := &receiverRemoteState{Role: "imported", Route: *in.ReceiverRoute, Request: *op.Request, Choice: *op.Receiver, Ready: true}
		b := ReplyReceiverBinding{ID: in.ID, Conv: op.Request.Conv, RequestRef: in.ReceiverRoute.RequestRef, Receiver: binding.receiver, Executor: stamp, handoff: binding.handoff, remote: remote, State: "pending"}
		if op.Request.GroupAdmission != "" {
			s := groupReceiverRequest{From: op.Request.From, Key: fp, Admission: op.Request.GroupAdmission, Replies: map[string]string{}}
			for _, k := range op.Request.GroupReplies {
				s.Replies[k.Key] = k.Admission
			}
			b.group = groupReceiverScopes{b.RequestRef: s}
		}
		if e = groupReceiverBinding(tx, b); e != nil {
			return e
		}
		deleted, e = a.importHeldReceiverRetractions(tx, remote)
		if e != nil {
			return e
		}
		deleted = deleted || receiverRetracted(tx, remote)
		if deleted {
			remote.Redacted = true
			remote.Request.Body = ""
			remote.Request.Attachments = nil
			readyCopies[0].state = stateNotDelivered
			readyCopies[0].why = "original ordinary request deleted before local import; no ready authorized"
		}
		raw, e := encodeReceiverBinding(b)
		if e != nil {
			return e
		}
		var executor any
		if stamp != nil {
			data, _ := json.Marshal(stamp)
			executor = string(data)
		}
		var canceled any
		if deleted {
			canceled = time.Now().Unix()
		}
		if _, e = tx.Exec(`INSERT INTO reply_receivers(id,conv,request_ref,receiver,executor,created_at,canceled_at) VALUES(?,?,?,?,?,?,?)`, b.ID, b.Conv, b.RequestRef, string(raw), executor, time.Now().Unix(), canceled); e != nil {
			return e
		}
		if deleted {
			_, e = tx.Exec(`UPDATE inbox SET body='',state=?,detail='original ordinary request deleted before local import; no selected dispatch',read_at=unixepoch(),notified=1 WHERE id=?`, stateNotRun, in.ID)
			return e
		}
		_, e = tx.Exec(`UPDATE inbox SET state=?,detail='delegation approved locally; no remote task executed',read_at=unixepoch(),notified=1 WHERE id=?`, stateManual, in.ID)
		return e
	}, "")
	if err == nil {
		if deleted {
			a.redactReceiverDelegations(ControlRef{ID: op.Request.ID, Fingerprint: op.Request.FromKey})
		}
		a.kickNow()
		notifyDaemon(a.home)
	}
	return err
}
func ptrReceiver(r ReplyReceiver) *ReplyReceiver { return &r }

var errReceiverSetupDone = errors.New("receiver setup already completed")

func (a *Agent) acceptReceiverReady(in envelope.Inner, fp string) error {
	if err := a.receiverSetupSender(a.store.db, in, fp); err != nil {
		return err
	}
	op, err := envelope.ParseReceiverOperation([]byte(in.Body), *in.ReceiverRoute, in.ReplyTo, nil)
	if err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = a.receiverSetupSender(tx, in, fp); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT id,receiver FROM reply_receivers`)
	if err != nil {
		return err
	}
	var matches []ReplyReceiverBinding
	for rows.Next() {
		var b ReplyReceiverBinding
		var raw string
		if err = rows.Scan(&b.ID, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = decodeReceiverBinding(raw, &b); err != nil {
			rows.Close()
			return err
		}
		if b.remote != nil && b.remote.Role == "origin" && b.remote.Route.DelegationID == in.ReplyTo {
			matches = append(matches, b)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(matches) != 1 {
		return errors.New("ready has no unambiguous original delegation")
	}
	b, err := replyReceiverIn(tx, matches[0].ID)
	if err != nil {
		return err
	}
	r := b.remote
	expected := r.Route
	expected.Op = "ready"
	if expected != *in.ReceiverRoute || r.Route.Host != in.From || r.Route.HostKey != fp || b.State == "canceled" {
		return errors.New("ready differs from exact authorized delegation")
	}
	if receiverRetracted(tx, r) {
		return errors.New("original ordinary request was deleted before ready")
	}
	if r.Ready {
		return nil
	}
	if op.Detail != "" {
		r.Refusal = op.Detail
		raw, e := encodeReceiverBinding(b)
		if e != nil {
			return e
		}
		if _, err = tx.Exec(`UPDATE reply_receivers SET receiver=? WHERE id=?`, string(raw), b.ID); err != nil {
			return err
		}
		for _, id := range r.OriginalIDs {
			if _, err = tx.Exec(`UPDATE outbox SET error=? WHERE id=? AND state=?`, op.Detail, id, stateReceiverWaiting); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE inbox SET detail=? WHERE id=?`, op.Detail, in.ID)
		if err != nil {
			return err
		}
		return a.store.done(tx.Commit())
	}
	if err = groupReceiverBinding(tx, b); err != nil {
		return err
	}
	if err = frozenReceiverBatch(tx, r); err != nil {
		return err
	}
	for _, id := range r.OriginalIDs {
		if _, err = tx.Exec(`UPDATE outbox SET state=?,error=NULL WHERE id=? AND state=?`, stateQueued, id, stateReceiverWaiting); err != nil {
			return err
		}
	}
	r.Ready = true
	raw, err := encodeReceiverBinding(b)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE reply_receivers SET receiver=? WHERE id=?`, string(raw), b.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE inbox SET state=?,read_at=unixepoch(),notified=1 WHERE id=?`, stateManual, in.ID); err != nil {
		return err
	}
	err = a.store.done(tx.Commit())
	if err == nil {
		a.kickNow()
		notifyDaemon(a.home)
	}
	return err
}

func (a *Agent) declineReceiverDelegation(ctx context.Context, in envelope.Inner, fp, reason string) (SendResult, error) {
	if err := a.receiverSetupSender(a.store.db, in, fp); err != nil {
		return SendResult{}, err
	}
	if reason == "" {
		reason = "selected-host delegation declined locally; original request remains unsent"
	}
	op := envelope.ReceiverOperation{V: 1, Detail: reason}
	route := *in.ReceiverRoute
	route.Op = "ready"
	body, _ := json.Marshal(op)
	reply := envelope.Inner{V: envelope.Version, ID: protocol.NewID(), From: a.Address, To: in.From, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: string(body), ReplyTo: in.ID, ReceiverRoute: &route}
	key, e := replyReceiverHostIn(a.store.db, ReplyReceiverHost{Address: in.From, Fingerprint: fp})
	if e != nil {
		return SendResult{}, e
	}
	to, e := key.Recipient()
	if e != nil {
		return SendResult{}, e
	}
	env, e := envelope.Seal(reply, a.id.Sign, to)
	if e != nil {
		return SendResult{}, e
	}
	e = a.store.addConvOutbox([]outCopy{{in: reply, env: env, state: stateQueued, required: protocol.CapReplyReceiver, recipientFP: fp}}, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if e := a.receiverSetupSender(tx, in, fp); e != nil {
			return e
		}
		res, e := tx.Exec(`UPDATE inbox SET state=?,detail=?,read_at=unixepoch(),notified=1 WHERE id=? AND state=? AND verified_by=? AND receiver_route=?`, stateDeclined, reason, in.ID, stateAwaiting, fp, receiverRouteJSON(in.ReceiverRoute))
		if e != nil {
			return e
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotPending
		}
		return nil
	}, "")
	if e != nil {
		return SendResult{}, e
	}
	notifyDaemon(a.home)
	return a.deliver(ctx, env, nil)
}

// Existing push/worker wake drains setup without invoking a model or polling.
func (a *Agent) drainReceiverSetups() {
	rows, err := a.store.db.Query(`SELECT id FROM inbox WHERE conv IS NULL AND receiver_route IS NOT NULL AND json_extract(receiver_route,'$.op')!='request' AND state IN (?,?,?,?) ORDER BY arrival`, stateNotRun, stateAwaiting, stateAccepted, statePending)
	if err != nil {
		return
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
		in, fp, e := a.storedReceiverSetup(id)
		if e == nil {
			switch in.ReceiverRoute.Op {
			case "catalog":
				e = a.processReceiverCatalog(in, fp)
			case "delegate":
				e = a.acceptReceiverDelegation(context.Background(), in, fp)
			case "ready":
				e = a.acceptReceiverReady(in, fp)
			}
		}
		if e != nil && !errors.Is(e, ErrNotPending) && !errors.Is(e, errReceiverSetupDone) {
			a.store.db.Exec(`UPDATE inbox SET detail=? WHERE id=?`, e.Error(), id)
		}
	}
}
