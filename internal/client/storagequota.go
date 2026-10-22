package client

import (
	"context"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func (a *Agent) ChangeStorageQuota(ctx context.Context, change protocol.StorageQuotaChange) (StorageSummary, error) {
	if !change.Confirm || change.QuotaBytes <= 0 || change.QuotaBytes > 1<<53-1 {
		return StorageSummary{}, errors.New("confirm a positive relay quota in whole bytes")
	}
	if err := a.hub.do(ctx, http.MethodPost, "/v1/admin/storage/quota", change, nil); err != nil {
		return StorageSummary{}, err
	}
	return a.Storage(ctx)
}
