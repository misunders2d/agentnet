package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

func (a *Agent) deviceFileSource(q dbq, m fileMsg) (deviceHistoryRow, error) {
	var found deviceHistoryRow
	if !groupFileMessageValid(m) || m.GroupAdmission != "" {
		return found, errors.New("device file: invalid exact manifest")
	}
	for _, storage := range []string{"out", "in"} {
		r, err := a.deviceHistorySource(q, storage, m.LID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return found, err
		}
		if r.item.FromKey != m.Author {
			continue
		}
		if deviceHistoryHash(r) != m.Hash || *m.Index >= len(r.item.Attachments) {
			return found, errors.New("device file: original differs")
		}
		f := r.item.Attachments[*m.Index]
		if f.Name != m.Name || f.Size != *m.Size || f.SHA256 != m.SHA256 {
			return found, errors.New("device file: manifest differs")
		}
		var erased int
		if err = q.QueryRow(`SELECT count(*) FROM conv_erased WHERE conv='' AND key=? AND lid=?`, m.Author, m.LID).Scan(&erased); err != nil {
			return found, err
		}
		if erased != 0 {
			return found, errors.New("device file: original was deleted")
		}
		if found.id != "" && deviceHistoryHash(found) != deviceHistoryHash(r) {
			return found, errors.New("device file: ambiguous original")
		}
		found = r
	}
	if found.id == "" {
		return found, errors.New("device file: original unavailable")
	}
	return found, nil
}

func (a *Agent) deviceFileCarrier(dev identity.Public, m fileMsg, atts ...envelope.Attachment) (outCopy, error) {
	if err := historyRecoveryCurrent(a.store.db, a.Self(), dev); err != nil {
		return outCopy{}, err
	}
	if _, err := a.deviceFileSource(a.store.db, m); err != nil {
		return outCopy{}, err
	}
	own, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return outCopy{}, err
	}
	if !ok {
		return outCopy{}, ErrNoPerson
	}
	raw, _ := json.Marshal(m)
	body, _ := json.Marshal(protocol.DeviceFile{V: 1, Person: own.info.Person, Roster: own.info.Roster, Item: raw})
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDeviceFile, Replica: true, Body: string(body), Attachments: atts}
	key, err := dev.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	env, err := envelope.Seal(in, a.id.Sign, key)
	return outCopy{env: env, in: in, state: stateQueued, required: protocol.CapOwnSyncV3, recipientFP: dev.Fingerprint()}, err
}

