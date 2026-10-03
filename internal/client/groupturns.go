package client

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const groupTurnRecipientSchema = `ALTER TABLE outbox ADD COLUMN recipient_fp TEXT;`

func groupTurnPacketIn(q dbq, conv string) (GroupContext, error) {
	var packet GroupContext
	var raw []byte
	err := q.QueryRow(`SELECT payload FROM group_context WHERE conv=?`, conv).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return packet, ErrGroupContextPending
	}
	if err != nil {
		return packet, err
	}
	err = json.Unmarshal(raw, &packet)
	return packet, err
}

func (a *Agent) projectGroupConversations(rows []ConversationInfo) ([]ConversationInfo, error) {
	data, err := a.store.db.Query(`SELECT conv FROM group_context ORDER BY conv`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for data.Next() {
		var id string
		if err = data.Scan(&id); err != nil {
			data.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = data.Err()
	data.Close()
	if err != nil {
		return nil, err
	}
	for _, conv := range ids {
		packet, err := groupTurnPacketIn(a.store.db, conv)
		if err != nil {
			return nil, err
		}
		view := ConversationInfo{ID: conv, Kind: protocol.ConvKindGroup, Title: packet.State.Title, Created: packet.Root.Created, Creator: packet.Root.Creator.Address}
		withdrawals, err := a.groupWithdrawals(conv)
		if err != nil {
			return nil, err
		}
		for _, member := range packet.State.EffectiveMembers(append(packet.Withdrawals, withdrawals...)) {
			p, ok, err := a.store.personByID(member.Person)
			if err != nil {
				return nil, err
			}
			if ok {
				view.Members = append(view.Members, p.info)
			}
		}
		if err = groupDeliveryRecipient(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
			view.Frozen = err.Error()
			ids, e := a.store.participationIDs(conv)
			if e != nil {
				return nil, e
			}
			for _, pid := range ids {
				if _, e = groupVisitorInvite(a.store.db, packet.Root, pid, a.Address, a.Self().Fingerprint()); e != nil {
					if errors.Is(e, ErrGroupContextPending) {
						continue
					}
					return nil, e
				}
				view.Role = "visitor" // disclosure projection only; not membership
				m, e := controlMembers(a.store.db, conv)
				if e != nil {
					if errors.Is(e, ErrGroupContextPending) {
						continue
					}
					return nil, e
				}
				info, e := participationIn(a.store.db, conv, pid, m, a.Address)
				if e != nil && !errors.Is(e, ErrNoParticipation) {
					return nil, e
				}
				if info.External && (info.State == PartInvited || info.State == PartActive) {
					view.Frozen = ""
				}
			}
		} else {
			view.Role = "member"
		}
		replaced := false
		for i := range rows {
			if rows[i].ID == conv {
				rows[i] = view
				replaced = true
				break
			}
		}
		if !replaced {
			rows = append(rows, view)
		}
	}
	return rows, nil
}

// Group replies name the parent's logical identity shared by every copy.
func groupReplyLID(q dbq, conv, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	rows, err := q.Query(`SELECT conv,lid FROM inbox WHERE (id=? OR lid=?) AND ref_id IS NULL UNION SELECT conv,lid FROM outbox WHERE (id=? OR lid=?) AND ref_id IS NULL`, ref, ref, ref, ref)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lid string
	for rows.Next() {
		var c sql.NullString
		var l sql.NullString
		if err = rows.Scan(&c, &l); err != nil {
			return "", err
		}
		if !c.Valid || c.String != conv || !l.Valid || l.String == "" || lid != "" && lid != l.String {
			return "", errors.New("group reply: ambiguous parent or another conversation")
		}
		lid = l.String
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	if lid == "" {
		return "", ErrGroupContextPending
	}
	return lid, nil
}

func ordinaryGroupTurn(in envelope.Inner) bool {
	return in.Kind == envelope.KindMessage && in.Sub == "" && in.Target == nil && in.PID == "" && in.AgentID == "" && in.Status == "" && in.Ref == nil && (in.Origin == "" || in.Origin == envelope.OriginUI)
}

func sameGroupRoot(a, b protocol.ConvRoot) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func groupTurnCheck(q dbq, packet GroupContext, address, fp string) error {
	return groupDeliveryRecipient(q, packet, address, fp)
}

func (a *Agent) sendGroupTurn(ctx context.Context, conv string, m ConvOutgoing, binding *replyBinding) (ConvSent, error) {
	if m.Kind == "" {
		m.Kind = envelope.KindMessage
	}
	if m.Origin == "" {
		m.Origin = envelope.OriginUI
	}
	test := envelope.Inner{Kind: m.Kind, Sub: m.sub, Target: m.Target, PID: m.PID, AgentID: m.AgentID, Status: m.status, Origin: m.Origin}
	if !ordinaryGroupTurn(test) || m.claim != nil || m.selfJob {
		return ConvSent{}, errors.New("group: only ordinary human messages are supported")
	}
	if m.Body == "" && len(m.Files) == 0 {
		return ConvSent{}, errors.New("nothing to send: no text and no files")
	}
	if len(m.Files) > envelope.MaxAttachments {
		return ConvSent{}, errors.New("group: too many files")
	}
	packet, err := groupTurnPacketIn(a.store.db, conv)
	if err != nil {
		return ConvSent{}, err
	}
	// Fresh rosters decide the devices. With the Hub out of reach the copies
	// go to the devices pinned here and wait, as a DM's do: each is released
	// only once its recipient's current record shows it may read it, and the
	// current group state is checked again when it is sent.
	waiting := ""
	for _, member := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, member.Person, false); hubUnreachable(err) {
			waiting = "cannot reach the Hub: " + err.Error()
			break
		} else if err != nil {
			return ConvSent{}, err
		}
	}
	if err = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return ConvSent{}, err
	}
	admission, err := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return ConvSent{}, err
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		return ConvSent{}, err
	}
	reply, err := groupReplyLID(a.store.db, conv, m.ReplyTo)
	if err != nil {
		return ConvSent{}, err
	}
	withdrawals, err := a.groupWithdrawals(conv)
	if err != nil {
		return ConvSent{}, err
	}
	raw, _ := json.Marshal(packet.Root)
	lid := protocol.NewID()
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return ConvSent{}, err
	}
	defer release()
	var copies []outCopy
	stored := false
	defer func() {
		if !stored {
			if binding != nil && binding.setup != nil {
				a.releaseGroupCopies([]outCopy{*binding.setup})
			}
			a.releaseGroupCopies(copies)
		}
	}()
	rosters := map[string]string{}
	for _, f := range m.Files {
		if err = a.keepSent(f.Path); err != nil {
			return ConvSent{}, err
		}
	}
	for _, member := range packet.State.EffectiveMembers(append(packet.Withdrawals, withdrawals...)) {
		person, ok, e := a.store.personByID(member.Person)
		if e != nil {
			return ConvSent{}, e
		}
		if !ok {
			return ConvSent{}, ErrGroupContextPending
		}
		rosters[member.Person] = person.roster.Hash()
		for _, device := range person.roster.Devices {
			if device.Address == a.Address {
				continue
			}
			if err = groupTurnCheck(a.store.db, packet, device.Address, device.Fingerprint()); err != nil {
				return ConvSent{}, err
			}
			key, e := a.sendKey(ctx, device.Address)
			if e != nil {
				return ConvSent{}, e
			}
			if key.Fingerprint() != device.Fingerprint() {
				return ConvSent{}, errors.New("group: directory differs from pinned exact recipient key")
			}
			recipient, e := key.Recipient()
			if e != nil {
				return ConvSent{}, e
			}
			fan := []envelope.Fan{{Person: me.roster.Person, Roster: me.roster.Hash()}}
			if person.roster.Person != me.roster.Person {
				fan = append(fan, envelope.Fan{Person: person.roster.Person, Roster: person.roster.Hash()})
			}
			in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: lid, From: a.Address, To: device.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: conv, Root: raw, Body: m.Body, ReplyTo: reply, Origin: m.Origin, Emotion: m.Emotion, Replica: person.roster.Person == me.roster.Person, Fan: fan}
			copies = append(copies, outCopy{in: in, state: stateQueued, required: protocol.CapGroup, recipientFP: key.Fingerprint(), groupAdmission: admission.Hash()})
			if waiting != "" {
				copies[len(copies)-1].state, copies[len(copies)-1].why = stateConvWaiting, waiting
			}
			copy := &copies[len(copies)-1]
			for _, file := range m.Files {
				att, e := a.spoolNamed(file, recipient)
				if e != nil {
					return ConvSent{}, e
				}
				copy.in.Attachments = append(copy.in.Attachments, att)
			}
			copy.env, e = envelope.Seal(copy.in, a.id.Sign, recipient)
			if e != nil {
				return ConvSent{}, e
			}
		}
	}
	if len(copies) == 0 {
		return ConvSent{}, errors.New("group: no other current device to receive a copy")
	}
	if err = a.prepareRemoteCopies(ctx, binding, copies, m.Files); err != nil {
		return ConvSent{}, err
	}
	beforeOutbox()
	err = a.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		current, e := groupTurnPacketIn(tx, conv)
		if e != nil {
			return e
		}
		if current.State.Hash() != packet.State.Hash() {
			return ErrGroupContextPending
		}
		if e = groupTurnCheck(tx, current, a.Address, a.Self().Fingerprint()); e != nil {
			return e
		}
		for person, hash := range rosters {
			p, ok, e := personByIDIn(tx, person)
			if e != nil {
				return e
			}
			if !ok || p.roster.Hash() != hash {
				return ErrGroupContextPending
			}
		}
		for _, copy := range copies {
			if e = groupTurnCheck(tx, current, copy.env.To, copy.recipientFP); e != nil {
				return e
			}
		}
		return nil
	}, "", binding)
	if err != nil {
		return ConvSent{}, err
	}
	stored = true
	release()
	if binding != nil && binding.setup != nil {
		if _, e := a.deliver(ctx, binding.setup.env, nil); e != nil && !retryable(e) {
			return ConvSent{}, e
		}
	}
	a.kickNow()
	defer notifyDaemon(a.home)
	sent := ConvSent{ID: copies[0].env.ID, LID: lid, State: protocol.StateDelivered}
	for _, copy := range copies {
		cp := ConvCopy{ID: copy.env.ID, To: copy.env.To, State: copy.state, Detail: copy.why}
		if copy.state != stateConvWaiting { // released by releaseConv, never sent from here
			r, e := a.deliver(ctx, copy.env, nil)
			if e != nil {
				cp.Detail = e.Error()
			} else {
				cp.State, cp.Detail = r.State, r.Detail
			}
		}
		sent.Copies = append(sent.Copies, cp)
		if rank(cp.State) < rank(sent.State) {
			sent.State, sent.Detail = cp.State, cp.Detail
		}
	}
	return sent, nil
}

