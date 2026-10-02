package hub

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Maintenance works on a stopped Hub's data directory: storage report,
// cleanup, and backup. It holds the same lock a running Hub holds, so it
// refuses to run while the Hub is up; stopping the Hub briefly is the
// supported way to get a consistent copy.
type Maintenance struct {
	dir    string
	store  *store
	unlock func()
}

// OpenMaintenance locks and opens a Hub data directory.
func OpenMaintenance(dir string) (*Maintenance, error) {
	if _, err := os.Stat(filepath.Join(dir, "hub.db")); err != nil {
		return nil, fmt.Errorf("%s is not a Hub data directory: %w", dir, err)
	}
	unlock, err := lockData(dir)
	if err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(dir, "hub.db"))
	if err != nil {
		unlock()
		return nil, err
	}
	return &Maintenance{dir: dir, store: st, unlock: unlock}, nil
}

// Close releases the directory.
func (m *Maintenance) Close() error {
	err := m.store.db.Close()
	m.unlock()
	return err
}

// Usage is attachment storage by kind; sizes are ciphertext bytes.
type Usage struct {
	Undelivered, Delivered, Unattached, Uploading Amount
	Quota                                         int64 `json:"-"`
}

// Amount is a count of files and their total size.
type Amount struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

func (m *Maintenance) amount(where string, args ...any) (Amount, error) {
	var a Amount
	err := m.store.db.QueryRow(`SELECT count(*), coalesce(sum(b.size), 0) FROM blobs b
		LEFT JOIN messages m ON m.id = b.message_id WHERE `+where, args...).Scan(&a.Files, &a.Bytes)
	return a, err
}

// Usage reports what the Hub stores. Undelivered attachments are messages
// still in custody; they are never removed by Cleanup.
func (m *Maintenance) Usage() (Usage, error) {
	var u Usage
	var err error
	sets := []struct {
		dst   *Amount
		where string
		args  []any
	}{
		{&u.Undelivered, `b.state = ? AND m.state = ?`, []any{protocol.BlobStored, protocol.StateCustody}},
		{&u.Delivered, `b.state = ? AND m.state IN (?, ?, ?)`, []any{protocol.BlobStored, protocol.StateDelivered, protocol.StateQuarantined, protocol.StateExpired}},
		{&u.Unattached, `b.state = ? AND b.message_id IS NULL`, []any{protocol.BlobStored}},
		{&u.Uploading, `b.state != ?`, []any{protocol.BlobStored}},
	}
	for _, s := range sets {
		if *s.dst, err = m.amount(s.where, s.args...); err != nil {
			return u, err
		}
	}
	return u, nil
}

// Cleanup removes attachments of messages delivered (or expired) more than
// delivered ago, completed uploads never attached to a message after
// unattached, and idle unfinished uploads after uploadTTL. Files are removed
// before their rows, so an interrupted cleanup leaves only counted rows to
// retry. It returns what was removed.
func (m *Maintenance) Cleanup(delivered, unattached, uploadTTL time.Duration) (Amount, error) {
	now := time.Now()
	rows, err := m.store.db.Query(`SELECT b.id, b.size FROM blobs b LEFT JOIN messages m ON m.id = b.message_id
		WHERE (b.state = ? AND m.state IN (?, ?, ?) AND coalesce(m.delivered_at, m.created_at) < ?)
		   OR (b.state = ? AND b.message_id IS NULL AND b.updated_at < ?)
		   OR (b.state != ? AND b.updated_at < ?)`,
		protocol.BlobStored, protocol.StateDelivered, protocol.StateQuarantined, protocol.StateExpired, now.Add(-delivered).Unix(),
		protocol.BlobStored, now.Add(-unattached).Unix(),
		protocol.BlobStored, now.Add(-uploadTTL).Unix())
	if err != nil {
		return Amount{}, err
	}
	type victim struct {
		id   string
		size int64
	}
	var vs []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.id, &v.size); err != nil {
			rows.Close()
			return Amount{}, err
		}
		vs = append(vs, v)
	}
	rows.Close()
	var removed Amount
	blobs := filepath.Join(m.dir, "blobs")
	for _, v := range vs {
		if err := blobfile.RemoveIfExists(filepath.Join(blobs, v.id+".part"), filepath.Join(blobs, v.id+".blob")); err != nil {
			return removed, err
		}
		if err := secfile.SyncDir(blobs); err != nil {
			return removed, err
		}
		if _, err := m.store.db.Exec(`DELETE FROM blobs WHERE id = ?`, v.id); err != nil {
			return removed, err
		}
		removed.Files++
		removed.Bytes += v.size
	}
	return removed, nil
}

