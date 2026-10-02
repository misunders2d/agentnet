package client

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

var (
	ErrRealmUnknown     = errors.New("this home has not recorded a workspace identity")
	ErrRealmUnsupported = errors.New("this Hub does not report a workspace identity; it remains unknown")
	ErrRealmInvalid     = errors.New("invalid workspace identity: expected a canonical 128-bit identifier")
)

// RealmChangedError refuses to replace this home's previously recorded
// namespace. It is not evidence that a changed endpoint or key is trusted.
type RealmChangedError struct{ Pinned, Offered string }

func (e *RealmChangedError) Error() string {
	return fmt.Sprintf("workspace identity changed (recorded %s, offered %s); this home's identity was not changed", e.Pinned, e.Offered)
}

// RealmID reads this home's recorded namespace without contacting the Hub.
// It does not assert current connectivity, membership or endpoint trust.
func (a *Agent) RealmID() (string, error) { return a.store.realmID() }

// CheckRealm explicitly asks the existing TLS-verified Hub connection once,
// records a first identity, or checks it against this home's existing one.
// It never merges homes, changes keys/grants or polls in the background.
func (a *Agent) CheckRealm(ctx context.Context) (string, error) {
	if _, err := a.RealmID(); err != nil && !errors.Is(err, ErrRealmUnknown) {
		return "", err
	}
	var v protocol.VersionInfo
	if err := a.hub.do(ctx, http.MethodGet, "/v1/version", nil, &v); err != nil {
		return "", fmt.Errorf("check workspace identity: %w", err)
	}
	if v.Protocol != protocol.ProtocolVersion {
		return "", fmt.Errorf("the Hub speaks protocol %d and this agentnet %d; workspace identity was not recorded", v.Protocol, protocol.ProtocolVersion)
	}
	if err := a.store.recordRealm(v.RealmID); err != nil {
		return "", err
	}
	return v.RealmID, nil
}

func (s *store) realmID() (string, error) {
	id, err := s.config("realm_id")
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrRealmUnknown
	}
	if err != nil {
		return "", err
	}
	if !validRealmID(id) {
		return "", ErrRealmInvalid
	}
	return id, nil
}

// recordRealm is the join seam for a VersionInfo learned over the existing
// verified connection. The caller must first check its protocol generation;
// absent realm_id is compatible with old relays and must be skipped by join.
// Serialize first-record decisions across Agent instances of the same home.
func (s *store) recordRealm(id string) error {
	if id == "" {
		return ErrRealmUnsupported
	}
	if !validRealmID(id) {
		return ErrRealmInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pinned string
	err = tx.QueryRow(`SELECT v FROM config WHERE k = 'realm_id'`).Scan(&pinned)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO config(k, v) VALUES('realm_id', ?)`, id); err != nil {
			return err
		}
		return tx.Commit()
	case err != nil:
		return err
	case !validRealmID(pinned):
		return ErrRealmInvalid
	case pinned != id:
		return &RealmChangedError{Pinned: pinned, Offered: id}
	default:
		return nil
	}
}

func validRealmID(id string) bool {
	if len(id) != 32 {
		return false
	}
	raw, err := hex.DecodeString(id)
	return err == nil && hex.EncodeToString(raw) == id
}
