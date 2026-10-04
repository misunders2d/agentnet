package client

import (
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

	"filippo.io/age"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Files of conversation history (history.go). A history message carries
// its files' manifests only: the new device asks a device of its person
// for one (RequestFile), which re-encrypts it to the new device and sends
// it back as an offer (sub "file"); the file is then opened like any other.
// What a device can offer:
//
//   - a file it sent: at send time it keeps a copy encrypted to its own key
//     in kept/ (by the file's SHA-256), because the copies it sent are
//     encrypted to other devices only. Kept copies stay as long as the
//     installation (there is no conversation deletion yet): they are not
//     spool files and are not removed when a message reaches the Hub;
//   - a file it received in a conversation: the ciphertext for this device,
//     which it fetches soon after receiving it (one file per sync, in the
//     background) and keeps in downloads/, also after it is saved.
//
// A file no device of the person holds any more (never fetched before the
// Hub dropped it) is answered as unavailable, and shown so.

// fileMsg is the body of a sub "file" message.
type fileMsg struct {
	V              int    `json:"v"`
	Type           string `json:"type"` // request or offer
	LID            string `json:"lid"`  // the message the file belongs to
	SHA256         string `json:"sha256"`
	Available      bool   `json:"available,omitempty"` // offer: attached
	Detail         string `json:"detail,omitempty"`    // offer: why not
	Author         string `json:"author,omitempty"`
	Hash           string `json:"hash,omitempty"`
	Index          *int   `json:"index,omitempty"`
	Name           string `json:"name,omitempty"`
	Size           *int64 `json:"size,omitempty"`
	GroupAdmission string `json:"group_admission,omitempty"`
}

func (a *Agent) keptPath(sha string) string { return filepath.Join(a.home, "kept", sha+".age") }

// keepSent keeps a copy of the file at path, encrypted to this device's own
// key, for other devices of this person to ask for later.
func (a *Agent) keepSent(path string) error {
	dir := filepath.Join(a.home, "kept")
	if err := secfile.EnsureDir(dir); err != nil {
		return err
	}
	src, _, err := openRegular(path)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := secfile.CreateTemp(dir, ".kept-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after rename
	pt := newCountingHash()
	enc, err := age.Encrypt(tmp, a.id.Box.Recipient())
	if err == nil {
		_, err = io.Copy(enc, io.TeeReader(io.LimitReader(src, MaxFileSize+1), pt))
	}
	if err == nil {
		err = enc.Close()
	}
	if err == nil && pt.n > MaxFileSize {
		err = fmt.Errorf("%s is larger than the %d byte limit", path, MaxFileSize)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if _, err := os.Stat(a.keptPath(pt.hex())); err == nil {
		return nil // kept already
	}
	if err := os.Rename(tmp.Name(), a.keptPath(pt.hex())); err != nil {
		return err
	}
	return syncDir(dir)
}

// RequestFile asks the device that forwarded history message msgID for its
// file index; the answer arrives as an offer, and the file then opens as
// usual (its Availability says where the request stands).
func (a *Agent) RequestFile(ctx context.Context, msgID string, index int) error {
	var conv, lid, via string
	err := a.store.db.QueryRow(`SELECT conv, lid, coalesce(via, '') FROM inbox WHERE id = ?`, msgID).Scan(&conv, &lid, &via)
	if errors.Is(err, sql.ErrNoRows) || err == nil && via == "" {
		return fmt.Errorf("message %s is not a history message here", msgID)
	}
	if err != nil {
		return err
	}
	if root, _, found, e := a.store.conversation(conv); e != nil {
		return e
	} else if found && root.Kind == protocol.ConvKindGroup {
		return a.requestGroupHistoryFile(ctx, msgID, index)
	}
	files, err := a.store.attachments(msgID)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(files) {
		return fmt.Errorf("message %s has no file %d", msgID, index)
	}
	f := files[index]
	if !strings.HasPrefix(f.BlobID, historyBlob) {
		return nil // here already
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	dev, own := me.device(via)
	if !ok || !own {
		return fmt.Errorf("%s is no longer a device of your person: the file cannot be asked from it", via)
	}
	_, raw, _, err := a.store.conversation(conv)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(fileMsg{V: 1, Type: "request", LID: lid, SHA256: f.SHA256})
	c, err := a.fileCarrier(dev, conv, raw, string(body))
	if err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertCopies(tx, []outCopy{c}); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO file_requests(message_id, sha256, state, updated_at) VALUES(?, ?, 'requested', ?)
		ON CONFLICT(message_id, sha256) DO UPDATE SET state = 'requested', detail = NULL, updated_at = excluded.updated_at`,
		msgID, f.SHA256, time.Now().Unix()); err != nil {
		return err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.kickNow()
	notifyDaemon(a.home)
	return nil
}

// fileCarrier seals a sub "file" message for the device to.
func (a *Agent) fileCarrier(to identity.Public, conv string, raw []byte, body string, atts ...envelope.Attachment) (outCopy, error) {
	recipient, err := to.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: to.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: body, Conv: conv, LID: protocol.NewID(), Root: raw, Replica: true, Sub: envelope.SubFile, Attachments: atts}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return outCopy{}, err
	}
	copy := outCopy{env: env, in: in, state: stateQueued}
	if root, e := protocol.ParseConvRoot(raw); e == nil && root.Kind == protocol.ConvKindGroup {
		packet, e := groupTurnPacketIn(a.store.db, conv)
		if e != nil {
			return outCopy{}, e
		}
		if e = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); e != nil {
			return outCopy{}, e
		}
		if e = groupTurnCheck(a.store.db, packet, to.Address, to.Fingerprint()); e != nil {
			return outCopy{}, e
		}
		me, ok, e := a.store.selfPerson(a.Address)
		if e != nil || !ok || !me.has(to.Address, to.Fingerprint()) {
			return outCopy{}, errors.New("group file copy is not a current own device")
		}
		copy.required, copy.recipientFP = protocol.CapGroup, to.Fingerprint()
	}
	return copy, nil
}

// admitFile takes a sub "file" message from another device of this
// person: a request is queued for the worker (serveFiles); an offer
// completes the history message's file.
func (a *Agent) admitFile(env envelope.Envelope, in envelope.Inner, hold func(string, string) error, checks ...func(dbq) error) error {
	check := func(q dbq) error {
		for _, guard := range checks {
			if err := guard(q); err != nil {
				return err
			}
		}
		return nil
	}
	var m fileMsg
	if err := decodeStrict([]byte(in.Body), &m); err != nil || m.V != 1 || !protocol.ValidID(m.LID) || !protocol.ValidHash(m.SHA256) {
		return hold(reasonInvalid, "a malformed file message")
	}
	now := time.Now().Unix()
	switch m.Type {
	case "request":
		tx, err := a.store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = check(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO file_serves(id, device, conv, lid, sha256, state, updated_at) VALUES(?, ?, ?, ?, ?, 'pending', ?)`,
			env.ID, env.From, in.Conv, m.LID, m.SHA256, now); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		a.convWork.due(convServe)
		a.kickNow()
		return nil
	case "offer":
		var msgID string
		err := a.store.db.QueryRow(`SELECT i.id FROM inbox i JOIN attachments f ON f.message_id = i.id
			WHERE i.conv = ? AND i.lid = ? AND i.via IS NOT NULL AND f.sha256 = ? AND f.blob_id LIKE ? LIMIT 1`,
			in.Conv, m.LID, m.SHA256, historyBlob+"%").Scan(&msgID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not waiting for it (any more)
		}
		if err != nil {
			return err
		}
		if !m.Available {
			tx, err := a.store.db.Begin()
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err = check(tx); err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE file_requests SET state = 'unavailable', detail = ?, updated_at = ? WHERE message_id = ? AND sha256 = ?`,
				m.Detail, now, msgID, m.SHA256)
			if err != nil {
				return err
			}
			return a.store.done(tx.Commit())
		}
		if len(in.Attachments) != 1 || in.Attachments[0].SHA256 != m.SHA256 {
			return hold(reasonInvalid, "a file offer without that file")
		}
		att := in.Attachments[0]
		tx, err := a.store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err = check(tx); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE attachments SET blob_id = ?, ct_size = ?, ct_sha256 = ? WHERE message_id = ? AND sha256 = ? AND blob_id LIKE ?`,
			att.Blob.ID, att.Blob.Size, att.Blob.SHA256, msgID, m.SHA256, historyBlob+"%"); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM file_requests WHERE message_id = ? AND sha256 = ?`, msgID, m.SHA256); err != nil {
			return err
		}
		return a.store.done(tx.Commit())
	}
	return hold(reasonInvalid, "a file message of unknown type")
}

// serveFiles answers one pending file request (a file for another device of
// this person) and reports whether more wait.
func (a *Agent) serveFiles(ctx context.Context) (more bool) {
	var id, device, conv, lid, sha string
	var descriptor []byte
	err := a.store.db.QueryRow(`SELECT id, device, conv, lid, sha256,group_descriptor FROM file_serves WHERE state = 'pending' ORDER BY updated_at, id LIMIT 1`).
		Scan(&id, &device, &conv, &lid, &sha, &descriptor)
	if err != nil {
		return false
	}
	if len(descriptor) > 0 {
		var job groupFileServe
		if decodeStrict(descriptor, &job) != nil || !protocol.ValidFingerprint(job.Key) || !groupFileMessageValid(job.Message) {
			err = errors.Join(errPermanent, errors.New("group: invalid persisted file request"))
		} else {
			err = a.serveGroupHistoryFile(ctx, id, device, conv, job)
		}
		if err != nil {
			if retryable(err) {
				return false
			}
			a.store.db.Exec(`UPDATE file_serves SET state='unavailable',updated_at=? WHERE id=?`, time.Now().Unix(), id)
		}
		var n int
		a.store.db.QueryRow(`SELECT count(*) FROM file_serves WHERE state='pending'`).Scan(&n)
		return n > 0
	}
	state, detail := "served", ""
	if err := a.serveFile(ctx, device, conv, lid, sha); err != nil {
		if retryable(err) {
			return false // the Hub is out of reach: the next sync tries again
		}
		state, detail = "unavailable", err.Error()
		me, ok, _ := a.store.selfPerson(a.Address)
		if dev, own := me.device(device); ok && own {
			_, raw, _, _ := a.store.conversation(conv)
			body, _ := json.Marshal(fileMsg{V: 1, Type: "offer", LID: lid, SHA256: sha, Detail: detail})
			if c, err := a.fileCarrier(dev, conv, raw, string(body)); err == nil {
				tx, err := a.store.db.Begin()
				if err == nil {
					if insertCopies(tx, []outCopy{c}) == nil {
						tx.Commit()
					} else {
						tx.Rollback()
					}
				}
			}
		}
	}
	a.store.db.Exec(`UPDATE file_serves SET state = ?, updated_at = ? WHERE id = ?`, state, time.Now().Unix(), id)
	var n int
	a.store.db.QueryRow(`SELECT count(*) FROM file_serves WHERE state = 'pending'`).Scan(&n)
	return n > 0
}

// serveFile re-encrypts the file (lid, sha) of conv for device, a current
// device of this person, and queues the offer carrying it.
func (a *Agent) serveFile(ctx context.Context, device, conv, lid, sha string) error {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	dev, own := me.device(device)
	if !ok || !own {
		return fmt.Errorf("%w: the asking device is no device of this person", errPermanent)
	}
	// A turn deleted here offers no file, unless unfinished work here still
	// keeps it (convclear.go: retainedIn).
	var erased int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM conv_erased e WHERE e.conv = ? AND e.lid = ?
		AND NOT EXISTS (SELECT 1 FROM inbox i WHERE i.conv = e.conv AND i.lid = e.lid AND `+retainedIn+`)
		AND NOT EXISTS (SELECT 1 FROM outbox o WHERE o.conv = e.conv AND o.lid = e.lid AND `+retainedOut+`)`, conv, lid).Scan(&erased); err != nil {
		return err
	}
	if erased > 0 {
		return fmt.Errorf("%w: its conversation was deleted here", errPermanent)
	}
	plain, name, err := a.fileSource(ctx, conv, lid, sha)
	if err != nil {
		return err
	}
	defer os.Remove(plain)
	recipient, err := dev.Recipient()
	if err != nil {
		return err
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return err
	}
	defer release()
	att, err := a.spoolNamed(OutgoingFile{Name: name, Path: plain}, recipient)
	if err != nil {
		return err
	}
	if att.SHA256 != sha {
		a.releaseSpool(envelope.Envelope{Blobs: []envelope.Blob{att.Blob}})
		return fmt.Errorf("%w: the kept file no longer matches its manifest", errPermanent)
	}
	_, raw, _, err := a.store.conversation(conv)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(fileMsg{V: 1, Type: "offer", LID: lid, SHA256: sha, Available: true})
	c, err := a.fileCarrier(dev, conv, raw, string(body), att)
	if err != nil {
		a.releaseSpool(envelope.Envelope{Blobs: []envelope.Blob{att.Blob}})
		return err
	}
	if err := a.store.addConvOutbox([]outCopy{c}, envelope.Inner{}, nil, ""); err != nil {
		a.releaseSpool(envelope.Envelope{ID: c.env.ID, Blobs: c.env.Blobs})
		return err
	}
	return nil
}

// fileSource writes the plaintext of the file (lid, sha) of conv into a
// private temporary file: from this device's kept copy of a file it sent,
// or its ciphertext of a file it received (fetched from the Hub if not
// here yet). It returns the path and the file's name.
func (a *Agent) fileSource(ctx context.Context, conv, lid, sha string) (path, name string, err error) {
	dir := filepath.Join(a.home, "opened")
	if err := secfile.EnsureDir(dir); err != nil {
		return "", "", err
	}
	var msgID string
	var f FileInfo
	err = a.store.db.QueryRow(`SELECT a.message_id, a.blob_id, a.name, a.size, a.sha256, a.ct_size, a.ct_sha256
		FROM attachments a JOIN inbox i ON i.id = a.message_id WHERE i.conv = ? AND i.lid = ? AND a.sha256 = ? AND a.blob_id NOT LIKE ? LIMIT 1`,
		conv, lid, sha, historyBlob+"%").Scan(&msgID, &f.BlobID, &f.Name, &f.Size, &f.SHA256, &f.ctSize, &f.ctSHA256)
	if err == nil { // received here
		if err := a.fetchCiphertext(ctx, f); err != nil {
			return "", "", err
		}
		tmp, err := a.decryptTo(dir, f)
		if err != nil {
			return "", "", fmt.Errorf("%w: %v", errPermanent, err)
		}
		return tmp, f.Name, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	err = a.store.db.QueryRow(`SELECT s.name, s.size FROM sent_attachments s JOIN outbox o ON o.id = s.message_id
		WHERE o.conv = ? AND o.lid = ? AND s.sha256 = ? LIMIT 1`, conv, lid, sha).Scan(&f.Name, &f.Size)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: this device holds no such file", errPermanent)
	}
	if err != nil {
		return "", "", err
	}
	path, err = a.openKept(dir, sha, f.Size)
	if err != nil {
		return "", "", err
	}
	return path, f.Name, nil
}

// errNotKept means this device holds no copy of a file it sent.
var errNotKept = errors.New("this device kept no copy of that file")

// keptHere reports whether this device holds its own copy of the file with
// digest sha.
func (a *Agent) keptHere(sha string) bool {
	_, err := os.Stat(a.keptPath(sha))
	return err == nil
}

// openKept decrypts this device's kept copy of the file with digest sha
// into a new private temporary file in dir, checked against size and sha,
// and returns its path.
func (a *Agent) openKept(dir, sha string, size int64) (string, error) {
	src, err := os.Open(a.keptPath(sha))
	if err != nil {
		return "", fmt.Errorf("%w: %w", errPermanent, errNotKept)
	}
	defer src.Close()
	r, err := age.Decrypt(src, a.id.Box)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errPermanent, err)
	}
	tmp, err := secfile.CreateTemp(dir, ".agentnet-*.part")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp.Name())
		}
	}()
	pt := newCountingHash()
	if _, err := io.Copy(io.MultiWriter(tmp, pt), io.LimitReader(r, size+1)); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if pt.n != size || pt.hex() != sha {
		return "", fmt.Errorf("%w: the kept file no longer matches its manifest", errPermanent)
	}
	ok = true
	return tmp.Name(), nil
}

