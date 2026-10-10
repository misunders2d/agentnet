package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

const archiveStaged = "archive_staged"
const archiveAccepted = "archive_accepted" // ciphertext retained; child import is not proven
const archiveUploading = "archive_uploading"

type isolatedArchivePackError struct{ error }

const historyArchiveSchema = `
CREATE TABLE history_archive_entries(child TEXT PRIMARY KEY,manifest TEXT NOT NULL);
CREATE INDEX history_archive_manifest ON history_archive_entries(manifest);
CREATE TABLE history_archive_jobs(id TEXT PRIMARY KEY,envelope TEXT NOT NULL,source_fp TEXT NOT NULL,retained INTEGER NOT NULL DEFAULT 0,pos INTEGER NOT NULL DEFAULT 0,done INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '');
CREATE INDEX history_archive_jobs_pending ON history_archive_jobs(done,error);
CREATE TABLE history_archive_children(id TEXT PRIMARY KEY,manifest TEXT NOT NULL,wire_sha256 TEXT NOT NULL);
CREATE TABLE history_archive_export_errors(recipient TEXT NOT NULL,recipient_fp TEXT NOT NULL,error TEXT NOT NULL,PRIMARY KEY(recipient,recipient_fp));
CREATE INDEX outbox_pending_exact ON outbox(state,recipient,recipient_fp,sub);
ALTER TABLE outbox ADD COLUMN replication_live INTEGER NOT NULL DEFAULT 0;
-- Reannounce only existing inert custody envelopes, with their original signed
-- bytes and IDs, so a pre-lane relay moves them out of its live FIFO. Late
-- terminal receipts still advance queued rows; no original is recreated.
UPDATE outbox SET state='queued' WHERE state='custody' AND sub IN ('history','device-history','read-sync','root-sync','topic-sync','topic-state-sync','model-sync','invitation-sync');
`

func (a *Agent) archivesFor(ctx context.Context, dev identity.Public) bool {
	return historyRecoveryCurrent(a.store.db, a.Self(), dev) == nil && a.requireParticipationCaps(ctx, dev, protocol.CapHistoryArchive) == nil
}

func stageArchiveCopies(copies []outCopy) {
	for i := range copies {
		copies[i].state = archiveStaged
	}
}

func (a *Agent) releaseAcceptedArchiveSpool(manifest string) error {
	rows, err := a.store.db.Query(`SELECT o.envelope FROM history_archive_entries e JOIN outbox o ON o.id=e.child WHERE e.manifest=? AND o.state=? LIMIT ?`, manifest, archiveAccepted, protocol.MaxHistoryArchiveEntries)
	if err != nil {
		return err
	}
	var children []envelope.Envelope
	for rows.Next() {
		var raw string
		var child envelope.Envelope
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &child); err != nil {
			break
		}
		children = append(children, child)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	for _, child := range children {
		a.releaseSpool(child)
	}
	return nil
}