func (a *Agent) requestDeviceHistoryFile(ctx context.Context, id string, index int) error {
	r, err := a.deviceHistorySource(a.store.db, "in", id)
	if err != nil {
		return err
	}
	var via string
	if err = a.store.db.QueryRow(`SELECT coalesce(via,'') FROM inbox WHERE id=? AND replica=1 AND EXISTS(SELECT 1 FROM device_history_rows WHERE storage='in' AND id=inbox.id)`, id).Scan(&via); err != nil {
		return err
	}
	files, err := a.store.attachments(id)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(files) {
		return errors.New("device file: missing attachment")
	}
	f := files[index]
	if !strings.HasPrefix(f.BlobID, historyBlob) {
		return nil
	}
	if err = a.refreshRecipientPerson(ctx, via, map[string]error{}); err != nil {
		return err
	}
	dev, err := a.sendKey(ctx, via)
	if err != nil {
		return err
	}
	size := f.Size
	m := fileMsg{V: 1, Type: "request", LID: id, Author: r.item.FromKey, Hash: deviceHistoryHash(r), Index: &index, Name: f.Name, Size: &size, SHA256: f.SHA256}
	c, err := a.deviceFileCarrier(dev, m)
	if err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = historyRecoveryCurrent(tx, a.Self(), dev); err != nil {
		return err
	}
	if _, err = a.deviceFileSource(tx, m); err != nil {
		return err
	}
	var raw []byte
	if e := tx.QueryRow(`SELECT group_descriptor FROM file_requests WHERE message_id=? AND sha256=? AND state='requested'`, id, f.SHA256).Scan(&raw); e == nil {
		var old fileMsg
		if json.Unmarshal(raw, &old) != nil || !groupFileSameTuple(old, m) {
			return errors.New("device file: another manifest is pending")
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if err = insertCopies(tx, []outCopy{c}); err != nil {
		return err
	}
	raw, _ = json.Marshal(m)
	_, err = tx.Exec(`INSERT INTO file_requests(message_id,sha256,state,updated_at,group_descriptor) VALUES(?,?,'requested',?,?) ON CONFLICT(message_id,sha256) DO UPDATE SET state='requested',detail=NULL,updated_at=excluded.updated_at,group_descriptor=excluded.group_descriptor`, id, f.SHA256, time.Now().Unix(), raw)
	if err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.kickNow()
		notifyDaemon(a.home)
	}
	return err
}

func (a *Agent) admitDeviceFile(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	admitted := func(tx *sql.Tx) error { // its hold ends where its receipt is kept
		if held {
			if _, err := tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
				return err
			}
		}
		_, err := tx.Exec(`INSERT OR IGNORE INTO history_receipts(id) VALUES(?)`, env.ID)
		return err
	}
	w, err := protocol.ParseDeviceFile([]byte(in.Body))
	var m fileMsg
	if err != nil || decodeStrict(w.Item, &m) != nil || !groupFileMessageValid(m) || m.GroupAdmission != "" || m.Type == "request" && (m.Available || len(in.Attachments) != 0) || m.Type == "offer" && (m.Available != (len(in.Attachments) == 1)) {
		return hold(reasonInvalid, "device file: malformed carrier")
	}
	if err = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = readSyncAuthority(tx, protocol.ReadSync{Person: w.Person, Roster: w.Roster}, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	r, err := a.deviceFileSource(tx, m)
	if err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	now := time.Now().Unix()
	if m.Type == "request" {
		descriptor, _ := json.Marshal(groupFileServe{Message: m, Key: sender.Fingerprint()})
		_, err = tx.Exec(`INSERT OR IGNORE INTO file_serves(id,device,conv,lid,sha256,state,updated_at,group_descriptor) VALUES(?,?,'',?,?,'pending',?,?)`, env.ID, env.From, m.LID, m.SHA256, now, descriptor)
	} else {
		if r.storage != "in" {
			tx.Rollback()
			return hold(reasonInvalid, "device file: offer does not name received history")
		}
		if m.Available {
			f := in.Attachments[0]
			if f.Name != m.Name || f.Size != *m.Size || f.SHA256 != m.SHA256 {
				tx.Rollback()
				return hold(reasonInvalid, "device file: offered bytes differ from manifest")
			}
		}
		var raw []byte
		err = tx.QueryRow(`SELECT group_descriptor FROM file_requests WHERE message_id=? AND sha256=? AND state='requested'`, m.LID, m.SHA256).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			var blob string
			err = tx.QueryRow(`SELECT blob_id FROM attachments WHERE message_id=? ORDER BY rowid LIMIT 1 OFFSET ?`, m.LID, *m.Index).Scan(&blob)
			if err != nil || blob == "" || strings.HasPrefix(blob, historyBlob) {
				tx.Rollback()
				return hold(reasonInvalid, "device file: no exact pending request or stored file")
			}
			// A reissued offer may acknowledge bytes already stored, without
			// replacing them with unsolicited ciphertext.
			if err = admitted(tx); err != nil {
				return err
			}
			return a.store.done(tx.Commit())
		}
		if err != nil {
			return err
		}
		var request fileMsg
		if json.Unmarshal(raw, &request) != nil || !groupFileSameTuple(request, m) {
			tx.Rollback()
			return hold(reasonInvalid, "device file: offer differs from requested manifest")
		}
		if m.Available {
			f := in.Attachments[0]
			_, err = tx.Exec(`UPDATE attachments SET blob_id=?,ct_size=?,ct_sha256=? WHERE message_id=? AND rowid=(SELECT rowid FROM attachments WHERE message_id=? ORDER BY rowid LIMIT 1 OFFSET ?) AND blob_id LIKE ?`, f.Blob.ID, f.Blob.Size, f.Blob.SHA256, m.LID, m.LID, *m.Index, historyBlob+"%")
			if err == nil {
				_, err = tx.Exec(`DELETE FROM file_requests WHERE message_id=? AND sha256=?`, m.LID, m.SHA256)
			}
		} else {
			_, err = tx.Exec(`UPDATE file_requests SET state='unavailable',detail=?,updated_at=? WHERE message_id=? AND sha256=?`, m.Detail, now, m.LID, m.SHA256)
		}
	}
	if err != nil {
		return err
	}
	if err = admitted(tx); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.convWork.due(convServe)
		a.kickNow()
	}
	return err
}