// OpenSentAttachment opens attachment index of a message this device sent
// (a conversation turn or a device message), from the copy it kept for
// itself at send time; closing it removes the temporary plaintext. A
// message sent before copies were kept, or from another device of this
// person, has no copy here: errNotKept, never a substitute.
func (a *Agent) OpenSentAttachment(ctx context.Context, msgID string, index int) (io.ReadCloser, FileInfo, error) {
	files, err := a.store.sentAttachments(msgID)
	if err != nil {
		return nil, FileInfo{}, err
	}
	if index < 0 || index >= len(files) {
		return nil, FileInfo{}, fmt.Errorf("sent message %s has no attachment %d", msgID, index)
	}
	f := files[index]
	dir := filepath.Join(a.home, "opened")
	if err := secfile.EnsureDir(dir); err != nil {
		return nil, f, err
	}
	tmp, err := a.openKept(dir, f.SHA256, f.Size)
	if err != nil {
		return nil, f, err
	}
	r, err := os.Open(tmp)
	if err != nil {
		os.Remove(tmp)
		return nil, f, err
	}
	return &removeOnClose{File: r}, f, nil
}

// ErrAmbiguousMessage means an id names both a received and a sent
// message here (a peer chooses the ids of what it sends, so it can pick one
// this device used), and the caller did not say which.
var ErrAmbiguousMessage = errors.New("that id names both a received and a sent message here: say which (dir in or out)")

