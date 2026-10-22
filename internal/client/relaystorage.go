package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/misunders2d/agentnet/internal/drivecontract"
)

// ErrRelayStorageAction is a relay attachment storage change other than
// connect, move or disconnect. Nothing is sent to the Hub for it.
var ErrRelayStorageAction = errors.New("choose connect, move or disconnect")

var errRelayStorageUnclear = errors.New("the server's attachment storage status was not understood")

var relayStorageStates = map[string]bool{"local": true, "not_configured": true, "connected": true, "migrating": true, "needs_reconnect": true, "offline": true}

// RelayAttachmentStorage reads, as this signed member, where the relay keeps
// attachment ciphertext: its own disk or the one Google Drive its admin
// connected. A read never starts consent, so a consent link is dropped. Hub
// refusals stay *HubError, with their status, for the page to explain.
func (a *Agent) RelayAttachmentStorage(ctx context.Context) (drivecontract.RelayStorageStatus, error) {
	var v drivecontract.RelayStorageStatus
	if err := a.hub.do(ctx, http.MethodGet, "/v1/storage/attachments", nil, &v); err != nil {
		return drivecontract.RelayStorageStatus{}, err
	}
	v.ConsentURL = ""
	return checkRelayStorage(v)
}

// ChangeRelayAttachmentStorage asks the Hub to connect its Drive, move the
// existing ciphertext there, or disconnect. The Hub alone decides whether
// this device is a current admin: an earlier can_admin grants nothing. Only
// connect answers with the initiating admin's consent link.
func (a *Agent) ChangeRelayAttachmentStorage(ctx context.Context, r drivecontract.RelayStorageAction) (drivecontract.RelayStorageStatus, error) {
	switch r.Action {
	case "connect", "move", "disconnect":
	default:
		return drivecontract.RelayStorageStatus{}, ErrRelayStorageAction
	}
	var v drivecontract.RelayStorageStatus
	if err := a.hub.do(ctx, http.MethodPost, "/v1/admin/storage/attachments", r, &v); err != nil {
		return drivecontract.RelayStorageStatus{}, err
	}
	if r.Action != "connect" {
		v.ConsentURL = ""
	}
	return checkRelayStorage(v)
}

// checkRelayStorage refuses an answer the page could only show wrongly: an
// unknown state, a negative count or a consent link that is not HTTPS.
func checkRelayStorage(v drivecontract.RelayStorageStatus) (drivecontract.RelayStorageStatus, error) {
	for _, n := range []int64{v.Local.Files, v.Local.Bytes, v.Remote.Files, v.Remote.Bytes, v.Pending.Files, v.Pending.Bytes,
		v.RemoteOnlyFiles, v.Migration.Files, v.Migration.Bytes, v.Migration.TotalFiles, v.Migration.TotalBytes} {
		if n < 0 {
			return drivecontract.RelayStorageStatus{}, errRelayStorageUnclear
		}
	}
	if !relayStorageStates[v.State] {
		return drivecontract.RelayStorageStatus{}, errRelayStorageUnclear
	}
	if v.ConsentURL != "" {
		if u, err := url.Parse(v.ConsentURL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return drivecontract.RelayStorageStatus{}, errRelayStorageUnclear
		}
	}
	return v, nil
}
