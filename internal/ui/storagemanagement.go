package ui

import (
	"context"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type storageQuotaProvider interface {
	ChangeStorageQuota(context.Context, protocol.StorageQuotaChange) (client.StorageSummary, error)
}
type storageCacheProvider interface {
	StorageCache(context.Context) (client.StorageCacheSelection, error)
	ClearStorageCache(context.Context, client.StorageCacheClear) (client.StorageCacheResult, error)
}
type storageRetryProvider interface {
	RetryStorageSend(context.Context, client.StorageSendRetry) (client.StorageSendRetried, error)
}

func (l *Live) ChangeStorageQuota(ctx context.Context, change protocol.StorageQuotaChange) (client.StorageSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, err := l.a.ChangeStorageQuota(ctx, change)
	return v, relayStorageWords(err)
}
func (l *Live) StorageCache(ctx context.Context) (client.StorageCacheSelection, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, err := l.a.StorageCache(ctx)
	if err != nil {
		return v, Refuse("Received cache could not be inspected. Your files are unchanged.")
	}
	return v, nil
}
func (l *Live) ClearStorageCache(ctx context.Context, change client.StorageCacheClear) (client.StorageCacheResult, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, err := l.a.ClearStorageCache(ctx, change)
	if err != nil {
		return v, Refuse("Received cache could not be cleared. Refresh storage to check the result.")
	}
	return v, nil
}
func (l *Live) RetryStorageSend(ctx context.Context, change client.StorageSendRetry) (client.StorageSendRetried, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, err := l.a.RetryStorageSend(ctx, change)
	if err != nil {
		return v, Refuse("This stored send could not be retried. Its text and files are kept here.")
	}
	return v, nil
}

func (s *Server) storageQuota(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(storageQuotaProvider)
	if !ok {
		writeErr(w, NotFound("quota changes are not available from this provider"))
		return
	}
	var change protocol.StorageQuotaChange
	if !readJSON(w, r, &change) {
		return
	}
	v, err := p.ChangeStorageQuota(r.Context(), change)
	writeResult(w, v, err)
}
func (s *Server) storageCache(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(storageCacheProvider)
	if !ok {
		writeErr(w, NotFound("received-cache management is not available from this provider"))
		return
	}
	if r.Method == http.MethodGet {
		v, err := p.StorageCache(r.Context())
		writeResult(w, v, err)
		return
	}
	var change client.StorageCacheClear
	if !readJSON(w, r, &change) {
		return
	}
	v, err := p.ClearStorageCache(r.Context(), change)
	writeResult(w, v, err)
}
func (s *Server) storageRetry(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(storageRetryProvider)
	if !ok {
		writeErr(w, NotFound("stored-send retry is not available from this provider"))
		return
	}
	var change client.StorageSendRetry
	if !readJSON(w, r, &change) {
		return
	}
	v, err := p.RetryStorageSend(r.Context(), change)
	writeResult(w, v, err)
}
