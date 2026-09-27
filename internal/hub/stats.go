package hub

import (
	"net/http"
	"sync/atomic"
)

// Stats counts Hub traffic, for operators and for tests that prove a
// payload bypassed the Hub.
type Stats struct {
	Requests     atomic.Int64 // every API request, including stream connects and ping acks
	Acks         atomic.Int64 // ping acknowledgements (the only periodic client request)
	Messages     atomic.Int64 // message envelopes accepted
	BlobBytesIn  atomic.Int64 // attachment ciphertext received
	BlobBytesOut atomic.Int64 // attachment ciphertext served
}

// Stats returns the Hub's live counters.
func (h *Hub) Stats() *Stats { return &h.stats }

func (h *Hub) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.stats.Requests.Add(1)
		next.ServeHTTP(w, r)
	})
}

// countingWriter counts body bytes written through it.
type countingWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (c countingWriter) Write(p []byte) (int, error) {
	n, err := c.ResponseWriter.Write(p)
	c.n.Add(int64(n))
	return n, err
}
