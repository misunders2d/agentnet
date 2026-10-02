package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
)

// StorageProvider is an optional read-only daemon page capability. Browser
// storage and eviction belong to the browser provider, not this local view.
type StorageProvider interface {
	Storage(context.Context) (client.StorageSummary, error)
}

func (l *Live) Storage(ctx context.Context) (client.StorageSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	return l.a.Storage(ctx)
}

// storage is registered as GET /api/storage inside the existing Server guard
// by the route owner. There is no action, cleanup or provider mutation here.
func (s *Server) storage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	p, ok := s.p.(StorageProvider)
	if !ok {
		writeErr(w, NotFound("local managed storage is not available from this provider"))
		return
	}
	view, err := p.Storage(r.Context())
	if err != nil {
		// Provider errors can contain paths: never pass them to the logger.
		http.Error(w, "storage summary unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, view)
}
