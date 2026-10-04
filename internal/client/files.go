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
	"github.com/misunders2d/agentnet/internal/lockfile"
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

// OutgoingFile is a file to attach: the file at Path, shown to the
// recipient as Name (its base name if empty). The file is encrypted into the
// private spool while sending, so the caller may remove it afterwards.
type OutgoingFile struct {
	Name string
	Path string
}

// maxFileName bounds a file's shown name (bytes).
const maxFileName = 255

// validSendName reports whether name may be a sent file's shown name: text
// of at most maxFileName bytes without control characters.
func validSendName(name string) bool {
	return len(name) <= maxFileName && utf8.ValidString(name) && strings.IndexFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7f }) < 0
}

// spoolFile encrypts path to recipient into the private spool, returning its
// manifest entry. The spooled ciphertext is what gets uploaded, so a resumed
// upload always sends identical bytes.
func (a *Agent) spoolFile(path string, recipient age.Recipient) (envelope.Attachment, error) {
	return a.spoolNamed(OutgoingFile{Path: path}, recipient)
}

// spoolNamed is spoolFile with the shown name f.Name (base name if empty):
// valid text of at most maxFileName bytes, without control characters. The
// recipient treats it as a claim and makes it safe to save (SafeName).
func (a *Agent) spoolNamed(f OutgoingFile, recipient age.Recipient) (envelope.Attachment, error) {
	path, name := f.Path, f.Name
	if name == "" {
		name = filepath.Base(path)
	}
	if !validSendName(name) {
		return envelope.Attachment{}, fmt.Errorf("file name %q: at most %d bytes of text, no control characters", name, maxFileName)
	}
	var att envelope.Attachment
	src, info, err := openRegular(path)
	if err != nil {
		return att, err
	}
	defer src.Close()
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
		Name:   name,
		Size:   pt.n,
		SHA256: pt.hex(),
	}
	if err := os.Rename(tmp.Name(), a.spoolPath(att.Blob.ID)); err != nil {
		return att, err
	}
	return att, syncDir(dir) // the outbox row will point at this file
}

// openRegular opens a file to attach, only if it is a regular file: a named
// pipe or device is refused before it is opened, as opening one can wait
// for a writer forever. It is checked again once open, in case it changed.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	if info, err := os.Stat(path); err != nil {
		return nil, nil, err
	} else if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
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
		if err := a.upload(ctx, a.hub, "/v1/blobs", env.To, b); err != nil {
			return fmt.Errorf("attachment upload: %w", err)
		}
		if err := a.store.setUploadStored(b.ID); err != nil {
			return err
		}
	}
	return nil
}

