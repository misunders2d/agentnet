package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"filippo.io/age"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Attachments stream through fixed-size buffers: encryption and hashing use
// io.Copy, uploads send ChunkSize pieces, downloads fetch downloadRange
// pieces. No whole file is ever held in memory.
const downloadRange = 4 << 20

// syncDir makes renames durable; tests replace it to inject failures.
var syncDir = secfile.SyncDir

// MaxFileSize is the plaintext limit the client enforces before encrypting.
var MaxFileSize int64 = protocol.DefaultMaxFileSize

func (a *Agent) spoolPath(blobID string) string {
	return filepath.Join(a.home, "spool", blobID+".age")
}

func (a *Agent) downloadPath(blobID string) string {
	return filepath.Join(a.home, "downloads", blobID+".age")
}

// countingHash hashes and counts what is written through it.
type countingHash struct {
	hash.Hash
	n int64
}

func (c *countingHash) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return c.Hash.Write(p)
}

func newCountingHash() *countingHash { return &countingHash{Hash: sha256.New()} }

func (c *countingHash) hex() string { return hex.EncodeToString(c.Sum(nil)) }

// spoolFile encrypts path to recipient into the private spool, returning its
// manifest entry. The spooled ciphertext is what gets uploaded, so a resumed
// upload always sends identical bytes.
func (a *Agent) spoolFile(path string, recipient age.Recipient) (envelope.Attachment, error) {
	var att envelope.Attachment
	src, err := os.Open(path)
	if err != nil {
		return att, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return att, err
	}
	if !info.Mode().IsRegular() {
		return att, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > MaxFileSize {
		return att, fmt.Errorf("%s is larger than the %d byte limit", path, MaxFileSize)
	}
	dir := filepath.Join(a.home, "spool")
	if err := secfile.EnsureDir(dir); err != nil {
		return att, err
	}
	tmp, err := os.CreateTemp(dir, ".spool-*")
	if err != nil {
		return att, err
	}
	defer os.Remove(tmp.Name()) // no-op after rename
	ct, pt := newCountingHash(), newCountingHash()
	enc, err := age.Encrypt(io.MultiWriter(tmp, ct), recipient)
	if err == nil {
		_, err = io.Copy(enc, io.TeeReader(io.LimitReader(src, MaxFileSize+1), pt))
	}
	if err == nil {
		err = enc.Close()
	}
	if err == nil && pt.n > MaxFileSize {
		err = fmt.Errorf("%s grew past the %d byte limit while reading", path, MaxFileSize)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return att, err
	}
	att = envelope.Attachment{
		Blob:   envelope.Blob{ID: protocol.NewID(), Size: ct.n, SHA256: ct.hex()},
		Name:   filepath.Base(path),
		Size:   pt.n,
		SHA256: pt.hex(),
	}
	if err := os.Rename(tmp.Name(), a.spoolPath(att.Blob.ID)); err != nil {
		return att, err
	}
	return att, syncDir(dir) // the outbox row will point at this file
}

// uploadAll sends every attachment of env the Hub does not hold yet.
func (a *Agent) uploadAll(ctx context.Context, env envelope.Envelope) error {
	for _, b := range env.Blobs {
		done, err := a.store.uploadStored(b.ID)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		if err := a.upload(ctx, env.To, b); err != nil {
			return fmt.Errorf("attachment upload: %w", err)
		}
		if err := a.store.setUploadStored(b.ID); err != nil {
			return err
		}
	}
	return nil
}

// upload resumes from wherever the Hub's copy ends, then asks the Hub to
// verify and finalise it. Every step is safe to repeat.
func (a *Agent) upload(ctx context.Context, to string, b envelope.Blob) error {
	f, err := os.Open(a.spoolPath(b.ID))
	if err != nil {
		return fmt.Errorf("spooled attachment missing: %w", errPermanent)
	}
	defer f.Close()
	var st protocol.BlobStatus
	if err := a.hub.do(ctx, "POST", "/v1/blobs", protocol.BlobReserve{ID: b.ID, Recipient: to, Size: b.Size, SHA256: b.SHA256}, &st); err != nil {
		return err
	}
	buf := make([]byte, protocol.ChunkSize)
	stale := 0 // consecutive conflicts without progress
	for st.State != protocol.BlobStored {
		before := st.Received
		if st.Received < st.Size {
			n := min(int64(len(buf)), st.Size-st.Received)
			if _, err := f.ReadAt(buf[:n], st.Received); err != nil {
				return err
			}
			path := "/v1/blobs/" + b.ID + "?offset=" + strconv.FormatInt(st.Received, 10)
			err = a.hub.doBytes(ctx, "PUT", path, buf[:n], &st)
		} else {
			err = a.hub.do(ctx, "POST", "/v1/blobs/"+b.ID+"/complete", nil, &st)
		}
		var he *HubError
		if errors.As(err, &he) && he.Status == 409 {
			// Our view of the offset is stale: a lost response, or another
			// process (CLI and daemon) sending the same message. Ask.
			if stale++; stale > maxStaleConflicts {
				return errors.New("upload keeps conflicting with another sender of the same message; will retry later")
			}
			err = a.hub.do(ctx, "GET", "/v1/blobs/"+b.ID, nil, &st)
		}
		if err != nil {
			return err
		}
		if st.Received > before || st.State == protocol.BlobStored {
			stale = 0
		}
	}
	return nil
}

const maxStaleConflicts = 3

// releaseSpool deletes spooled ciphertext once the Hub holds the message.
func (a *Agent) releaseSpool(env envelope.Envelope) {
	for _, b := range env.Blobs {
		os.Remove(a.spoolPath(b.ID))
	}
	if err := a.store.releaseUploads(env.ID); err != nil {
		a.Logf("uploads: %v", err)
	}
}

// ErrExists means a download target already exists and overwrite was not asked for.
var ErrExists = errors.New("target file already exists (use --force to replace it)")

// Download saves every attachment of inbox message id into dir and returns
// the saved paths. Each file is fetched resumably, checked against the
// signed ciphertext digest, decrypted to a private temporary file, checked
// against the manifest, and only then given its final name. Names are made
// safe and unique within the message. A file already at its final path with
// exactly the manifest's content counts as saved, so an interrupted
// download can simply be repeated; any other existing file is kept unless
// overwrite is set.
func (a *Agent) Download(ctx context.Context, id, dir string, overwrite bool) ([]string, error) {
	files, err := a.store.attachments(id)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("message %s has no attachments", id)
	}
	var saved []string
	for i, name := range finalNames(files) {
		f, final := files[i], filepath.Join(dir, name)
		if err := a.downloadOne(ctx, id, f, final, overwrite); err != nil {
			return saved, fmt.Errorf("%s: %w", f.Name, err)
		}
		saved = append(saved, final)
	}
	return saved, nil
}