func (a *Agent) admitGroupTurn(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, me, sp personRow, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	if in.Sub == envelope.SubHistory {
		return a.admitGroupHistory(ctx, env, in, root, sender, fromQuarantine, hold)
	}
	if in.Sub == envelope.SubFile {
		return a.admitGroupFile(ctx, env, in, root, sender, hold)
	}
	if !ordinaryGroupTurn(in) {
		return hold(reasonInvalid, "group: this operation is not an ordinary human turn")
	}
	packet, err := groupTurnPacketIn(a.store.db, in.Conv)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	if !sameGroupRoot(packet.Root, root) {
		return hold(reasonInvalid, "group: root differs from verified current context")
	}
	for _, p := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, p.Person, false); err != nil {
			return err
		}
	}
	me, _, err = a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	check := func(q dbq) error {
		current, e := groupTurnPacketIn(q, in.Conv)
		if e != nil {
			return e
		}
		if e = groupTurnCheck(q, current, a.Address, a.Self().Fingerprint()); e != nil {
			return e
		}
		if e = groupTurnCheck(q, current, sender.Address, sender.Fingerprint()); e != nil {
			return e
		}
		if in.Replica != (me.roster.Person == sp.roster.Person) {
			return errors.New("group: replica does not match sender's own person")
		}
		for _, fan := range in.Fan {
			if fan.Person != me.roster.Person && fan.Person != sp.roster.Person {
				return errors.New("group: fan names another person")
			}
			var n int
			if e = q.QueryRow(`SELECT count(*) FROM person_chain WHERE person=? AND hash=?`, fan.Person, fan.Roster).Scan(&n); e != nil {
				return e
			}
			if n != 1 {
				return errors.New("group: fan roster lacks verified person chain")
			}
		}
		parent, e := groupReplyLID(q, in.Conv, in.ReplyTo)
		if errors.Is(e, ErrGroupContextPending) && protocol.ValidID(in.ReplyTo) {
			return nil
		} // absent selected-history context; never fetch another turn
		if e != nil {
			return e
		}
		if parent != in.ReplyTo {
			return errors.New("group: reply must name exact logical parent")
		}
		return nil
	}
	if err = check(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	now := time.Now()
	admission, err := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return err
	}
	forward := a.forwardStale(me, in, sender.Fingerprint(), in.Root, admission.Hash())
	result, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		if e := check(tx); e != nil {
			return e
		}
		for _, copy := range forward {
			current, e := groupTurnPacketIn(tx, in.Conv)
			if e != nil {
				return e
			}
			if e = groupTurnCheck(tx, current, copy.env.To, copy.recipientFP); e != nil {
				return e
			}
		}
		if e := insertCopies(tx, forward); e != nil {
			return e
		}
		current, e := groupTurnPacketIn(tx, in.Conv)
		if e != nil {
			return e
		}
		stamp, e := groupMemberAdmission(tx, current, a.Address, a.Self().Fingerprint())
		if e != nil {
			return e
		}
		if stamp.Hash() != admission.Hash() {
			return ErrGroupContextPending
		}
		if _, e = tx.Exec(`UPDATE inbox SET group_admission=? WHERE id=?`, stamp.Hash(), in.ID); e != nil {
			return e
		}
		if me.roster.Person == sp.roster.Person {
			_, e := tx.Exec(`UPDATE inbox SET read_at=? WHERE id=?`, now.Unix(), in.ID)
			return e
		}
		return queueAlert(tx, in, sender.Fingerprint(), now)
	})
	if err != nil {
		return err
	}
	if result == admitConflict {
		return hold(reasonDuplicate, "group: conflicting logical turn")
	}
	if result == admitted {
		work := convRetry
		if len(in.Attachments) > 0 {
			work |= convFetch
		}
		a.convWork.due(work)
		a.kickNow()
		a.wakeWorker() // an exactly correlated local receiver may own this human reply
		a.wakeAlerts()
	}
	return nil
}