func (a *Agent) serveDeviceHistoryFile(ctx context.Context, id, device string, job groupFileServe) error {
	own, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	dev, has := own.device(device)
	if !ok || !has || dev.Fingerprint() != job.Key {
		return errors.Join(errPermanent, errors.New("device file: requester changed"))
	}
	if err = historyRecoveryCurrent(a.store.db, a.Self(), dev); err != nil {
		return errors.Join(errPermanent, err)
	}
	r, err := a.deviceFileSource(a.store.db, job.Message)
	if err != nil {
		return errors.Join(errPermanent, err)
	}
	dir := filepath.Join(a.home, "opened")
	if err = secfile.EnsureDir(dir); err != nil {
		return err
	}
	var plain string
	if r.storage == "out" {
		plain, err = a.openKept(dir, job.Message.SHA256, *job.Message.Size)
	} else {
		files, e := a.store.attachments(r.id)
		err = e
		if err == nil {
			f := files[*job.Message.Index]
			if strings.HasPrefix(f.BlobID, historyBlob) {
				err = errNotKept
			} else if err = a.fetchCiphertext(ctx, f); err == nil {
				plain, err = a.decryptTo(dir, f)
			}
		}
	}
	if retryable(err) {
		return err
	}
	m := job.Message
	m.Type = "offer"
	m.Available = err == nil
	var atts []envelope.Attachment
	release, e := lockfile.Wait(a.spoolLockPath())
	if e != nil {
		return e
	}
	defer release()
	committed := false
	defer func() {
		if !committed {
			for _, f := range atts {
				a.releaseSpool(envelope.Envelope{Blobs: []envelope.Blob{f.Blob}})
			}
		}
	}()
	if err == nil {
		defer os.Remove(plain)
		recipient, e := dev.Recipient()
		if e != nil {
			return e
		}
		f, e := a.spoolNamed(OutgoingFile{Name: m.Name, Path: plain}, recipient)
		if e != nil {
			return e
		}
		atts = []envelope.Attachment{f}
		if f.SHA256 != m.SHA256 || f.Size != *m.Size {
			return errors.Join(errPermanent, errors.New("device file: kept bytes differ"))
		}
	} else {
		m.Detail = "This device no longer holds the original file."
	}
	c, err := a.deviceFileCarrier(dev, m, atts...)
	if err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = historyRecoveryCurrent(tx, a.Self(), dev); err != nil {
		return err
	}
	if _, err = a.deviceFileSource(tx, m); err != nil {
		return err
	}
	if err = insertCopies(tx, []outCopy{c}); err != nil {
		return err
	}
	for _, f := range c.in.Attachments {
		if _, err = tx.Exec(`INSERT INTO sent_attachments(message_id,blob_id,name,size,sha256) VALUES(?,?,?,?,?)`, c.env.ID, f.Blob.ID, f.Name, f.Size, f.SHA256); err != nil {
			return err
		}
	}
	for _, b := range c.env.Blobs {
		if _, err = tx.Exec(`INSERT INTO uploads(blob_id,message_id,state) VALUES(?,?,?)`, b.ID, c.env.ID, protocol.BlobUploading); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE file_serves SET state='served',updated_at=? WHERE id=?`, time.Now().Unix(), id); err != nil {
		return err
	}
	err = tx.Commit()
	committed = err == nil
	return a.store.done(err)
}