func (a *Agent) downloadOne(ctx context.Context, msgID string, f FileInfo, final string, overwrite bool) error {
	match, exists, err := matchesManifest(final, f)
	if err != nil {
		return err
	}
	if match {
		// Possibly released by an earlier run whose directory sync failed;
		// sync again before recording it as saved.
		if err := syncDir(filepath.Dir(final)); err != nil {
			return err
		}
		return a.store.setSaved(msgID, f.BlobID, final)
	}
	if exists && !overwrite {
		return ErrExists
	}
	if err := a.fetchCiphertext(ctx, f); err != nil {
		return err
	}
	tmp, err := a.decryptTo(filepath.Dir(final), f)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	if overwrite {
		err = os.Rename(tmp, final)
	} else if err = os.Link(tmp, final); errors.Is(err, os.ErrExist) {
		err = ErrExists
	}
	if err == nil {
		err = syncDir(filepath.Dir(final))
	}
	if err != nil {
		return err
	}
	os.Remove(a.downloadPath(f.BlobID))
	return a.store.setSaved(msgID, f.BlobID, final)
}

// matchesManifest reports whether path is a regular file (not a link) with
// exactly the attachment's size and digest, and whether anything is there.
func matchesManifest(path string, f FileInfo) (match, exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() || info.Size() != f.Size {
		return false, true, nil
	}
	size, sum, err := fileDigest(path)
	return err == nil && size == f.Size && sum == f.SHA256, true, err
}

