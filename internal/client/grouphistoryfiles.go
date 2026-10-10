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

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// This local descriptor augments the existing file_serves job. The key comes
// from signature verification, never from a requester-supplied JSON field.
type groupFileServe struct {
	Message fileMsg `json:"message"`
	Key     string  `json:"key"`
}

func groupFileSameTuple(a, b fileMsg) bool {
	return a.LID == b.LID && a.Author == b.Author && a.Hash == b.Hash && a.SHA256 == b.SHA256 && a.Name == b.Name && a.Index != nil && b.Index != nil && *a.Index == *b.Index && a.Size != nil && b.Size != nil && *a.Size == *b.Size && a.GroupAdmission == b.GroupAdmission
}

func groupFileMessageValid(m fileMsg) bool {
	return m.V == 1 && (m.Type == "request" || m.Type == "offer") && protocol.ValidID(m.LID) && protocol.ValidFingerprint(m.Author) && protocol.ValidHash(m.Hash) && protocol.ValidHash(m.SHA256) && m.Index != nil && *m.Index >= 0 && *m.Index < envelope.MaxAttachments && m.Name != "" && m.Size != nil && *m.Size >= 0 && *m.Size <= MaxFileSize && (m.GroupAdmission == "" || protocol.ValidHash(m.GroupAdmission))
}
func groupFileRef(m fileMsg) protocol.GroupHistoryRef {
	return protocol.GroupHistoryRef{LID: m.LID, Author: m.Author, Hash: m.Hash}
}

func (a *Agent) groupFileSource(q dbq, conv string, m fileMsg, original ...bool) (groupHistorySource, error) {
	if !groupFileMessageValid(m) {
		return groupHistorySource{}, errors.Join(errPermanent, errors.New("group: malformed exact file grant"))
	}
	var source groupHistorySource
	var err error
	if len(original) > 0 && original[0] {
		source, err = a.groupHistorySourceIn(q, conv, groupFileRef(m), true)
		if err != nil && !errors.Is(err, ErrGroupHistoryUnavailable) {
			return source, err
		}
	}
	if !source.original || source.stamp != m.GroupAdmission {
		source, err = a.groupFileSourceIn(q, conv, groupFileRef(m))
	}
	if err != nil {
		return source, err
	}
	if *m.Index >= len(source.item.Attachments) {
		return source, errors.Join(errPermanent, errors.New("group: selected file index absent"))
	}
	f := source.item.Attachments[*m.Index]
	if f.Name != m.Name || f.Size != *m.Size || f.SHA256 != m.SHA256 {
		return source, errors.Join(errPermanent, errors.New("group: selected manifest differs"))
	}
	return source, nil
}

func (a *Agent) groupFileAuthorized(q dbq, packet GroupContext, address, fp string, m fileMsg) error {
	_, err := a.groupFileAuthorizedSource(q, packet, address, fp, m)
	return err
}

