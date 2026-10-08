package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var errGroupPublished = errors.New("group: exact publication already completed")
var errGroupRecipientWithdrawn = errors.New("group: recipient admission withdrawn")
var errGroupRecipientNotCurrent = errors.New("group: recipient exact key is not a current member device")

// Exact custody of a superseded local attempt completes without publishing an
// old snapshot again. Only our newer atomically published batch supplies that
// recovery guarantee; a remotely installed context alone is insufficient.
func (a *Agent) completeSupersededGroupPublication(c protocol.GroupCommit, exact, payload []byte) (bool, error) {
	completed := false
	err := a.store.addConvOutbox(nil, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		var oldRecord, oldPayload, currentRaw []byte
		if err := tx.QueryRow(`SELECT record,payload FROM group_publications WHERE conv=? AND bootstrap=? AND seq=?`, c.Conv, c.Bootstrap, c.Seq).Scan(&oldRecord, &oldPayload); err != nil {
			return err
		}
		if !bytes.Equal(oldRecord, exact) || !bytes.Equal(oldPayload, payload) {
			return errors.New("group: superseded publication bytes changed")
		}
		if err := tx.QueryRow(`SELECT payload FROM group_context WHERE conv=?`, c.Conv).Scan(&currentRaw); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		var current GroupContext
		if err := json.Unmarshal(currentRaw, &current); err != nil {
			return err
		}
		if current.State.Seq <= c.Seq || current.Root.ID() != c.Conv || current.Root.Creator.Fingerprint != c.Bootstrap {
			return nil
		}
		var newerRecord []byte
		if err := tx.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND bootstrap=? AND seq=? AND published=1`, c.Conv, c.Bootstrap, current.State.Seq).Scan(&newerRecord); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		original, err := groupProofRecord(tx, c.Conv, c.Bootstrap, current.State.Seq)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(original)
		if !bytes.Equal(raw, newerRecord) || !original.Matches(current.State) {
			return errors.New("group: superseding publication differs from verified installed state")
		}
		if _, err = tx.Exec(`UPDATE group_publications SET published=1 WHERE conv=? AND bootstrap=? AND seq=?`, c.Conv, c.Bootstrap, c.Seq); err != nil {
			return err
		}
		completed = true
		return nil
	}, "")
	return completed, err
}

// groupDeliveryRecipient checks the candidate committed snapshot and latest
// pinned recipient key. The caller uses it both before sealing and in the
// existing outbox installation transaction; retry uses the installed snapshot.
func groupDeliveryRecipient(q dbq, packet GroupContext, address, fp string) error {
	var head sql.NullInt64
	if err := q.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, packet.State.Conv, packet.Root.Creator.Fingerprint).Scan(&head); err != nil {
		return err
	}
	if !head.Valid || head.Int64 != packet.State.Seq {
		return ErrGroupContextPending
	}
	var seq int64
	var hash string
	err := q.QueryRow(`SELECT seq,hash FROM group_known_heads WHERE conv=? AND bootstrap=?`, packet.State.Conv, packet.Root.Creator.Fingerprint).Scan(&seq, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (seq > packet.State.Seq || seq == packet.State.Seq && hash != packet.State.Hash()) {
		return ErrGroupContextPending
	}
	for _, member := range packet.State.Members {
		person, ok, err := personByIDIn(q, member.Person)
		if err != nil {
			return err
		}
		if !ok || !person.has(address, fp) {
			continue
		}
		if person.info.State == personConflict {
			return errPersonConflict
		}
		var withdrawn int
		if err = q.QueryRow(`SELECT count(*) FROM (SELECT admission FROM group_withdrawals WHERE conv=? AND person=? AND admission=? UNION ALL SELECT admission FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?)`, packet.State.Conv, member.Person, member.Admission.Hash(), packet.State.Conv, member.Person, member.Admission.Hash()).Scan(&withdrawn); err != nil {
			return err
		}
		if withdrawn != 0 || packet.State.Withdrawn(member, packet.Withdrawals) {
			return errGroupRecipientWithdrawn
		}
		return nil
	}
	return errGroupRecipientNotCurrent
}

type groupDeliveryPayload struct {
	sub        string
	descriptor protocol.GroupCarrier
	raw        []byte
}

func groupDeliveryPayloads(q dbq, packet GroupContext) ([]groupDeliveryPayload, error) {
	var payloads []groupDeliveryPayload
	rows, err := q.Query(`SELECT record FROM group_proof_records WHERE conv=? AND bootstrap=? AND seq<=? ORDER BY seq`, packet.State.Conv, packet.Root.Creator.Fingerprint, packet.State.Seq)
	if err != nil {
		return nil, err
	}
	var page protocol.GroupJournalPage
	flush := func() {
		if len(page.Records) == 0 {
			return
		}
		last := page.Records[len(page.Records)-1]
		raw, _ := json.Marshal(page)
		payloads = append(payloads, groupDeliveryPayload{envelope.SubGroupProof, protocol.GroupCarrier{V: 1, Seq: last.Seq, Hash: last.Hash}, raw})
		page = protocol.GroupJournalPage{}
	}
	expected := int64(0)
	for rows.Next() {
		var raw []byte
		var record protocol.GroupCommit
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			break
		}
		if record.Seq != expected {
			err = ErrGroupContextPending
			break
		}
		expected++
		candidate := protocol.GroupJournalPage{Records: append(append([]protocol.GroupCommit{}, page.Records...), record)}
		encoded, _ := json.Marshal(candidate)
		if len(candidate.Records) > 16 || len(encoded) > protocol.MaxBody-1024 {
			flush()
			candidate = protocol.GroupJournalPage{Records: []protocol.GroupCommit{record}}
			encoded, _ = json.Marshal(candidate)
		}
		if len(encoded) > protocol.MaxBody-1024 {
			err = errors.New("group: original proof record exceeds blob page bound")
			break
		}
		page = candidate
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if rowErr != nil {
		return nil, rowErr
	}
	if expected != packet.State.Seq+1 {
		return nil, ErrGroupContextPending
	}
	flush()
	packet.Proof = nil
	packet.Memberships, err = roomMembershipProof(q, packet)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	if len(raw) > protocol.MaxGroupState {
		return nil, errors.New("group: current context exceeds bound")
	}
	payloads = append(payloads, groupDeliveryPayload{envelope.SubGroupContext, protocol.GroupCarrier{V: 1, Seq: packet.State.Seq, Hash: packet.State.Hash()}, raw})
	return payloads, nil
}

func (a *Agent) groupDeliveryTo(ctx context.Context, packet GroupContext, payloads []groupDeliveryPayload, dev identity.Public) (copies []outCopy, err error) {
	defer func() {
		if err != nil {
			a.releaseGroupCopies(copies)
		}
	}()
	if err = groupDeliveryRecipient(a.store.db, packet, dev.Address, dev.Fingerprint()); err != nil {
		return nil, err
	}
	rootRaw, _ := json.Marshal(packet.Root)
	key, e := a.sendKey(ctx, dev.Address)
	if e != nil {
		return copies, e
	}
	if key.Fingerprint() != dev.Fingerprint() {
		return copies, errors.New("group: recipient directory key differs from pinned roster")
	}
	recipient, e := key.Recipient()
	if e != nil {
		return copies, e
	}
	for _, p := range payloads {
		p.descriptor.ToKey = key.Fingerprint()
		descriptor, _ := json.Marshal(p.descriptor)
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: a.Address, To: key.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: packet.State.Conv, Root: rootRaw, Sub: p.sub, Body: string(descriptor)}
		copies = append(copies, outCopy{in: in, state: stateQueued, required: protocol.CapGroup, recipientFP: key.Fingerprint()})
		copy := &copies[len(copies)-1]
		path, cleanup, e := a.StageUpload(p.sub+".json", bytes.NewReader(p.raw))
		if e != nil {
			return copies, e
		}
		att, e := a.spoolNamed(OutgoingFile{Path: path, Name: p.sub + ".json"}, recipient)
		cleanup()
		if e != nil {
			return copies, e
		}
		copy.in.Attachments = []envelope.Attachment{att}
		copy.env, e = envelope.Seal(copy.in, a.id.Sign, recipient)
		if e != nil {
			return copies, e
		}
		encoded, _ := json.Marshal(copy.env)
		if len(encoded) > protocol.MaxBody {
			return copies, errors.New("group: encoded carrier exceeds transport bound")
		}
	}
	return copies, nil
}

// groupDeliveryCopies retains ordinary publication fan-out; own-history calls
// the same payload and exact-recipient sealing helpers without fan-out.
func (a *Agent) groupDeliveryCopies(ctx context.Context, packet GroupContext) (copies []outCopy, err error) {
	defer func() {
		if err != nil {
			a.releaseGroupCopies(copies)
		}
	}()
	payloads, err := groupDeliveryPayloads(a.store.db, packet)
	if err != nil {
		return nil, err
	}
	for _, member := range packet.State.EffectiveMembers(packet.Withdrawals) {
		if _, err = a.refreshPerson(ctx, member.Person, false); err != nil {
			return copies, err
		}
		person, ok, e := a.store.personByID(member.Person)
		if e != nil {
			return copies, e
		}
		if !ok || person.info.State == personConflict {
			return copies, ErrGroupContextPending
		}
		for _, dev := range person.roster.Devices {
			if dev.Address == a.Address {
				continue
			}
			part, e := a.groupDeliveryTo(ctx, packet, payloads, dev)
			if e != nil {
				return copies, e
			}
			copies = append(copies, part...)
		}
	}
	visitors, e := a.groupVisitorTargets(packet.State.Conv)
	if e != nil {
		return copies, e
	}
	for _, target := range visitors {
		if err = groupVisitorCarrierDestination(a.store.db, packet, target.pid, target.key.Address, target.key.Fingerprint()); err != nil {
			return copies, err
		}
		for _, p := range payloads {
			var value any
			if p.sub == envelope.SubGroupContext {
				var context GroupContext
				if err = decodeGroupCarrierJSON(p.raw, &context); err != nil {
					return copies, err
				}
				// Outside execution hosts receive only their PID's existing
				// scopes, never other agents' private original invitations.
				context.Memberships = nil
				value = context
			} else if err = decodeGroupCarrierJSON(p.raw, &value); err != nil {
				return copies, err
			}
			copy, e := a.groupLifecycleCopy(packet.Root, p.sub, p.descriptor, value, target.key, target.pid)
			if e != nil {
				return copies, e
			}
			copies = append(copies, copy)
		}
	}
	return copies, nil
}

func (a *Agent) releaseGroupCopies(copies []outCopy) {
	for _, c := range copies {
		a.releaseSpool(envelope.Envelope{ID: c.in.ID, Blobs: blobsOf(c.in.Attachments)})
	}
}

func (a *Agent) mayDeliverGroup(env envelope.Envelope) (bool, bool, error) {
	var sub, state, body, conv, pid string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),state,body,conv,coalesce(pid,'') FROM outbox WHERE id=?`, env.ID).Scan(&sub, &state, &body, &conv, &pid)
	if err != nil || sub != envelope.SubGroupProof && sub != envelope.SubGroupContext {
		return false, false, nil
	}
	if state != stateQueued {
		return true, false, nil
	}
	var descriptor protocol.GroupCarrier
	if err = decodeStrict([]byte(body), &descriptor); err == nil {
		err = descriptor.Validate()
	}
	packet, e := a.GroupContext(conv)
	if err == nil {
		err = e
	}
	if err == nil && sub == envelope.SubGroupContext && (descriptor.Seq != packet.State.Seq || descriptor.Hash != packet.State.Hash()) {
		err = errors.New("group: queued context is no longer current")
	}
	if err == nil {
		if pid != "" {
			err = groupVisitorCarrierDestination(a.store.db, packet, pid, env.To, descriptor.ToKey)
		} else {
			err = groupDeliveryRecipient(a.store.db, packet, env.To, descriptor.ToKey)
		}
	}
	if errors.Is(err, ErrGroupContextPending) || errors.Is(err, errPersonConflict) {
		return true, false, nil
	}
	if err != nil {
		e = a.store.setOutboxState(env.ID, stateNotDelivered, "group recipient no longer eligible", "")
		if e == nil {
			a.releaseSpool(env)
		}
		return true, false, e
	}
	return true, true, nil
}