// finalNames gives each attachment a safe file name, adding " (2)", " (3)"
// ... before the extension when names collide (case-insensitively, as on
// Windows and macOS). The result depends only on the manifest order.
func finalNames(files []FileInfo) []string {
	used := map[string]bool{}
	names := make([]string, len(files))
	for i, f := range files {
		base := SafeName(f.Name)
		name := base
		for k := 2; used[strings.ToLower(name)]; k++ {
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			if stem == "" {
				stem, ext = base, ""
			}
			name = fmt.Sprintf("%s (%d)%s", stem, k, ext)
		}
		used[strings.ToLower(name)] = true
		names[i] = name
	}
	return names
}

// fetchCiphertext completes the private ciphertext copy, resuming a partial
// one, and verifies it against the signed size and digest.
func (a *Agent) fetchCiphertext(ctx context.Context, f FileInfo) error {
	done := a.downloadPath(f.BlobID)
	if _, err := os.Stat(done); err == nil {
		return nil
	}
	if err := secfile.EnsureDir(filepath.Dir(done)); err != nil {
		return err
	}
	part := done + ".part"
	w, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer w.Close()
	info, err := w.Stat()
	if err != nil {
		return err
	}
	offset := info.Size()
	if offset > f.ctSize {
		return a.discard(part, "partial download is longer than the attachment")
	}
	for offset < f.ctSize {
		end := min(offset+downloadRange, f.ctSize)
		if err := a.hub.getRange(ctx, "/v1/blobs/"+url.PathEscape(f.BlobID)+"/data", offset, end, w); err != nil {
			return err // the part file keeps what arrived; the next attempt resumes
		}
		if err := w.Sync(); err != nil {
			return err
		}
		offset = end
	}
	if err := w.Close(); err != nil {
		return err
	}
	size, sum, err := fileDigest(part)
	if err != nil {
		return err
	}
	if size != f.ctSize || sum != f.ctSHA256 {
		return a.discard(part, "downloaded attachment does not match the sender's signed digest")
	}
	if err := os.Rename(part, done); err != nil {
		return err
	}
	return syncDir(filepath.Dir(done))
}

func (a *Agent) discard(part, why string) error {
	os.Remove(part)
	return errors.New(why)
}

// decryptTo decrypts the verified ciphertext into a new temporary file in
// dir and checks it against the manifest. It returns the temporary path.
func (a *Agent) decryptTo(dir string, f FileInfo) (string, error) {
	src, err := os.Open(a.downloadPath(f.BlobID))
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := secfile.CreateTemp(dir, ".agentnet-*.part") // owner-only before plaintext lands
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
	r, err := age.Decrypt(src, a.id.Box)
	if err == nil {
		_, err = io.Copy(io.MultiWriter(tmp, pt), io.LimitReader(r, f.Size+1))
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	if pt.n != f.Size || pt.hex() != f.SHA256 {
		return "", errors.New("decrypted attachment does not match the sender's manifest")
	}
	ok = true
	return tmp.Name(), nil
}

func fileDigest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := newCountingHash()
	if _, err := io.Copy(h, f); err != nil {
		return 0, "", err
	}
	return h.n, h.hex(), nil
}

var windowsReserved = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true}

// SafeName turns a sender-chosen name into a plain file name: no directory
// parts, control characters, or characters Windows forbids, and never
// empty, "." or "..".
func SafeName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	if name == "" {
		return "attachment"
	}
	if windowsReserved[strings.ToUpper(strings.SplitN(name, ".", 2)[0])] {
		name = "_" + name
	}
	for len(name) > 200 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	return name
}