// Resolve immutable originals only after current own-human and admission checks.
// Returning this exact source keeps subsequent byte reads on the same projection.
func (a *Agent) groupFileAuthorizedSource(q dbq, packet GroupContext, address, fp string, m fileMsg) (groupHistorySource, error) {
	var source groupHistorySource
	if !groupFileMessageValid(m) {
		return source, errors.Join(errPermanent, errors.New("group: file lacks exact message/manifest binding"))
	}
	admission, err := groupMemberAdmission(q, packet, address, fp)
	if err != nil {
		return source, err
	}
	if m.GroupAdmission == "" || m.GroupAdmission != admission.Hash() {
		return source, errors.Join(errPermanent, errors.New("group: file requester admission changed"))
	}
	own, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return source, err
	}
	original := false
	if ok && own.has(address, fp) && own.roster.Human(fp) && own.roster.Human(a.Self().Fingerprint()) {
		current, e := groupMemberAdmission(q, packet, a.Address, a.Self().Fingerprint())
		if e != nil {
			return source, e
		}
		dev, _ := own.device(address)
		if e = historyRecoveryCurrent(q, a.Self(), dev); e != nil {
			return source, e
		}
		original = current.Hash() == m.GroupAdmission
	}
	source, err = a.groupFileSource(q, packet.State.Conv, m, original)
	if err != nil {
		return source, err
	}
	if source.original || source.item.PID != "" {
		if !ok || !own.has(address, fp) || source.stamp != admission.Hash() {
			return source, errors.Join(errPermanent, errors.New("group: original files require exact current own-linked history"))
		}
		if source.original || source.item.GroupHistory != nil {
			dev, _ := own.device(address)
			if err = historyRecoveryCurrent(q, a.Self(), dev); err != nil {
				return source, err
			}
		}
		return source, nil
	}
	if admission.AllowsHistory(groupFileRef(m)) {
		return source, nil
	}
	if !ok || !own.has(address, fp) {
		return source, errors.Join(errPermanent, errors.New("group: file is not selected/current own live history"))
	}
	if source.stamp != m.GroupAdmission {
		return source, errors.Join(errPermanent, ErrGroupHistoryUnavailable)
	}
	return source, nil
}

func (a *Agent) groupFileCarrier(to identity.Public, packet GroupContext, m fileMsg, atts ...envelope.Attachment) (outCopy, error) {
	if err := groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return outCopy{}, err
	}
	if err := groupTurnCheck(a.store.db, packet, to.Address, to.Fingerprint()); err != nil {
		return outCopy{}, err
	}
	requester, key := a.Address, a.Self().Fingerprint()
	if m.Type == "offer" {
		requester, key = to.Address, to.Fingerprint()
	}
	if err := a.groupFileAuthorized(a.store.db, packet, requester, key, m); err != nil {
		return outCopy{}, err
	}
	recipient, err := to.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	raw, _ := json.Marshal(packet.Root)
	body, _ := json.Marshal(m)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: a.Address, To: to.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: packet.State.Conv, Root: raw, Body: string(body), Replica: true, Sub: envelope.SubFile, Attachments: atts}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	return outCopy{env: env, in: in, state: stateQueued, required: protocol.CapGroup, recipientFP: to.Fingerprint()}, err
}