func (a *Agent) groupCarrierBytes(ctx context.Context, att envelope.Attachment) ([]byte, error) {
	f := FileInfo{BlobID: att.Blob.ID, Name: att.Name, Size: att.Size, SHA256: att.SHA256, ctSize: att.Blob.Size, ctSHA256: att.Blob.SHA256}
	if err := a.fetchCiphertext(ctx, f); err != nil {
		if errors.Is(err, errFileCiphertextIntegrity) {
			return nil, errors.Join(errPermanent, err)
		}
		return nil, err
	}
	size, sum, err := fileDigest(a.downloadPath(f.BlobID))
	if err != nil {
		return nil, err
	}
	if size != att.Blob.Size || sum != att.Blob.SHA256 {
		return nil, errors.Join(errPermanent, errors.New("group: carrier ciphertext differs from signed manifest"))
	}
	src, err := os.Open(a.downloadPath(f.BlobID))
	if err != nil {
		return nil, err
	}
	defer src.Close()
	reader, err := age.Decrypt(src, a.id.Box)
	if err != nil {
		return nil, errors.Join(errPermanent, err)
	}
	data, err := io.ReadAll(io.LimitReader(reader, att.Size+1))
	if err != nil {
		return nil, errors.Join(errPermanent, err)
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != att.Size || hex.EncodeToString(hash[:]) != att.SHA256 {
		return nil, errors.Join(errPermanent, errors.New("group: carrier plaintext digest differs from signed manifest"))
	}
	return data, nil
}

func (a *Agent) admitGroupCarrier(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	if in.PID != "" {
		return a.admitGroupVisitorCarrier(ctx, env, in, root, sender, fromQuarantine, hold)
	}
	var descriptor protocol.GroupCarrier
	if err := decodeStrict([]byte(in.Body), &descriptor); err != nil || descriptor.Validate() != nil || descriptor.ToKey != a.Self().Fingerprint() {
		return hold(reasonInvalid, "group: descriptor targets another exact key")
	}
	data, err := a.groupCarrierBytes(ctx, in.Attachments[0])
	if err != nil {
		var he *HubError
		if retryable(err) || errors.As(err, &he) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	if in.Sub == envelope.SubGroupProof {
		var page protocol.GroupJournalPage
		if err = decodeGroupCarrierJSON(data, &page); err == nil {
			if len(page.Records) == 0 {
				err = errors.New("group: empty proof carrier")
			} else {
				last := page.Records[len(page.Records)-1]
				if last.Seq != descriptor.Seq || last.Hash != descriptor.Hash {
					err = errors.New("group: proof descriptor differs")
				}
			}
		}
		if err == nil {
			err = a.IngestGroupProofPage(ctx, root, protocol.GroupJournalPage{})
		}
		if err == nil {
			var head sql.NullInt64
			err = a.store.db.QueryRow(`SELECT max(seq) FROM group_proof_records WHERE conv=? AND bootstrap=?`, in.Conv, root.Creator.Fingerprint).Scan(&head)
			next := int64(0)
			if head.Valid {
				next = head.Int64 + 1
			}
			if err == nil && page.Records[0].Seq > next {
				err = ErrGroupContextPending
			}
		}
		if err == nil {
			err = a.IngestGroupProofPage(ctx, root, page)
		}
	} else {
		var packet GroupContext
		if err = decodeGroupCarrierJSON(data, &packet); err == nil {
			x, _ := json.Marshal(root)
			y, _ := json.Marshal(packet.Root)
			if !bytes.Equal(x, y) || packet.State.Seq != descriptor.Seq || packet.State.Hash() != descriptor.Hash || len(packet.Proof) != 0 {
				err = errors.New("group: context descriptor/root differs")
			}
		}
		if err == nil {
			_, err = groupProofRecord(a.store.db, in.Conv, root.Creator.Fingerprint, packet.State.Seq)
		}
		if err == nil {
			err = a.acceptGroupContextFrom(ctx, packet, sender)
		}
	}
	if err != nil {
		if errors.Is(err, ErrGroupContextPending) || errors.Is(err, ErrNoPerson) {
			return hold(reasonProof, err.Error())
		}
		var he *HubError
		var ne net.Error
		if errors.As(err, &he) && retryable(err) || errors.As(err, &ne) || errors.Is(err, context.Canceled) {
			return err
		}
		return hold(reasonInvalid, err.Error())
	}
	res, err := a.store.addConvInbox(in, sender.Fingerprint(), "", fromQuarantine, func(tx *sql.Tx) error {
		_, e := tx.Exec(`UPDATE inbox SET read_at=? WHERE id=?`, time.Now().Unix(), in.ID)
		return e
	})
	if err == nil && res == admitConflict {
		return hold(reasonDuplicate, "group: logical carrier conflict")
	}
	if err == nil && res == admitted {
		a.convWork.due(convRetry)
		a.kickNow()
	}
	return err
}

func decodeGroupCarrierJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("group: trailing carrier JSON")
	}
	return nil
}
