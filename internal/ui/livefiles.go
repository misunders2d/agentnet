package ui

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Files in DMs on the daemon's page (MEL-489), through the client only. The
// page hands this computer's AgentNet the bytes of a chosen or pasted file
// (kept privately under the home until sent), then sends the message
// naming them; SendConv encrypts each to the recipient before it returns,
// and the staged copy is removed. A received file is opened only after the
// client checked it against what its sender signed.

// staged are files the page handed over and not sent yet, by id.
type staged struct {
	mu    sync.Mutex
	files map[string]stagedFile
}

type stagedFile struct {
	name, path string
	cleanup    func()
	at         time.Time
}

// maxStaged bounds the files waiting to be sent; older ones than stagedFor
// are dropped when another comes (a page closed before sending).
const (
	maxStaged = 32
	stagedFor = time.Hour
)

func (l *Live) fileLimits() *FileLimits {
	return &FileLimits{MaxFile: client.MaxFileSize, MaxCount: envelope.MaxAttachments}
}

// StageFile implements Files.
func (l *Live) StageFile(name string, r io.Reader) (string, error) {
	l.staged.mu.Lock()
	defer l.staged.mu.Unlock()
	if l.staged.files == nil {
		l.staged.files = map[string]stagedFile{}
	}
	for id, f := range l.staged.files {
		if time.Since(f.at) > stagedFor {
			f.cleanup()
			delete(l.staged.files, id)
		}
	}
	if len(l.staged.files) >= maxStaged {
		return "", Refuse("Too many files are waiting to be sent: send or remove some first.")
	}
	path, cleanup, err := l.a.StageUpload(name, r)
	if err != nil {
		return "", Refuse(sentence(err))
	}
	id := protocol.NewID()
	l.staged.files[id] = stagedFile{name: name, path: path, cleanup: cleanup, at: time.Now()}
	return id, nil
}

// DiscardFiles implements Files.
func (l *Live) DiscardFiles(ids []string) {
	l.staged.mu.Lock()
	defer l.staged.mu.Unlock()
	for _, id := range ids {
		if f, ok := l.staged.files[id]; ok {
			f.cleanup()
			delete(l.staged.files, id)
		}
	}
}

// takeStaged hands over the staged files ids name, and the cleanup that
// removes them (whatever the send did). A send takes every file it names,
// even when one of them is gone: those found are removed with the refusal,
// and the page hands the files over again.
func (l *Live) takeStaged(ids []string) ([]client.OutgoingFile, func(), error) {
	l.staged.mu.Lock()
	defer l.staged.mu.Unlock()
	var out []client.OutgoingFile
	var cleanups []func()
	missing := false
	for _, id := range ids {
		f, ok := l.staged.files[id]
		if !ok {
			missing = true
			continue
		}
		delete(l.staged.files, id)
		out = append(out, client.OutgoingFile{Name: f.name, Path: f.path})
		cleanups = append(cleanups, f.cleanup)
	}
	cleanup := func() {
		for _, c := range cleanups {
			c()
		}
	}
	if missing {
		cleanup()
		return nil, nil, Refuse("A file to send is no longer with AgentNet on this computer (it restarted, or an hour passed): send again to hand it over again.")
	}
	return out, cleanup, nil
}

// OpenFile implements Files: the received file, checked and decrypted, or
// the kept copy of a file sent from this device, with its name made safe.
func (l *Live) OpenFile(ctx context.Context, dir, msgID string, index int) (io.ReadCloser, string, error) {
	r, f, err := l.a.OpenFileFrom(ctx, dir, msgID, index)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, "", err
		}
		return nil, "", Refuse(sentence(err))
	}
	return r, client.SafeName(f.Name), nil
}

// RequestFile implements HistoryFiles.
func (l *Live) RequestFile(ctx context.Context, msgID string, index int) error {
	if err := l.a.RequestFile(ctx, msgID, index); err != nil {
		return Refuse(sentence(err))
	}
	return nil
}

// fileViews are a message's files for the page.
func fileViews(files []client.FileInfo) []FileView {
	var out []FileView
	for i, f := range files {
		v := FileView{Index: i, Name: client.SafeName(f.Name), Size: f.Size, Saved: f.SavedPath, Availability: f.Availability, Openable: f.Openable}
		if !v.Openable && v.Availability == "" {
			v.Note = notKeptNote
		}
		out = append(out, v)
	}
	return out
}

// notKeptNote explains a sent file this device cannot open.
const notKeptNote = "Sent from this device before it kept copies of sent files, or from another device of yours: no copy here to open."
