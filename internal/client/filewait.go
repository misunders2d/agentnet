package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Big received files wait for a tap instead of being kept in the background
// (owner, November 1, 2026: "large video files (>10Mb) should be offered for
// download instead of instantly playing. same as on Telegram"). Telegram
// Android (3de37c8c, DownloadController.java) keeps videos up to 10 MB and
// other files up to 1–3 MB by default; pictures here are always kept. The
// relay keeps an attachment until an admin's cleanup removes it, on every
// relay, so a waiting file downloads when tapped (FetchFile) until then. A
// file whose cached copy was cleared waits as well.
const (
	autoKeepVideo = 10 << 20 // 10 MiB
	autoKeepFile  = 3 << 20  // 3 MiB
)

// The page's own picture and video names (Message.files.tsx pictureName,
// videoName): what it shows as a picture is kept like one here.
var (
	pictureName = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp)$`)
	videoName   = regexp.MustCompile(`(?i)\.(mp4|m4v|mov|webm|ogv|ogg)$`)
)

// keptAutomatically says whether a received file is fetched in the
// background (prefetchFiles) rather than on a tap.
func keptAutomatically(name string, size int64) bool {
	switch {
	case pictureName.MatchString(name):
		return true
	case videoName.MatchString(name):
		return size <= autoKeepVideo
	}
	return size <= autoKeepFile
}

// FileInfo.Fetch values for a received file not held here yet.
const (
	FetchWait     = "wait"     // download on a tap (FetchFile)
	FetchFetching = "fetching" // downloading now (FileProgress)
)

// errFileGone: the relay no longer has the attachment (an admin's cleanup).
var errFileGone = errors.New("this file is no longer on the relay")

// fileGoneKey marks an attachment the relay answered "unknown" for, per
// ciphertext digest, like cache-cleared/ (storagecache.go).
func fileGoneKey(blobID string) string { return "file-gone/" + blobID }

// markFetch fills Fetch (or unavailability) of one received file that can
// be opened: nothing when it is here or about to be kept in the background.
func (a *Agent) markFetch(f *FileInfo) {
	if _, err := os.Stat(a.downloadPath(f.BlobID)); err == nil {
		return
	}
	if a.fetches.active(f.BlobID) {
		f.Fetch = FetchFetching
		return
	}
	var gone, cleared int
	a.store.db.QueryRow(`SELECT count(*) FILTER (WHERE k=?), count(*) FILTER (WHERE k=?) FROM config WHERE k IN (?,?) AND v=?`,
		fileGoneKey(f.BlobID), "cache-cleared/"+f.BlobID, fileGoneKey(f.BlobID), "cache-cleared/"+f.BlobID, f.ctSHA256).Scan(&gone, &cleared)
	switch {
	case gone > 0:
		f.Openable, f.Availability = false, "unavailable"
	case cleared > 0 || !keptAutomatically(f.Name, f.Size):
		f.Fetch = FetchWait
	}
}

// FileProgress is where the download of a received file stands.
type FileProgress struct {
	Have     int64  `json:"have"`  // ciphertext bytes here
	Total    int64  `json:"total"` // ciphertext bytes in all
	Done     bool   `json:"done"`  // held here: it opens at once
	Fetching bool   `json:"fetching,omitempty"`
	Gone     bool   `json:"gone,omitempty"`  // the relay no longer has it
	Error    string `json:"error,omitempty"` // the last attempt failed; fetch again to retry
}

// fileFetches are the downloads a person started, one per blob, running
// until done or until the Agent closes, whoever looks at them.
type fileFetches struct {
	mu      sync.Mutex
	running map[string]*fileFetch
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
}

type fileFetch struct {
	have, total int64
	done        bool
	err         error
	moved       chan struct{} // closed and replaced at each reported step
	reported    int64
	at          time.Time
}

func (s *fileFetches) active(blob string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.running[blob]
	return f != nil && !f.done
}

func (s *fileFetches) stop() {
	s.mu.Lock()
	s.closed = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// step records progress, waking waiters at most every 1% or 250 ms.
func (s *fileFetches) step(f *fileFetch, have int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f.have = have
	if have-f.reported >= max(f.total/100, 1) || time.Since(f.at) >= 250*time.Millisecond {
		f.reported, f.at = have, time.Now()
		close(f.moved)
		f.moved = make(chan struct{})
	}
}

func (s *fileFetches) finish(f *fileFetch, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f.done, f.err = true, err
	close(f.moved)
	f.moved = make(chan struct{})
}

// receivedFile is attachment index of a message as the page names it: dir
// "in" a received message, "out" a sent one (only one sent by another
// device of this person is fetched; this device's own copy is here).
func (a *Agent) receivedFile(dir, msgID string, index int) (f FileInfo, here bool, err error) {
	switch dir {
	case "in":
	case "out":
		via, err := a.sentElsewhere(msgID)
		if err != nil {
			return f, false, err
		}
		if !via {
			return f, true, nil
		}
	default:
		return f, false, fmt.Errorf("unknown direction %q (in or out)", dir)
	}
	files, err := a.store.attachments(msgID)
	if err != nil {
		return f, false, err
	}
	if index < 0 || index >= len(files) {
		return f, false, fmt.Errorf("received message %s has no attachment %d", msgID, index)
	}
	f = files[index]
	if f.ctSHA256 == "" || strings.HasPrefix(f.BlobID, historyBlob) {
		return f, false, errors.New("this file came with the conversation's history: request it from the device that shared it first")
	}
	if retracted, err := a.store.retracted(msgID); err != nil {
		return f, false, err
	} else if retracted {
		return f, false, errRetractedFiles
	}
	_, err = os.Stat(a.downloadPath(f.BlobID))
	return f, err == nil, nil
}

// FetchFile starts downloading a received file in the background, unless it
// is here or already downloading, and says where it stands. It keeps going
// when the person looks away; FileProgress follows it.
func (a *Agent) FetchFile(dir, msgID string, index int) (FileProgress, error) {
	f, here, err := a.receivedFile(dir, msgID, index)
	if err != nil {
		return FileProgress{}, err
	}
	if here {
		return FileProgress{Have: f.ctSize, Total: f.ctSize, Done: true}, nil
	}
	s := &a.fetches
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return FileProgress{}, errors.New("this device is closing")
	}
	if e := s.running[f.BlobID]; e != nil && !e.done {
		return e.progress(), nil
	}
	if s.ctx == nil {
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.running = map[string]*fileFetch{}
	}
	e := &fileFetch{total: f.ctSize, moved: make(chan struct{}), at: time.Now()}
	if info, err := os.Stat(a.downloadPath(f.BlobID) + ".part"); err == nil {
		e.have = min(info.Size(), f.ctSize) // resumes there
	}
	s.running[f.BlobID] = e
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		err := a.fetchCiphertextTo(s.ctx, f, func(have int64) { s.step(e, have) })
		s.finish(e, err)
		a.changes.bump() // its file shows as here, or as no longer available
	}()
	return e.progress(), nil
}

// progress reads e; s.mu is held.
func (e *fileFetch) progress() FileProgress {
	p := FileProgress{Have: e.have, Total: e.total, Fetching: !e.done}
	if e.done {
		switch {
		case e.err == nil:
			p.Done, p.Have = true, e.total
		case errors.Is(e.err, errFileGone):
			p.Gone, p.Error = true, errFileGone.Error()
		default:
			p.Error = e.err.Error()
		}
	}
	return p
}

// FileProgress says where a received file's download stands once it has
// moved past after bytes, ended, or wait passed: one answer per step, never
// a timer on the page's side.
func (a *Agent) FileProgress(ctx context.Context, dir, msgID string, index int, after int64, wait time.Duration) (FileProgress, error) {
	f, here, err := a.receivedFile(dir, msgID, index)
	if err != nil {
		return FileProgress{}, err
	}
	if here {
		return FileProgress{Have: f.ctSize, Total: f.ctSize, Done: true}, nil
	}
	s := &a.fetches
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		s.mu.Lock()
		e := s.running[f.BlobID]
		if e == nil {
			s.mu.Unlock()
			p := FileProgress{Total: f.ctSize}
			if info, err := os.Stat(a.downloadPath(f.BlobID) + ".part"); err == nil {
				p.Have = min(info.Size(), f.ctSize)
			}
			return p, nil
		}
		p, moved := e.progress(), e.moved
		s.mu.Unlock()
		if !p.Fetching || p.Have > after {
			return p, nil
		}
		select {
		case <-moved:
		case <-timer.C:
			return p, nil
		case <-ctx.Done():
			return p, ctx.Err()
		}
	}
}

// progressWriter reports the bytes written so far, from at.
type progressWriter struct {
	w      io.Writer
	at     int64
	report func(int64)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.at += int64(n)
	p.report(p.at)
	return n, err
}

// goneOnRelay records that the relay no longer has f, so its file shows as
// no longer available instead of offering a download that cannot succeed.
func (a *Agent) goneOnRelay(f FileInfo, err error) error {
	var he *HubError
	if !errors.As(err, &he) || he.Status != http.StatusNotFound {
		return err
	}
	gone := fmt.Errorf("%w (%w)", errFileGone, err) // still the relay's refusal: never retried (retryable)
	if e := a.store.setConfig(map[string]string{fileGoneKey(f.BlobID): f.ctSHA256}); e != nil {
		return errors.Join(gone, e)
	}
	return gone
}
