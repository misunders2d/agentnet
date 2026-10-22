package ui

import (
	"context"
	"errors"
	"net/http"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/drivecontract"
)

// RelayStorageProvider is optional: a provider without this workspace's
// signed relay transport answers 404 rather than invent a relay status.
type RelayStorageProvider interface {
	RelayAttachmentStorage(context.Context) (drivecontract.RelayStorageStatus, error)
	ChangeRelayAttachmentStorage(context.Context, drivecontract.RelayStorageAction) (drivecontract.RelayStorageStatus, error)
}

func (l *Live) RelayAttachmentStorage(ctx context.Context) (drivecontract.RelayStorageStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, err := l.a.RelayAttachmentStorage(ctx)
	return v, relayStorageWords(err)
}

func (l *Live) ChangeRelayAttachmentStorage(ctx context.Context, r drivecontract.RelayStorageAction) (drivecontract.RelayStorageStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	v, err := l.a.ChangeRelayAttachmentStorage(ctx, r)
	return v, relayStorageWords(err)
}

// relayStorageWords keeps the Hub's own refusals (confirmation, setup and
// the disconnect count notice) and replaces anything else with a fixed
// sentence: writeErr logs unexplained errors, and these may carry provider
// or transport detail.
func relayStorageWords(err error) error {
	var he *client.HubError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, client.ErrRelayStorageAction):
		return Refuse("Choose connect, move or disconnect.")
	case errors.As(err, &he) && he.Status == http.StatusNotFound:
		return NotFound("Your server does not offer workspace attachment storage.")
	case errors.As(err, &he) && he.Status == http.StatusForbidden:
		return Refuse("Only an admin of your server can change workspace attachment storage.")
	case errors.As(err, &he) && (he.Status == http.StatusBadRequest || he.Status == http.StatusConflict || he.Status == http.StatusUpgradeRequired) && he.Msg != "":
		return Refuse(capitalize(he.Msg))
	}
	return Refuse("Workspace attachment storage could not be checked with your server. Try again.")
}

// relayStorage is GET and POST /api/storage/attachments: a read never
// changes anything; a change is exactly one of connect, move or disconnect.
func (s *Server) relayStorage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(RelayStorageProvider)
	if !ok {
		writeErr(w, NotFound("workspace attachment storage is not available here"))
		return
	}
	if r.Method == http.MethodGet {
		v, err := p.RelayAttachmentStorage(r.Context())
		writeResult(w, v, err)
		return
	}
	var change drivecontract.RelayStorageAction
	if !readJSON(w, r, &change) {
		return
	}
	v, err := p.ChangeRelayAttachmentStorage(r.Context(), change)
	writeResult(w, v, err)
}