// upload resumes from wherever the receiving side's copy ends (the Hub, or a
// peer for direct delivery; prefix selects the API), then asks it to verify
// and finalise the file. Every step is safe to repeat.
func (a *Agent) upload(ctx context.Context, conn *hubConn, prefix, to string, b envelope.Blob) error {
	f, err := os.Open(a.spoolPath(b.ID))
	if err != nil {
		return fmt.Errorf("spooled attachment missing: %w", errPermanent)
	}
	defer f.Close()
	var st protocol.BlobStatus
	if err := conn.do(ctx, "POST", prefix, protocol.BlobReserve{ID: b.ID, Recipient: to, Size: b.Size, SHA256: b.SHA256}, &st); err != nil {
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
			path := prefix + "/" + b.ID + "?offset=" + strconv.FormatInt(st.Received, 10)
			err = conn.doBytes(ctx, "PUT", path, buf[:n], &st)
		} else {
			err = conn.do(ctx, "POST", prefix+"/"+b.ID+"/complete", nil, &st)
		}
		var he *HubError
		if errors.As(err, &he) && he.Status == 409 {
			// Our view of the offset is stale: a lost response, or another
			// process (CLI and daemon) sending the same message. Ask.
			if stale++; stale > maxStaleConflicts {
				return errors.New("upload keeps conflicting with another sender of the same message; will retry later")
			}
			err = conn.do(ctx, "GET", prefix+"/"+b.ID, nil, &st)
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

// errRetractedFiles refuses the files of a message its sender deleted,
// opened or saved alike: what was saved before stays where it was saved.
var errRetractedFiles = errors.New("its sender deleted this message: its files are not shown here any more (what you saved stays yours)")

// Download saves every attachment of inbox message id into dir and returns
// the saved paths. Each file is fetched resumably, checked against the
// signed ciphertext digest, decrypted to a private temporary file, checked
// against the manifest, and only then given its final name. Names are made
// safe and unique within the message. A file already at its final path with
// exactly the manifest's content counts as saved, so an interrupted
// download can simply be repeated; any other existing file is kept unless
// overwrite is set. The files of a message its sender deleted are refused,
// as OpenAttachment refuses them. A file known here only from a
// conversation's history is not fetched: it is named in the error with
// how to request it, and the other files are still saved.
func (a *Agent) Download(ctx context.Context, id, dir string, overwrite bool) ([]string, error) {
	files, err := a.store.attachments(id)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("message %s has no attachments", id)
	}
	if retracted, err := a.store.retracted(id); err != nil {
		return nil, err
	} else if retracted {
		return nil, errRetractedFiles
	}
	var saved []string
	defer func() {
		if len(saved) > 0 {
			notifyDaemon(a.home) // a messenger page shows where files were saved
		}
	}()
	var notHere []error
	for i, name := range finalNames(files) {
		f, final := files[i], filepath.Join(dir, name)
		if strings.HasPrefix(f.BlobID, historyBlob) {
			os.Remove(a.downloadPath(f.BlobID) + ".lock") // left by an earlier version that tried to fetch it
			notHere = append(notHere, fmt.Errorf("%s: %w", f.Name, a.historyFileError(id, i)))
			continue
		}
		if err := a.downloadOne(ctx, id, f, final, overwrite); err != nil {
			return saved, errors.Join(append(notHere, fmt.Errorf("%s: %w", f.Name, err))...)
		}
		saved = append(saved, final)
	}
	return saved, errors.Join(notHere...)
}

// errHistoryFile refuses a file known here only from a conversation's
// history (its manifest): its bytes come once requested.
var errHistoryFile = errors.New("this file came with the conversation's history and is not here yet")

// historyFileError says how to request file index of history message
// msgID: in a group from the member device that forwarded it, in a DM from
// this person's other device (asked from the messenger page).
func (a *Agent) historyFileError(msgID string, index int) error {
	conv, err := a.store.convOf(msgID)
	if err != nil {
		return err
	}
	if root, _, found, err := a.store.conversation(conv); err != nil {
		return err
	} else if found && root.Kind == protocol.ConvKindGroup {
		return fmt.Errorf("%w: request it first with agentnet group request-file %s %s %d", errHistoryFile, conv, msgID, index)
	}
	return fmt.Errorf("%w: ask your other device for it first from the messenger page", errHistoryFile)
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
	// Ciphertext that arrived directly may be the only copy; keep it. So
	// is a conversation file's: another device of this person may ask for
	// it (historyfiles.go).
	conv, _ := a.store.convOf(msgID)
	if local, err := a.store.heldLocally(f.BlobID); err == nil && !local && conv == "" {
		os.Remove(a.downloadPath(f.BlobID))
	}
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
// Classifiable signed-integrity failure; ordinary file callers may redownload
// after the corrupt partial is removed, while internal carriers fail closed.
var errFileCiphertextIntegrity = errors.New("signed ciphertext integrity mismatch")

func (a *Agent) fetchCiphertext(ctx context.Context, f FileInfo) error {
	done := a.downloadPath(f.BlobID)
	if _, err := os.Stat(done); err == nil {
		return nil
	}
	if err := secfile.EnsureDir(filepath.Dir(done)); err != nil {
		return err
	}
	// One fetch of a blob at a time, across goroutines and processes (the
	// background keeping of conversation files, a person opening or saving
	// it): both append to the same partial file.
	release, err := lockfile.Wait(done + ".lock")
	if err != nil {
		return err
	}
	defer release()
	if _, err := os.Stat(done); err == nil {
		return nil // fetched meanwhile
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
		return errors.Join(errFileCiphertextIntegrity, a.discard(part, "downloaded attachment does not match the sender's signed digest"))
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

// OpenAttachment opens attachment index of received message msgID for
// reading, without saving it anywhere chosen: the ciphertext is fetched
// resumably and checked against the signed digest, decrypted into a private
// temporary file under the home and checked against the manifest, and only
// then opened. Closing it removes the temporary file. A sent message's
// files are not kept here in the clear, so only received ones open.
func (a *Agent) OpenAttachment(ctx context.Context, msgID string, index int) (io.ReadCloser, FileInfo, error) {
	files, err := a.store.attachments(msgID)
	if err != nil {
		return nil, FileInfo{}, err
	}
	if index < 0 || index >= len(files) {
		return nil, FileInfo{}, fmt.Errorf("received message %s has no attachment %d", msgID, index)
	}
	f := files[index]
	if strings.HasPrefix(f.BlobID, historyBlob) {
		return nil, f, errors.New("this file came with the conversation's history: ask your other device for it first (RequestFile)")
	}
	if retracted, err := a.store.retracted(msgID); err != nil {
		return nil, f, err
	} else if retracted {
		return nil, f, errRetractedFiles
	}
	if err := a.fetchCiphertext(ctx, f); err != nil {
		return nil, f, err
	}
	dir := filepath.Join(a.home, "opened")
	if err := secfile.EnsureDir(dir); err != nil {
		return nil, f, err
	}
	tmp, err := a.decryptTo(dir, f)
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

type removeOnClose struct{ *os.File }

func (r *removeOnClose) Close() error {
	err := r.File.Close()
	os.Remove(r.Name())
	return err
}

// CleanStaging removes what StageUpload left in the private staging folder
// (its upload-* files only). Call it only where nothing can be staging: in
// the daemon holding the home, before its page takes uploads.
func (a *Agent) CleanStaging() error {
	matches, err := filepath.Glob(filepath.Join(a.home, "staging", "upload-*"))
	if err != nil {
		return err
	}
	var first error
	for _, m := range matches {
		if info, err := os.Lstat(m); err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.Remove(m); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// CleanOpened removes plaintext left in the private opened/ folder by an
// earlier run (a file being viewed or served when the daemon stopped or
// crashed). Call it only in the daemon that owns the home, before anything
// opens a file: only this process ever writes there. Files a person saved
// (Download) live where they chose and are never touched.
func (a *Agent) CleanOpened() error {
	entries, err := os.ReadDir(filepath.Join(a.home, "opened"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var first error
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if err := os.Remove(filepath.Join(a.home, "opened", e.Name())); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// StageUpload keeps bytes a local page received (a chosen or pasted file)
// in a private file under the home until they are sent: at most MaxFileSize
// bytes. Pass the path as OutgoingFile.Path (with name as its Name), and call
// cleanup once SendConv returned, whatever it returned.
func (a *Agent) StageUpload(name string, r io.Reader) (path string, cleanup func(), err error) {
	dir := filepath.Join(a.home, "staging")
	if err := secfile.EnsureDir(dir); err != nil {
		return "", nil, err
	}
	f, err := secfile.CreateTemp(dir, "upload-*")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { os.Remove(f.Name()) }
	n, err := io.Copy(f, io.LimitReader(r, MaxFileSize+1))
	if err == nil && n > MaxFileSize {
		err = fmt.Errorf("%q is larger than the %d byte limit", name, MaxFileSize)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return f.Name(), cleanup, nil
}