// archiveStep rides on existing daemon wakes, outside the independent live
// sender. One durable upload/download/import step, never a timer or poller.
func (a *Agent) archiveStep(ctx context.Context) (more bool, err error) {
	a.archiveOnce.Do(func() { a.archiveLock = make(chan struct{}, 1) })
	select {
	case a.archiveLock <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-a.archiveLock }()
	// Most existing daemon wakes have no archive export work. Use the
	// pending-state index before preparing the recipient/copy join queries.
	var pending bool
	if err = a.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox WHERE state IN (?,?) LIMIT 1)`, archiveStaged, archiveUploading).Scan(&pending); err != nil {
		return false, err
	}
	if !pending {
		return false, nil
	}
	packErr := a.archivePack(ctx)
	var raw, recipientFP string
	err = a.store.db.QueryRow(`SELECT envelope,recipient_fp FROM outbox o WHERE sub=? AND state=? AND NOT EXISTS(SELECT 1 FROM history_archive_export_errors x WHERE x.recipient=o.recipient AND x.recipient_fp=o.recipient_fp) ORDER BY rowid LIMIT 1`, envelope.SubHistoryArchive, archiveUploading).Scan(&raw, &recipientFP)
	if err == nil {
		var env envelope.Envelope
		if err = json.Unmarshal([]byte(raw), &env); err != nil {
			return false, err
		}
		defer func() {
			if err != nil && !more && ctx.Err() == nil {
				if _, e := a.store.db.Exec(`INSERT OR REPLACE INTO history_archive_export_errors(recipient,recipient_fp,error) VALUES(?,?,?)`, env.To, recipientFP, err.Error()); e != nil {
					more, err = false, e
				} else {
					more = true
				}
			}
		}()
		dev, _, found, e := a.store.peer(env.To)
		if e != nil {
			return false, e
		}
		if !found {
			entry, e := a.directory(ctx, env.To)
			if e != nil {
				return false, e
			}
			dev = entry.Public
		}
		if dev.Fingerprint() != recipientFP {
			return false, errors.New("archive recipient key changed")
		}
		if err = historyRecoveryCurrent(a.store.db, a.Self(), dev); err != nil {
			return false, err
		}
		if err = a.uploadAll(ctx, env); err != nil {
			return false, err
		}
		// Only the already-uploaded small descriptor becomes sendable.
		current, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return false, e
		}
		if !found {
			person, exists, e := a.store.selfPerson(a.Address)
			if e != nil {
				return false, e
			}
			if exists {
				current, found = person.device(env.To)
			}
		}
		if !found || pending != nil || current.Fingerprint() != recipientFP {
			return false, errors.New("archive recipient key changed during upload")
		}
		if err = historyRecoveryCurrent(a.store.db, a.Self(), current); err != nil {
			return false, err
		}
		result, e := a.store.db.Exec(`UPDATE outbox SET state=? WHERE id=? AND state=? AND recipient_fp=?`, stateQueued, env.ID, archiveUploading, recipientFP)
		err = e
		if err != nil {
			return false, err
		}
		if changed, e := result.RowsAffected(); e != nil || changed != 1 {
			return false, fmt.Errorf("archive upload transition changed %d rows: %v", changed, e)
		}
		a.NoteChange()
		a.kickNow()
		more = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var isolated *isolatedArchivePackError
	return more || errors.As(packErr, &isolated), packErr
}

// A staged child is already exact signed ciphertext, committed with its
// source cursor. A crash before packing discovers it here; no source is lost.
func (a *Agent) archivePack(ctx context.Context) (err error) {
	rows, err := a.store.db.Query(`SELECT o.envelope,o.recipient_fp FROM outbox o LEFT JOIN history_archive_entries e ON e.child=o.id LEFT JOIN outbox m ON m.id=e.manifest WHERE o.state=? AND (e.child IS NULL OR m.state IN ('expired','failed','not_delivered')) AND NOT EXISTS(SELECT 1 FROM history_archive_export_errors x WHERE x.recipient=o.recipient AND x.recipient_fp=o.recipient_fp) AND NOT EXISTS(SELECT 1 FROM outbox live WHERE live.recipient=o.recipient AND live.recipient_fp=o.recipient_fp AND live.sub=? AND live.state IN ('archive_uploading','queued','waiting','custody')) ORDER BY o.rowid LIMIT ?`, archiveStaged, envelope.SubHistoryArchive, protocol.MaxHistoryArchiveEntries)
	if err != nil {
		return err
	}
	type selected struct {
		env envelope.Envelope
		fp  string
	}
	var selectedRows []selected
	for rows.Next() {
		var raw string
		var r selected
		if err = rows.Scan(&raw, &r.fp); err != nil {
			break
		}
		if err = json.Unmarshal([]byte(raw), &r.env); err != nil {
			break
		}
		selectedRows = append(selectedRows, r)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if len(selectedRows) == 0 {
		return nil
	}
	first := selectedRows[0]
	defer func() {
		if err != nil && ctx.Err() == nil {
			if _, e := a.store.db.Exec(`INSERT OR REPLACE INTO history_archive_export_errors(recipient,recipient_fp,error) VALUES(?,?,?)`, first.env.To, first.fp, err.Error()); e != nil {
				err = e
			} else {
				err = &isolatedArchivePackError{err}
			}
		}
	}()
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("archive source has no verified person")
	}
	dev, ok := me.device(first.env.To)
	if !ok || dev.Fingerprint() != first.fp {
		return errors.New("archive recipient is no longer this exact own device")
	}
	if err = historyRecoveryCurrent(a.store.db, a.Self(), dev); err != nil {
		return err
	}
	chunk := envelope.HistoryArchiveChunk{V: 1}
	chunkBytes := len(`{"v":1,"entries":[]}`)
	for _, r := range selectedRows {
		if r.env.To != dev.Address || r.fp != dev.Fingerprint() {
			break
		}
		entry := envelope.HistoryArchiveEntry{Envelope: r.env, Blobs: []envelope.HistoryArchiveBlob{}}
		for _, blob := range r.env.Blobs {
			if blob.Size > protocol.MaxHistoryArchivePlaintext {
				return errors.New("archive structural blob exceeds chunk bound")
			}
			ct, e := os.ReadFile(a.spoolPath(blob.ID))
			if e != nil {
				return fmt.Errorf("archive structural ciphertext unavailable: %w", e)
			}
			entry.Blobs = append(entry.Blobs, envelope.HistoryArchiveBlob{ID: blob.ID, CT: ct})
		}
		encoded, e := json.Marshal(entry)
		if e != nil {
			return e
		}
		n := len(encoded)
		if len(chunk.Entries) > 0 {
			n++
		}
		if chunkBytes+n > protocol.MaxHistoryArchivePlaintext {
			if len(chunk.Entries) == 0 {
				return fmt.Errorf("archive record %s exceeds the bounded chunk", r.env.ID)
			}
			break
		}
		chunkBytes += n
		chunk.Entries = append(chunk.Entries, entry)
	}
	data, err := envelope.MarshalHistoryArchiveChunk(chunk)
	if err != nil {
		return err
	}
	recipient, err := dev.Recipient()
	if err != nil {
		return err
	}
	att, err := a.spoolReader("history.age.json", bytes.NewReader(data), recipient)
	if err != nil {
		return err
	}
	kept := false
	defer func() {
		if !kept {
			os.Remove(a.spoolPath(att.Blob.ID))
		}
	}()
	manifest, err := json.Marshal(protocol.HistoryArchive{V: 1, Person: me.info.Person, Roster: me.info.Roster, Count: len(chunk.Entries), Format: protocol.HistoryArchiveFormat})
	if err != nil {
		return err
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubHistoryArchive, Replica: true, Body: string(manifest), Attachments: []envelope.Attachment{att}}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
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
	for _, entry := range chunk.Entries {
		var raw string
		if err = tx.QueryRow(`SELECT envelope FROM outbox WHERE id=? AND state=? AND recipient=? AND recipient_fp=?`, entry.Envelope.ID, archiveStaged, dev.Address, dev.Fingerprint()).Scan(&raw); err != nil {
			return err
		}
		want, _ := json.Marshal(entry.Envelope)
		var stored envelope.Envelope
		if json.Unmarshal([]byte(raw), &stored) != nil {
			return errors.New("archive staged ciphertext changed")
		}
		actual, _ := json.Marshal(stored)
		if string(actual) != string(want) {
			return errors.New("archive staged ciphertext changed")
		}
	}
	if err = insertCopies(tx, []outCopy{{env: env, in: in, state: archiveUploading, required: protocol.CapHistoryArchive, recipientFP: dev.Fingerprint()}}); err != nil {
		return err
	}
	// This descriptor is deliberately rootless. Existing copy insertion stores
	// empty conversation strings; the generic delivery gate requires NULL.
	if _, err = tx.Exec(`UPDATE outbox SET conv=NULL,lid=NULL WHERE id=?`, env.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO sent_attachments(message_id,blob_id,name,size,sha256) VALUES(?,?,?,?,?)`, env.ID, att.Blob.ID, att.Name, att.Size, att.SHA256); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO uploads(blob_id,message_id,state) VALUES(?,?,?)`, att.Blob.ID, env.ID, protocol.BlobUploading); err != nil {
		return err
	}
	for _, entry := range chunk.Entries {
		if _, err = tx.Exec(`INSERT INTO history_archive_entries(child,manifest) VALUES(?,?) ON CONFLICT(child) DO UPDATE SET manifest=excluded.manifest`, entry.Envelope.ID, env.ID); err != nil {
			return err
		}
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	kept = true
	return nil
}

func (a *Agent) archiveAuthority(ctx context.Context, env envelope.Envelope, in envelope.Inner, source identity.Public) (protocol.HistoryArchive, error) {
	manifest, err := protocol.ParseHistoryArchive([]byte(in.Body))
	if err != nil {
		return manifest, err
	}
	if in.Sub != envelope.SubHistoryArchive || !in.Replica || len(in.Attachments) != 1 || in.Attachments[0].Size > protocol.MaxHistoryArchivePlaintext {
		return manifest, errors.New("invalid history archive shape")
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return manifest, err
	}
	if !ok {
		return manifest, ErrNoPerson
	}
	if manifest.Person != me.info.Person {
		return manifest, errors.New("archive belongs to another person")
	}
	bound, err := inChainIn(a.store.db, manifest.Person, manifest.Roster)
	if err != nil {
		return manifest, err
	}
	if !bound {
		return manifest, fmt.Errorf("%w: archive roster not yet verified", errPersonRecord)
	}
	err = readSyncAuthority(a.store.db, protocol.ReadSync{V: 1, Person: manifest.Person, Roster: manifest.Roster}, env.From, source.Fingerprint(), a.Address, a.Self().Fingerprint())
	return manifest, err
}

// Dispatch records only authenticated small descriptor metadata. No blob I/O
// or imported turn runs on the held live push stream, and no ACK yet exists.
func (a *Agent) admitHistoryArchive(ctx context.Context, env envelope.Envelope, in envelope.Inner, source identity.Public, held bool, hold func(string, string) error) error {
	_, err := a.archiveAuthority(ctx, env, in, source)
	if held && (errors.Is(err, errPersonRecord) || errors.Is(err, ErrNoPerson)) {
		// Existing proof recovery runs in background upkeep. Never fetch the
		// roster on the live stream merely to admit a bootstrap descriptor.
		if err := a.refreshRecipientPerson(ctx, a.Address, map[string]error{}); err != nil {
			return holdForPerson(err, hold)
		}
		_, err = a.archiveAuthority(ctx, env, in, source)
	}
	if errors.Is(err, errPersonRecord) || errors.Is(err, ErrNoPerson) {
		if err := holdForPerson(err, hold); err != nil {
			return err
		}
		if !held {
			a.convWork.due(convRetry)
			a.kickNow()
		}
		return nil
	}
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	raw, _ := json.Marshal(env)
	var prior string
	err = a.store.db.QueryRow(`SELECT envelope FROM history_archive_jobs WHERE id=?`, env.ID).Scan(&prior)
	if err == nil {
		if prior != string(raw) {
			return hold(reasonConflict, "archive descriptor ciphertext changed")
		}
		a.convWork.due(convHistory)
		a.kickNow()
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err = a.store.db.Exec(`INSERT INTO history_archive_jobs(id,envelope,source_fp) VALUES(?,?,?)`, env.ID, string(raw), source.Fingerprint()); err != nil {
		return err
	}
	a.convWork.due(convHistory)
	a.kickNow()
	return nil
}

func (a *Agent) archiveImportStep(ctx context.Context) (bool, error) {
	var id, raw, fp string
	var retained, pos int
	err := a.store.db.QueryRow(`SELECT id,envelope,source_fp,retained,pos FROM history_archive_jobs WHERE done=0 AND error='' ORDER BY rowid LIMIT 1`).Scan(&id, &raw, &fp, &retained, &pos)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	failed := func(err error) (bool, error) {
		if _, persistErr := a.store.db.Exec(`UPDATE history_archive_jobs SET error=? WHERE id=?`, err.Error(), id); persistErr != nil {
			return false, persistErr
		}
		return true, err // isolate this job; other eligible jobs still get a turn
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &env); err != nil {
		return failed(err)
	}
	source, pending, found, err := a.store.peer(env.From)
	if err != nil {
		return failed(err)
	}
	if !found || pending != nil || source.Fingerprint() != fp {
		return failed(errors.New("archive source key is not currently trusted"))
	}
	in, err := envelope.Open(env, a.id, a.Address, source)
	if err != nil {
		return failed(err)
	}
	manifest, err := a.archiveAuthority(ctx, env, in, source)
	if err != nil {
		return failed(err)
	}
	att := in.Attachments[0]
	f := FileInfo{BlobID: att.Blob.ID, Name: att.Name, Size: att.Size, SHA256: att.SHA256, ctSize: att.Blob.Size, ctSHA256: att.Blob.SHA256}
	if retained == 0 {
		if err = a.fetchCiphertext(ctx, f); err != nil {
			return failed(err)
		}
		// Cache existence alone is insufficient: detect later corruption too.
		size, sum, e := fileDigest(a.downloadPath(f.BlobID))
		if e != nil {
			return failed(e)
		}
		if size != f.ctSize || sum != f.ctSHA256 {
			return failed(errFileCiphertextIntegrity)
		}
		tx, e := a.store.db.Begin()
		if e != nil {
			return failed(e)
		}
		defer tx.Rollback()
		if _, e = tx.Exec(`UPDATE history_archive_jobs SET retained=1,error='' WHERE id=?`, id); e == nil {
			_, e = tx.Exec(`INSERT OR IGNORE INTO history_receipts(id) VALUES(?)`, id)
		}
		if e == nil {
			_, e = tx.Exec(`DELETE FROM quarantine WHERE id=?`, id)
		}
		if e == nil {
			e = a.store.done(tx.Commit())
		}
		if e != nil {
			return failed(e)
		}
		a.postOutboxBackground() // only the descriptor's durably retained ciphertext is acknowledged
	}
	dir := filepath.Join(a.home, "opened")
	if err = secfile.EnsureDir(dir); err != nil {
		return failed(err)
	}
	path, err := a.decryptTo(dir, f)
	if err != nil {
		return failed(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return failed(err)
	}
	chunk, err := envelope.ParseHistoryArchiveChunk(data)
	if err != nil {
		return failed(err)
	}
	if len(chunk.Entries) != manifest.Count {
		return failed(errors.New("archive count does not match signed manifest"))
	}
	// Decode this bounded chunk once, then checkpoint each ordinary admission.
	// No transaction spans crypto, network, or filesystem work.
	for pos < len(chunk.Entries) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		entry := chunk.Entries[pos]
		current, pending, found, e := a.store.peer(env.From)
		if e != nil {
			return failed(e)
		}
		if !found || pending != nil || current.Fingerprint() != fp {
			return failed(errors.New("archive source key is no longer trusted"))
		}
		if _, e = a.archiveAuthority(ctx, env, in, current); e != nil {
			return failed(e)
		}
		child := entry.Envelope
		if child.From != env.From || child.To != a.Address {
			return failed(errors.New("archive child endpoints differ from descriptor"))
		}
		inner, e := envelope.Open(child, a.id, a.Address, source)
		if e != nil {
			return failed(e)
		}
		if e = envelope.ValidateHistoryArchiveChild(inner); e != nil {
			return failed(e)
		}
		if len(entry.Blobs) > 0 && inner.Sub != envelope.SubGroupProof && inner.Sub != envelope.SubGroupContext {
			return failed(errors.New("archive embeds nonstructural ciphertext"))
		}
		for _, blob := range entry.Blobs {
			if e = secfile.EnsureDir(filepath.Dir(a.downloadPath(blob.ID))); e != nil {
				return failed(e)
			}
			if e = secfile.Write(a.downloadPath(blob.ID), blob.CT); e != nil {
				return failed(e)
			}
		}
		seen, e := a.store.seen(child.ID)
		if e != nil {
			return failed(e)
		}
		if !seen {
			rawChild, e := json.Marshal(child)
			if e != nil {
				return failed(e)
			}
			if _, e = a.store.db.Exec(`INSERT OR IGNORE INTO history_archive_children(id,manifest,wire_sha256) VALUES(?,?,?)`, child.ID, id, fmt.Sprintf("%x", sha256.Sum256(rawChild))); e != nil {
				return failed(e)
			}
			if e = a.verifyAndStore(ctx, child); e != nil {
				return failed(e)
			}
		}
		pos++
		if _, err = a.store.db.Exec(`UPDATE history_archive_jobs SET pos=? WHERE id=?`, pos, id); err != nil {
			return failed(err)
		}
	}
	_, err = a.store.db.Exec(`UPDATE history_archive_jobs SET pos=?,done=?,error='' WHERE id=?`, pos, pos == len(chunk.Entries), id)
	if err != nil {
		return failed(err)
	}
	a.NoteChange()
	// Final admissions and cursor are durable. The small completed descriptor
	// remains for idempotent duplicate ACKs; the duplicate chunk can be released.
	if err = os.Remove(a.downloadPath(f.BlobID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

// Separate coalesced upload and import flows share only durable rows, so one
// blocked upload cannot delay an inbound archive or a live receipt/send.
func (a *Agent) postArchiveBackground(parent context.Context) {
	a.archiveWake(parent, &a.archivePosting, a.archiveStep)
	a.archiveWake(parent, &a.archiveImporting, a.archiveImportStep)
}
func (a *Agent) archiveWake(parent context.Context, p *backgroundPosts, step func(context.Context) (bool, error)) {
	p.Lock()
	defer p.Unlock()
	if p.closed || parent.Err() != nil {
		return
	}
	p.archiveParent = parent
	if p.done != nil {
		p.again = true
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.cancel, p.done = cancel, make(chan struct{})
	done := p.done
	go func() {
		defer cancel()
		// Retry incomplete jobs only on an existing external wake, never
		// immediately spin on the same unavailable blob or authority proof.
		if p == &a.archiveImporting && ctx.Err() == nil {
			a.store.db.Exec(`UPDATE history_archive_jobs SET error='' WHERE done=0`)
		}
		if p == &a.archivePosting && ctx.Err() == nil {
			a.store.db.Exec(`DELETE FROM history_archive_export_errors`)
		}
		for {
			more, err := step(ctx)
			if err != nil && ctx.Err() == nil {
				a.Logf("history archive: %v", err)
			}
			p.Lock()
			if (more || p.again) && !p.closed && ctx.Err() == nil {
				p.again = false
				p.Unlock()
				continue
			}
			p.done = nil
			close(done)
			restart := p.again && !p.closed && p.archiveParent != nil && p.archiveParent.Err() == nil
			latest := p.archiveParent
			p.again = false
			p.Unlock()
			if restart {
				a.archiveWake(latest, p, step)
			}
			return
		}
	}()
}
func (a *Agent) stopArchivePosts() {
	for _, p := range []*backgroundPosts{&a.archivePosting, &a.archiveImporting} {
		p.Lock()
		p.closed = true
		done, cancel := p.done, p.cancel
		p.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}
}

// Run shutdown cancels and joins transport work while keeping the Agent and
// its durable data available for offline reads and a later Run.
func (a *Agent) joinArchiveRun() {
	for _, p := range []*backgroundPosts{&a.archivePosting, &a.archiveImporting} {
		p.Lock()
		done, cancel := p.done, p.cancel
		p.again = false
		p.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}
}
