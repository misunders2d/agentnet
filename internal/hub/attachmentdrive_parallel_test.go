package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/gdrive"
)

// Delay provider round trips outside the fake provider's own metadata lock.
// This measures the existing ciphertext worker, not CPU or real Drive speed.
type delayedAttachmentProvider struct {
	base         http.RoundTripper
	delay        time.Duration
	mu           sync.Mutex
	active, peak int
	started      chan struct{}
	gate         <-chan struct{}
}

type mixedAttachmentFailures struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (p *mixedAttachmentFailures) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/files/generateIds") {
		n := p.calls.Add(1)
		if n <= 2 {
			status, body, delay := 503, `{"error":{"code":503}}`, time.Millisecond
			if n == 2 {
				status = 403
				body = `{"error":{"code":403,"errors":[{"reason":"storageQuotaExceeded"}]}}`
				delay = 20 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	}
	return p.base.RoundTrip(r)
}

func TestRelayAttachmentDriveQuotaFailureWinsConcurrentTemporaryFailure(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, true)
	h.attachmentDrive.close() // exercise one bounded pass without its retry timer
	for range 8 {
		a.upload(t, h, b.addr, []byte("retained encrypted fixture"))
	}
	h.attachmentDrive.client.HTTP = &http.Client{Transport: &mixedAttachmentFailures{base: h.attachmentDrive.client.HTTP.Transport}}
	if err := h.copyAttachmentQueue(t.Context()); !errors.Is(err, gdrive.ErrStorageFull) {
		t.Fatalf("known Drive quota must pause rather than retry a concurrent temporary failure: %v", err)
	}
}

func (p *delayedAttachmentProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.Contains(r.URL.Path, "/files") {
		return p.base.RoundTrip(r)
	}
	p.mu.Lock()
	p.active++
	p.peak = max(p.peak, p.active)
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.active--; p.mu.Unlock() }()
	if p.started != nil {
		select {
		case p.started <- struct{}{}:
		default:
		}
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	if p.delay > 0 {
		timer := time.NewTimer(p.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return p.base.RoundTrip(r)
}

func attachmentProviderDelay(t *testing.T, h *Hub, delay time.Duration) *delayedAttachmentProvider {
	t.Helper()
	awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool { return !s.Migration.Running })
	p := &delayedAttachmentProvider{base: h.attachmentDrive.client.HTTP.Transport, delay: delay}
	h.attachmentDrive.client.HTTP = &http.Client{Transport: p}
	return p
}

func TestRelayAttachmentDriveSmallFileThroughput(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, true)
	delayed := attachmentProviderDelay(t, h, 25*time.Millisecond)
	const total = 18
	ids := make([]string, 0, total)
	var size int64
	start := time.Now()
	for i := range total {
		n := 8 << 10
		if i%9 == 0 {
			n = 256 << 10
		}
		data := bytes.Repeat([]byte{byte(i)}, n)
		ids = append(ids, a.upload(t, h, b.addr, data))
		size += int64(n)
	}
	// A fresh upload enters while the old backlog is still being copied.
	arrival := time.Now()
	live := a.upload(t, h, b.addr, []byte("new live ciphertext"))
	localLatency := time.Since(arrival)
	awaitAttachmentState(t, h, live, "remote")
	liveLatency := time.Since(arrival)
	for _, id := range ids {
		awaitAttachmentState(t, h, id, "remote")
	}
	elapsed := time.Since(start)
	delayed.mu.Lock()
	peak := delayed.peak
	delayed.mu.Unlock()
	t.Logf("delayed-provider %d files %d bytes: %.2f files/s %.0f bytes/s elapsed=%s live-local-custody=%s live-remote=%s peak=%d", total, size, float64(total)/elapsed.Seconds(), float64(size)/elapsed.Seconds(), elapsed, localLatency, liveLatency, peak)
	if peak > 4 {
		t.Fatalf("unbounded transfers: %d", peak)
	}
	if localLatency > time.Second {
		t.Fatalf("provider delayed local custody: %s", localLatency)
	}
}

func TestRelayAttachmentDriveBoundedParallelProgress(t *testing.T) {
	p := newRelayDriveProvider(t)
	h, a, b, _ := attachmentDriveHub(t, p, true)
	delayed := attachmentProviderDelay(t, h, 0)
	gate := make(chan struct{})
	delayed.gate, delayed.started = gate, make(chan struct{}, 8)
	var once sync.Once
	defer once.Do(func() { close(gate) })
	for range 8 {
		a.upload(t, h, b.addr, []byte("small encrypted attachment"))
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for range 4 {
		select {
		case <-delayed.started:
		case <-ctx.Done():
			t.Fatal("one provider request held all other queued transfers")
		}
	}
	delayed.mu.Lock()
	peak := delayed.peak
	delayed.mu.Unlock()
	if peak != 4 {
		t.Fatalf("active provider requests=%d, want four bounded transfers", peak)
	}
	once.Do(func() { close(gate) })
	awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool { return !s.Migration.Running && s.Remote.Files == 8 })
}

func TestRelayAttachmentDriveArrivalsRespectProviderBackoff(t *testing.T) {
	p := newRelayDriveProvider(t)
	p.failUpload = true
	h, a, b, _ := attachmentDriveHub(t, p, true)
	first := a.upload(t, h, b.addr, []byte("old encrypted attachment"))
	awaitAttachmentState(t, h, first, "retry")
	awaitRelayDriveStatus(t, h, func(s drivecontract.RelayStorageStatus) bool { return !s.Migration.Running && s.State == "offline" })
	p.mu.Lock()
	before := p.uploadCalls
	p.failUpload = false
	p.mu.Unlock()
	ids := []string{first}
	for range 6 {
		ids = append(ids, a.upload(t, h, b.addr, []byte("new attachment keeps local custody during backoff")))
	}
	for _, id := range ids {
		awaitAttachmentState(t, h, id, "remote")
	}
	p.mu.Lock()
	starts := append([]time.Time(nil), p.uploadStarts...)
	p.mu.Unlock()
	// Arrival uploads can take longer than the backoff on a slow disk. Check
	// the actual provider dispatch interval rather than requiring the timer
	// to remain active until this test finishes creating all six arrivals.
	if before != 1 || len(starts) < 2 {
		t.Fatalf("missing initial failure and subsequent retry: before=%d starts=%d", before, len(starts))
	}
	if interval := starts[1].Sub(starts[0]); interval < time.Second {
		t.Fatalf("arrivals bypassed one-second provider backoff: retry after %s", interval)
	}
	t.Logf("provider retry began %s after the initial failed dispatch", starts[1].Sub(starts[0]))
}