func (a *Agent) mayDeliverGroupTurn(env envelope.Envelope) (bool, bool, error) {
	var conv, state, fp, required, sub, body, grant string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,''),state,coalesce(recipient_fp,''),coalesce(required_cap,''),coalesce(sub,''),body,coalesce(group_admission,'') FROM outbox WHERE id=?`, env.ID).Scan(&conv, &state, &fp, &required, &sub, &body, &grant)
	if err != nil || conv == "" {
		return false, false, nil
	}
	packet, e := groupTurnPacketIn(a.store.db, conv)
	if e != nil {
		if required == protocol.CapGroup {
			return true, false, nil
		}
		return false, false, nil
	}
	if state != stateQueued {
		return true, false, nil
	}
	if fp == "" {
		return true, false, nil
	} // legacy/unbound rows never infer today's key
	e = groupTurnCheck(a.store.db, packet, env.To, fp)
	if e == nil {
		e = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint())
	}
	// This outbox history row pins the destination admission; ordinary turn
	// rows use the same column for own original live provenance. Imported
	// selected inbox rows retain NULL and gain no direct-live authority.
	if e == nil && sub == envelope.SubHistory {
		var item HistoryItem
		admission, admissionErr := groupMemberAdmission(a.store.db, packet, env.To, fp)
		if admissionErr != nil {
			e = admissionErr
		} else if grant == "" || admission.Hash() != grant {
			e = errors.New("group: queued history recipient admission changed")
		} else if decodeStrict([]byte(body), &item) != nil {
			e = ErrGroupHistoryUnavailable
		} else if groupControlSub(item.Sub) {
			me, ok, err := a.store.selfPerson(a.Address)
			if err != nil {
				e = err
			} else if !ok || !me.has(env.To, fp) {
				e = errors.New("group: historical control recipient is not an own linked device")
			} else {
				e = a.groupControlHistoryCheck(a.store.db, packet.Root, a.Self(), item)
			}
		} else if groupParticipationHistoryItem(item) {
			e = a.groupParticipationHistoryOutboundCheck(a.store.db, packet, env.To, fp, item)
		} else {
			e = a.groupHistoryOutboundCheck(a.store.db, packet, env.To, fp, item)
		}
	}
	if e == nil && sub == envelope.SubFile {
		var message fileMsg
		if decodeStrict([]byte(body), &message) != nil {
			e = ErrGroupHistoryUnavailable
		} else {
			e = a.groupFileOutboundCheck(a.store.db, packet, env.To, fp, message)
		}
	}
	if errors.Is(e, ErrGroupContextPending) || errors.Is(e, errPersonConflict) {
		return true, false, nil
	}
	if e != nil {
		if err = a.store.setOutboxState(env.ID, stateNotDelivered, "group membership no longer eligible", ""); err == nil {
			if sub == envelope.SubHistory {
				_, err = a.store.db.Exec(`UPDATE outbox SET body='' WHERE id=?`, env.ID)
			}
			a.releaseSpool(env)
		}
		return true, false, err
	}
	return true, true, nil
}

func (a *Agent) admitGroupOwnCopy(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, me, sp personRow, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	if !in.Replica || me.roster.Person != sp.roster.Person {
		return hold(reasonInvalid, "group history/files require a current own linked device")
	}
	packet, err := groupTurnPacketIn(a.store.db, in.Conv)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	if !sameGroupRoot(root, packet.Root) {
		return hold(reasonInvalid, "group own copy root differs")
	}
	for _, member := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, member.Person, false); err != nil {
			return err
		}
	}
	check := func(q dbq) error {
		current, e := groupTurnPacketIn(q, in.Conv)
		if e != nil {
			return e
		}
		if e = groupTurnCheck(q, current, a.Address, a.Self().Fingerprint()); e != nil {
			return e
		}
		return groupTurnCheck(q, current, sender.Address, sender.Fingerprint())
	}
	if err = check(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	if in.Sub == envelope.SubFile {
		return a.admitFile(env, in, hold, check)
	}
	var item HistoryItem
	if err = decodeGroupCarrierJSON([]byte(in.Body), &item); err != nil || item.V != 1 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) || !protocol.ValidFingerprint(item.FromKey) {
		return hold(reasonInvalid, "group own copy has malformed item")
	}
	original := item.inner(in.Conv)
	if !ordinaryGroupTurn(original) || item.Ref != nil {
		return hold(reasonInvalid, "group own history grants no execution/controls")
	}
	checkItem := func(q dbq) error {
		if e := check(q); e != nil {
			return e
		}
		current, e := groupTurnPacketIn(q, in.Conv)
		if e != nil {
			return e
		}
		if e = groupTurnCheck(q, current, item.From, item.FromKey); e != nil {
			return e
		}
		parent, e := groupReplyLID(q, in.Conv, item.ReplyTo)
		if e != nil {
			return e
		}
		if parent != item.ReplyTo {
			return errors.New("group history reply lacks exact logical parent")
		}
		return nil
	}
	if err = checkItem(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	res, err := a.store.addHistoryInbox(original, item.At, item.FromKey, env.From, env.ID, fromQuarantine, func(tx *sql.Tx) error { return checkItem(tx) })
	if err != nil {
		return err
	}
	if res == admitConflict {
		return hold(reasonDuplicate, "group own history conflicts")
	}
	if res == admitted {
		a.convWork.due(convRetry)
		a.kickNow()
	}
	return nil
}
