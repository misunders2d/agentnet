package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/misunders2d/agentnet/internal/gdrive"
)

// ServeContent remains the authority for HTTP conditions, HEAD, suffix and
// multipart ranges. This reader translates only its selected reads into exact
// provider ranges, without a full-file cache or cross-recipient serialization.
type attachmentRangeReader struct {
	mu                        sync.Mutex
	ctx                       context.Context
	download                  *gdrive.CiphertextDownload
	size, position, remaining int64
	ranges                    []attachmentByteRange
	next                      int
	body                      io.ReadCloser
	closed                    bool
}
type attachmentByteRange struct{ start, length int64 }

func newAttachmentRangeReader(r *http.Request, download *gdrive.CiphertextDownload, size int64) *attachmentRangeReader {
	header := r.Header.Get("Range")
	// The existing handler has no ETag or modification time. ServeContent
	// therefore ignores every If-Range condition and sends the whole body.
	if r.Header.Get("If-Range") != "" {
		header = ""
	}
	return &attachmentRangeReader{ctx: r.Context(), download: download, size: size, ranges: attachmentRangePlan(header, size)}
}
func (r *attachmentRangeReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errors.New("attachment reader closed")
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.position >= r.size {
		return 0, io.EOF
	}
	if r.body == nil {
		end := r.size - 1
		if r.next < len(r.ranges) && r.ranges[r.next].start == r.position {
			part := r.ranges[r.next]
			end = part.start + part.length - 1
			r.next++
		}
		body, err := r.download.Range(r.ctx, r.position, end)
		if err != nil {
			return 0, err
		}
		r.body, r.remaining = body, end-r.position+1
	}
	if int64(len(p)) > r.remaining {
		p = p[:int(r.remaining)]
	}
	n, err := r.body.Read(p)
	r.position += int64(n)
	r.remaining -= int64(n)
	if r.remaining == 0 {
		r.body.Close()
		r.body = nil
		return n, io.EOF
	}
	return n, err
}
func (r *attachmentRangeReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, errors.New("attachment reader closed")
	}
	position := offset
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		position += r.position
	case io.SeekEnd:
		position += r.size
	default:
		return 0, errors.New("invalid attachment seek")
	}
	if position < 0 {
		return 0, errors.New("invalid attachment seek")
	}
	if r.body != nil {
		r.body.Close()
		r.body = nil
	}
	r.position = position
	return position, nil
}
func (r *attachmentRangeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.body != nil {
		err := r.body.Close()
		r.body = nil
		return err
	}
	return nil
}

// Normalize byte ranges as net/http does, solely to bound each provider read.
// Invalid/unsatisfiable headers are still answered by ServeContent before Read.
func attachmentRangePlan(header string, size int64) []attachmentByteRange {
	if !strings.HasPrefix(header, "bytes=") {
		return nil
	}
	var out []attachmentByteRange
	var total int64
	for _, part := range strings.Split(strings.TrimPrefix(header, "bytes="), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		start, end, ok := strings.Cut(part, "-")
		if !ok {
			return nil
		}
		start, end = strings.TrimSpace(start), strings.TrimSpace(end)
		var first, length int64
		if start == "" {
			if end == "" || strings.HasPrefix(end, "-") {
				return nil
			}
			n, err := strconv.ParseInt(end, 10, 64)
			if err != nil || n < 0 {
				return nil
			}
			if n > size {
				n = size
			}
			first, length = size-n, n
		} else {
			n, err := strconv.ParseInt(start, 10, 64)
			if err != nil || n < 0 {
				return nil
			}
			if n >= size {
				continue
			}
			first = n
			last := size - 1
			if end != "" {
				last, err = strconv.ParseInt(end, 10, 64)
				if err != nil || last < first {
					return nil
				}
				if last >= size {
					last = size - 1
				}
			}
			length = last - first + 1
		}
		// ServeContent also ignores excessive overlapping aggregate ranges.
		if length > size-total {
			return nil
		}
		total += length
		out = append(out, attachmentByteRange{first, length})
	}
	return out
}