func (a *Agent) requestGroupHistoryFile(ctx context.Context, msgID string, index int) error {
	var conv, lid, author, via, stamp string
	if err := a.store.db.QueryRow(`SELECT conv,lid,coalesce(verified_by,claimed_fp,''),coalesce(via,''),coalesce(group_admission,'') FROM inbox WHERE id=?`, msgID).Scan(&conv, &lid, &author, &via, &stamp); err != nil {
		return err
	}
	if via == "" {
		return errors.New("group: file is not forwarded history")
	}
	files, err := a.store.attachments(msgID)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(files) {
		return errors.New("group: no selected file index")
	}
	f := files[index]
	if !strings.HasPrefix(f.BlobID, historyBlob) {
		return nil
	}
	packet, err := a.GroupContext(conv)
	if err != nil {
		return err
	}
	admission, err := groupMemberAdmission(a.store.db, packet, a.Address, a.Self().Fingerprint())
	if err != nil {
		return err
	}
	var sources []groupHistorySource
	if stamp == admission.Hash() {
		own, ok, e := a.store.selfPerson(a.Address)
		if e != nil {
			return e
		}
		dev, found := own.device(via)
		if ok && found && own.roster.Human(a.Self().Fingerprint()) && own.roster.Human(dev.Fingerprint()) {
			if e = historyRecoveryCurrent(a.store.db, a.Self(), dev); e != nil {
				return e
			}
			sources, err = a.groupHistorySources(a.store.db, conv, lid, author, 0, 2, true)
			if err != nil {
				return err
			}
		}
	}
	if len(sources) == 0 {
		sources, err = a.groupFileSources(a.store.db, conv, lid, author, 2)
	}
	if err != nil {
		return err
	}
	if len(sources) != 1 || sources[0].item.ID != msgID {
		return errors.New("group: ambiguous history file source")
	}
	ref := historyRef(conv, sources[0].item)
	size := f.Size
	m := fileMsg{V: 1, Type: "request", LID: lid, SHA256: f.SHA256, Author: author, Hash: ref.Hash, Index: &index, Name: f.Name, Size: &size, GroupAdmission: stamp}
	m.GroupAdmission = admission.Hash()
	for _, member := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, member.Person, false); err != nil {
			return err
		}
	}
	key, err := a.sendKey(ctx, via)
	if err != nil {
		return err
	}
	c, err := a.groupFileCarrier(key, packet, m)
	if err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = a.groupFileAuthorized(tx, packet, a.Address, a.Self().Fingerprint(), m); err != nil {
		return err
	}
	if err = groupTurnCheck(tx, packet, via, key.Fingerprint()); err != nil {
		return err
	}
	var pending []byte
	if e := tx.QueryRow(`SELECT group_descriptor FROM file_requests WHERE message_id=? AND sha256=? AND state='requested'`, msgID, f.SHA256).Scan(&pending); e == nil {
		var previous fileMsg
		if json.Unmarshal(pending, &previous) != nil || !groupFileSameTuple(previous, m) {
			return errors.New("group: another exact file index is pending; finish it before requesting this one")
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if err = insertCopies(tx, []outCopy{c}); err != nil {
		return err
	}
	descriptor, _ := json.Marshal(m)
	if _, err = tx.Exec(`INSERT INTO file_requests(message_id,sha256,state,updated_at,group_descriptor)VALUES(?,?,'requested',?,?) ON CONFLICT(message_id,sha256)DO UPDATE SET state='requested',detail=NULL,updated_at=excluded.updated_at,group_descriptor=excluded.group_descriptor`, msgID, f.SHA256, time.Now().Unix(), descriptor); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.kickNow()
	notifyDaemon(a.home)
	return nil
}

// admitGroupFile takes a group history file request or offer from an own
// linked device. Each, a stale offer included, is receipted in the step that
// admits it: it stores no inbox row (MIXED-1).
func (a *Agent) admitGroupFile(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, held bool, hold func(string, string) error) error {
	admitted := func(tx *sql.Tx) error {
		if held {
			if _, err := tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
				return err
			}
		}
		return receiptCarrier(tx, env.ID)
	}
	var m fileMsg
	if !in.Replica || decodeStrict([]byte(in.Body), &m) != nil || !groupFileMessageValid(m) {
		return hold(reasonInvalid, "group: file carrier lacks exact selected manifest")
	}
	packet, err := a.GroupContext(in.Conv)
	if err != nil {
		return hold(reasonProof, err.Error())
	}
	if !sameGroupRoot(root, packet.Root) {
		return hold(reasonInvalid, "group: file root differs")
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
		if e = groupTurnCheck(q, current, sender.Address, sender.Fingerprint()); e != nil {
			return e
		}
		address, key := sender.Address, sender.Fingerprint()
		if m.Type == "offer" {
			address, key = a.Address, a.Self().Fingerprint()
		}
		if e = a.groupFileAuthorized(q, current, address, key, m); e != nil {
			return e
		}
		return nil
	}
	if err = check(a.store.db); err != nil {
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, err.Error())
		}
		return hold(reasonInvalid, err.Error())
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = check(tx); err != nil {
		return err
	}
	if m.Type == "request" {
		if len(in.Attachments) != 0 {
			return hold(reasonInvalid, "group: request carries file bytes")
		}
		descriptor, _ := json.Marshal(groupFileServe{Message: m, Key: sender.Fingerprint()})
		if _, err = tx.Exec(`INSERT OR IGNORE INTO file_serves(id,device,conv,lid,sha256,state,updated_at,group_descriptor)VALUES(?,?,?,?,?,'pending',?,?)`, env.ID, env.From, in.Conv, m.LID, m.SHA256, time.Now().Unix(), descriptor); err != nil {
			return err
		}
		if err = admitted(tx); err != nil {
			return err
		}
		if err = a.store.done(tx.Commit()); err != nil {
			return err
		}
		a.convWork.due(convServe)
		a.kickNow()
		return nil
	}
	source, err := a.groupFileAuthorizedSource(tx, packet, a.Address, a.Self().Fingerprint(), m)
	if err != nil {
		return err
	}
	var via string
	if err = tx.QueryRow(`SELECT coalesce(via,'') FROM inbox WHERE id=?`, source.item.ID).Scan(&via); err != nil {
		return err
	}
	if via != sender.Address {
		return hold(reasonInvalid, "group: file offer from another forwarder")
	}
	var requested int
	if err = tx.QueryRow(`SELECT count(*) FROM file_requests WHERE message_id=? AND sha256=? AND state='requested'`, source.item.ID, m.SHA256).Scan(&requested); err != nil {
		return err
	}
	if requested == 0 { // not waiting for it (any more): only its receipt
		if err = admitted(tx); err != nil {
			return err
		}
		return a.store.done(tx.Commit())
	}
	var requestedDescriptor []byte
	if err = tx.QueryRow(`SELECT group_descriptor FROM file_requests WHERE message_id=? AND sha256=?`, source.item.ID, m.SHA256).Scan(&requestedDescriptor); err != nil {
		return err
	}
	var originalRequest fileMsg
	if json.Unmarshal(requestedDescriptor, &originalRequest) != nil || !groupFileSameTuple(originalRequest, m) {
		return hold(reasonInvalid, "group: offer does not match exact pending file request")
	}
	if !m.Available {
		if len(in.Attachments) != 0 {
			return hold(reasonInvalid, "group: unavailable offer has bytes")
		}
		_, err = tx.Exec(`UPDATE file_requests SET state='unavailable',detail=?,updated_at=? WHERE message_id=? AND sha256=?`, m.Detail, time.Now().Unix(), source.item.ID, m.SHA256)
	} else {
		if len(in.Attachments) != 1 || in.Attachments[0].Name != m.Name || in.Attachments[0].Size != *m.Size || in.Attachments[0].SHA256 != m.SHA256 {
			return hold(reasonInvalid, "group: offered file differs from selected manifest")
		}
		att := in.Attachments[0]
		_, err = tx.Exec(`UPDATE attachments SET blob_id=?,ct_size=?,ct_sha256=? WHERE rowid=(SELECT rowid FROM attachments WHERE message_id=? ORDER BY rowid LIMIT 1 OFFSET ?) AND name=? AND size=? AND sha256=? AND blob_id LIKE ?`, att.Blob.ID, att.Blob.Size, att.Blob.SHA256, source.item.ID, *m.Index, m.Name, *m.Size, m.SHA256, historyBlob+"%")
		if err == nil {
			_, err = tx.Exec(`DELETE FROM file_requests WHERE message_id=? AND sha256=?`, source.item.ID, m.SHA256)
		}
	}
	if err == nil {
		err = admitted(tx)
	}
	if err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.convWork.due(convFetch)
	a.kickNow()
	return nil
}

