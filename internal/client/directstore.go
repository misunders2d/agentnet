package client

import (
	"database/sql"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Accounting for attachment ciphertext received directly from peers. It
// mirrors the Hub's rules: reservations are checked against a quota in one
// transaction, and abandoned uploads are reclaimed in two phases so a failed
// delete stays counted. Completed direct files are the recipient's only
// copy and stay until an operator removes them.

const blobReclaiming = "reclaiming"

var (
	errDirectQuota    = errors.New("direct delivery storage quota exceeded")
	errDirectConflict = errors.New("upload id already used for a different upload")
	errDirectBusy     = errors.New("upload is being reclaimed; retry later")
	errNoSuchUpload   = errors.New("unknown upload")
)

type directBlob struct {
	Owner          string
	Size, Received int64
	SHA256, State  string
}

func (s *store) directBlob(id string) (directBlob, error) {
	var b directBlob
	err := s.db.QueryRow(`SELECT owner, size, received, sha256, state FROM direct_blobs WHERE id = ?`, id).
		Scan(&b.Owner, &b.Size, &b.Received, &b.SHA256, &b.State)
	if errors.Is(err, sql.ErrNoRows) {
		return b, errNoSuchUpload
	}
	return b, err
}

func (s *store) reserveDirect(owner string, r protocol.BlobReserve, quota int64) (directBlob, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return directBlob{}, err
	}
	defer tx.Rollback()
	var b directBlob
	err = tx.QueryRow(`SELECT owner, size, received, sha256, state FROM direct_blobs WHERE id = ?`, r.ID).
		Scan(&b.Owner, &b.Size, &b.Received, &b.SHA256, &b.State)
	switch {
	case err == nil:
		if b.Owner != owner || b.Size != r.Size || b.SHA256 != r.SHA256 {
			return b, errDirectConflict
		}
		if b.State == blobReclaiming {
			return b, errDirectBusy
		}
		return b, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return b, err
	}
	var used int64
	if err := tx.QueryRow(`SELECT coalesce(sum(size), 0) FROM direct_blobs`).Scan(&used); err != nil {
		return b, err
	}
	if used+r.Size > quota {
		return b, errDirectQuota
	}
	if _, err := tx.Exec(`INSERT INTO direct_blobs(id, owner, size, sha256, state, updated_at) VALUES(?, ?, ?, ?, ?, ?)`,
		r.ID, owner, r.Size, r.SHA256, protocol.BlobUploading, time.Now().Unix()); err != nil {
		return b, err
	}
	return directBlob{Owner: owner, Size: r.Size, SHA256: r.SHA256, State: protocol.BlobUploading}, tx.Commit()
}

func affectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errNoSuchUpload
	}
	return nil
}

func (s *store) setDirectReceived(id string, received int64) error {
	return affectOne(s.db.Exec(`UPDATE direct_blobs SET received = ?, updated_at = ? WHERE id = ? AND state = ?`,
		received, time.Now().Unix(), id, protocol.BlobUploading))
}

func (s *store) setDirectState(id, state string) error {
	return affectOne(s.db.Exec(`UPDATE direct_blobs SET state = ?, received = CASE WHEN ? = ? THEN size ELSE 0 END, updated_at = ?
		WHERE id = ? AND state IN (?, ?)`,
		state, state, protocol.BlobStored, time.Now().Unix(), id, protocol.BlobUploading, protocol.BlobStored))
}

// directStored reports whether id is a completed direct upload from owner
// with the given size and digest.
func (s *store) directStored(id, owner string, size int64, sha string) (bool, error) {
	b, err := s.directBlob(id)
	if errors.Is(err, errNoSuchUpload) {
		return false, nil
	}
	return err == nil && b.State == protocol.BlobStored && b.Owner == owner && b.Size == size && b.SHA256 == sha, err
}

// heldLocally reports whether the ciphertext for id arrived directly, so the
// local copy may be the only one and must not be discarded.
func (s *store) heldLocally(id string) (bool, error) {
	b, err := s.directBlob(id)
	if errors.Is(err, errNoSuchUpload) {
		return false, nil
	}
	return err == nil && b.State == protocol.BlobStored, err
}

func (s *store) markDirectReclaiming(before time.Time) ([]string, error) {
	if _, err := s.db.Exec(`UPDATE direct_blobs SET state = ? WHERE state = ? AND updated_at < ?`,
		blobReclaiming, protocol.BlobUploading, before.Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id FROM direct_blobs WHERE state = ?`, blobReclaiming)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *store) deleteDirectReclaimed(id string) error {
	_, err := s.db.Exec(`DELETE FROM direct_blobs WHERE id = ? AND state = ?`, id, blobReclaiming)
	return err
}