// OpenFileFrom opens attachment index of message msgID: dir "in" a message
// received (OpenAttachment), "out" one sent by this device
// (OpenSentAttachment) or, as views show it, by another device of this
// person (sentElsewhere: stored here as received). With dir "" the
// direction is the one table the id is in; an id in both fails closed
// (ErrAmbiguousMessage) rather than serve the other direction's file. The
// one file API the messenger page uses for both directions.
func (a *Agent) OpenFileFrom(ctx context.Context, dir, msgID string, index int) (io.ReadCloser, FileInfo, error) {
	switch dir {
	case "in":
		return a.OpenAttachment(ctx, msgID, index)
	case "out":
		if via, err := a.sentElsewhere(msgID); err != nil {
			return nil, FileInfo{}, err
		} else if via {
			return a.OpenAttachment(ctx, msgID, index)
		}
		return a.OpenSentAttachment(ctx, msgID, index)
	case "":
	default:
		return nil, FileInfo{}, fmt.Errorf("unknown direction %q (in or out)", dir)
	}
	var received, sent int
	if err := a.store.db.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE id = ?), (SELECT count(*) FROM outbox WHERE id = ?)`,
		msgID, msgID).Scan(&received, &sent); err != nil {
		return nil, FileInfo{}, err
	}
	switch {
	case received > 0 && sent > 0:
		return nil, FileInfo{}, ErrAmbiguousMessage
	case sent > 0:
		return a.OpenSentAttachment(ctx, msgID, index)
	}
	return a.OpenAttachment(ctx, msgID, index)
}

// markOpenable fills Openable for files of one message: sent files open
// from a kept copy; received files open unless known from history only.
func (a *Agent) markOpenable(files []FileInfo, sent bool) {
	for i := range files {
		switch {
		case sent:
			files[i].Openable = a.keptHere(files[i].SHA256)
		default:
			files[i].Openable = !strings.HasPrefix(files[i].BlobID, historyBlob)
		}
	}
}

// prefetchFiles fetches (and keeps) the ciphertext of one received file
// not held here yet, newest message first, and reports whether more wait:
// conversation files and device-message files alike, so a file survives
// the Hub dropping its blob later. Nothing counts as held before the
// ciphertext matched the sender's signed size and digest (fetchCiphertext).
// Files that failed are left for a later run.
func (a *Agent) prefetchFiles(ctx context.Context) (more bool) {
	rows, err := a.store.db.Query(`SELECT a.blob_id, a.name, a.size, a.sha256, a.ct_size, a.ct_sha256
		FROM attachments a JOIN inbox i ON i.id = a.message_id
		WHERE i.local = 0 AND a.blob_id NOT LIKE ? AND NOT `+retractedClause+` AND NOT (`+erasedIn+` AND NOT `+retainedIn+`)
		ORDER BY coalesce(i.received_ms, i.received_at * 1000) DESC`, historyBlob+"%")
	if err != nil {
		return false
	}
	var todo []FileInfo
	for rows.Next() {
		var f FileInfo
		if rows.Scan(&f.BlobID, &f.Name, &f.Size, &f.SHA256, &f.ctSize, &f.ctSHA256) != nil {
			continue
		}
		if _, err := os.Stat(a.downloadPath(f.BlobID)); err == nil || a.prefetchFailed[f.BlobID] {
			continue
		}
		todo = append(todo, f)
	}
	rows.Close()
	if len(todo) == 0 {
		return false
	}
	if err := a.fetchCiphertext(ctx, todo[0]); err != nil && !retryable(err) {
		if a.prefetchFailed == nil {
			a.prefetchFailed = map[string]bool{}
		}
		a.prefetchFailed[todo[0].BlobID] = true
		a.Logf("keeping a conversation file: %v", err)
	} else if err != nil {
		return false // out of reach: the next connection tries again
	}
	return len(todo) > 1
}