func (a *Agent) groupFilePlain(ctx context.Context, source groupHistorySource, m fileMsg) (string, error) {
	dir := filepath.Join(a.home, "opened")
	if err := secfile.EnsureDir(dir); err != nil {
		return "", err
	}
	if source.dir == "out" {
		return a.openKept(dir, m.SHA256, *m.Size)
	}
	files, err := a.store.attachments(source.item.ID)
	if err != nil {
		return "", err
	}
	if *m.Index >= len(files) || strings.HasPrefix(files[*m.Index].BlobID, historyBlob) {
		return "", errors.Join(errPermanent, errors.New("group: this device has no file bytes"))
	}
	f := files[*m.Index]
	if err = a.fetchCiphertext(ctx, f); err != nil {
		return "", err
	}
	return a.decryptTo(dir, f)
}

func (a *Agent) serveGroupHistoryFile(ctx context.Context, id, device, conv string, job groupFileServe) error {
	packet, err := a.GroupContext(conv)
	if err != nil {
		return err
	}
	for _, member := range packet.State.Members {
		if _, err = a.refreshPerson(ctx, member.Person, false); err != nil {
			return err
		}
	}
	if err = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return errors.Join(errPermanent, err)
	}
	source, err := a.groupFileAuthorizedSource(a.store.db, packet, device, job.Key, job.Message)
	if err != nil {
		return errors.Join(errPermanent, err)
	}
	key, err := a.sendKey(ctx, device)
	if err != nil {
		return err
	}
	if key.Fingerprint() != job.Key {
		return errors.Join(errPermanent, errors.New("group: asking key changed"))
	}
	plain, err := a.groupFilePlain(ctx, source, job.Message)
	m := job.Message
	m.Type = "offer"
	m.Available = err == nil
	if err != nil && !errors.Is(err, errPermanent) && !errors.Is(err, errFileCiphertextIntegrity) {
		return err
	}
	var atts []envelope.Attachment
	release, e := lockfile.Wait(a.spoolLockPath())
	if e != nil {
		return e
	}
	defer release()
	if err == nil {
		defer os.Remove(plain)
		recipient, e := key.Recipient()
		if e != nil {
			return e
		}
		att, e := a.spoolNamed(OutgoingFile{Name: m.Name, Path: plain}, recipient)
		if e != nil {
			return e
		}
		if att.SHA256 != m.SHA256 || att.Size != *m.Size {
			a.releaseSpool(envelope.Envelope{Blobs: []envelope.Blob{att.Blob}})
			return errors.Join(errPermanent, errors.New("group: file bytes no longer match selection"))
		}
		atts = []envelope.Attachment{att}
	} else {
		m.Detail = "This device no longer holds the selected file."
	}
	copy, err := a.groupFileCarrier(key, packet, m, atts...)
	if err != nil {
		a.releaseSpool(envelope.Envelope{Blobs: func() []envelope.Blob {
			var bs []envelope.Blob
			for _, att := range atts {
				bs = append(bs, att.Blob)
			}
			return bs
		}()})
		return err
	}
	stored := false
	defer func() {
		if !stored {
			a.releaseSpool(copy.env)
		}
	}()
	err = a.store.addConvOutbox([]outCopy{copy}, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		if e := groupTurnCheck(tx, packet, a.Address, a.Self().Fingerprint()); e != nil {
			return e
		}
		if e := a.groupFileAuthorized(tx, packet, device, job.Key, m); e != nil {
			return e
		}
		var saved []byte
		if e := tx.QueryRow(`SELECT group_descriptor FROM file_serves WHERE id=? AND state='pending'`, id).Scan(&saved); e != nil {
			return e
		}
		var current groupFileServe
		if json.Unmarshal(saved, &current) != nil || current.Key != job.Key || !groupFileSameTuple(current.Message, job.Message) {
			return errors.New("group: file serve binding changed")
		}
		state := "served"
		if !m.Available {
			state = "unavailable"
		}
		_, e := tx.Exec(`UPDATE file_serves SET state=?,updated_at=? WHERE id=?`, state, time.Now().Unix(), id)
		return e
	}, "")
	if err != nil {
		return err
	}
	stored = true
	a.kickNow()
	return nil
}

func (a *Agent) groupFileOutboundCheck(q dbq, packet GroupContext, to, fp string, m fileMsg) error {
	requester, key := a.Address, a.Self().Fingerprint()
	if m.Type == "offer" {
		requester, key = to, fp
	}
	source, err := a.groupFileAuthorizedSource(q, packet, requester, key, m)
	if err != nil {
		return err
	}
	if m.Type == "request" {
		var via string
		if err := q.QueryRow(`SELECT coalesce(via,'') FROM inbox WHERE id=?`, source.item.ID).Scan(&via); err != nil {
			return err
		}
		if via != to {
			return fmt.Errorf("group: request no longer names its exact forwarder")
		}
	}
	return nil
}