// backupSkip lists files that are not part of a backup.
func backupSkip(name string) bool {
	return name == "hub.lock" || name == "hub.db.upgrade.lock" || strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") ||
		strings.Contains(name, ".v") && strings.HasSuffix(name, ".bak")
}

// Backup writes a gzip-compressed tar of the stopped Hub: database (after a
// full checkpoint), TLS key and certificate, pending bootstrap invite, and
// attachment ciphertext. It contains secrets; store it like a private key.
func (m *Maintenance) Backup(w io.Writer) error {
	if _, err := m.store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint: %w", err)
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.WalkDir(m.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || backupSkip(d.Name()) {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		rel, err := filepath.Rel(m.dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: filepath.ToSlash(rel), Mode: 0o600, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, f)
		f.Close()
		return err
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	return err
}

// backupEntry is the exact set of names a backup may contain, so nothing
// can be written outside the restore directory on any platform.
var backupEntry = regexp.MustCompile(`^(hub\.db|tls\.crt|tls\.key|` + regexp.QuoteMeta(pushKeyFile) + `|` + regexp.QuoteMeta(BootstrapFile) +
	`|blobs/[0-9a-f]{32}\.(blob|part))$`)

// RestoreSummary describes a restored data directory.
type RestoreSummary struct {
	Agents, Messages, Blobs int
}

// Restore unpacks a backup into dir, which must be empty or missing, then
// checks that the database opens and every stored attachment is present
// with its recorded size. A failed restore leaves dir for inspection.
func Restore(r io.Reader, dir string) (RestoreSummary, error) {
	var sum RestoreSummary
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return sum, fmt.Errorf("%s is not empty; restore into a new directory", dir)
	}
	if err := secfile.EnsureDir(dir); err != nil {
		return sum, err
	}
	if err := secfile.EnsureDir(filepath.Join(dir, "blobs")); err != nil {
		return sum, err
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return sum, fmt.Errorf("not an AgentNet Hub backup: %w", err)
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, fmt.Errorf("reading backup: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || !backupEntry.MatchString(hdr.Name) {
			return sum, fmt.Errorf("backup entry %q is not allowed", hdr.Name)
		}
		dst := filepath.Join(dir, filepath.FromSlash(hdr.Name))
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return sum, err
		}
		_, err = io.Copy(f, tr)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return sum, err
		}
	}
	if err := secfile.SyncDir(filepath.Join(dir, "blobs")); err != nil {
		return sum, err
	}
	if err := secfile.SyncDir(dir); err != nil {
		return sum, err
	}
	m, err := OpenMaintenance(dir)
	if err != nil {
		return sum, err
	}
	defer m.Close()
	return m.verify()
}

func (m *Maintenance) verify() (RestoreSummary, error) {
	var sum RestoreSummary
	db := m.store.db
	if err := db.QueryRow(`SELECT count(*) FROM agents`).Scan(&sum.Agents); err != nil {
		return sum, err
	}
	if err := db.QueryRow(`SELECT count(*) FROM messages`).Scan(&sum.Messages); err != nil {
		return sum, err
	}
	rows, err := db.Query(`SELECT id, size, sha256 FROM blobs WHERE state = ?`, protocol.BlobStored)
	if err != nil {
		return sum, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, sha string
		var size int64
		if err := rows.Scan(&id, &size, &sha); err != nil {
			return sum, err
		}
		if err := blobfile.Check(filepath.Join(m.dir, "blobs", id+".blob"), size, sha); err != nil {
			return sum, fmt.Errorf("attachment %s missing or damaged in the backup: %w", id, err)
		}
		sum.Blobs++
	}
	return sum, rows.Err()
}
