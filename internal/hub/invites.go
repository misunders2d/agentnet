package hub

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// inviteHintsSchema keeps, with each invitation an admin makes, the name
// written on it and when it was made, so admins can see and withdraw the
// unused ones (GET /v1/admin/invites). Appended after every shipped step.
const inviteHintsSchema = `
ALTER TABLE invites ADD COLUMN name TEXT;
ALTER TABLE invites ADD COLUMN created_at INTEGER;
`

// inviteLabelFallback is the label for a name that gives no usable letters
// (for example a name in another script): the Hub then counts on from it.
const inviteLabelFallback = "member"

// labelFromName makes a person label from the name an admin wrote on an
// invitation: lowercase ASCII letters and digits, other runs of characters
// as one dash, starting with a letter, at most protocol.MaxName long.
func labelFromName(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9' && b.Len() > 0:
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= protocol.MaxName {
			break
		}
	}
	label := strings.TrimRight(truncate(b.String(), protocol.MaxName), "-")
	if !protocol.ValidName(label) {
		return inviteLabelFallback
	}
	return label
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// freeInviteLabel returns base, or base-2, base-3 … when people already use
// it or an unused, unexpired invitation is waiting with it: a label made
// from a name never makes two people share addresses.
func (s *store) freeInviteLabel(base string, now time.Time) (string, error) {
	taken := map[string]bool{}
	rows, err := s.db.Query(`SELECT label FROM agents UNION SELECT label FROM invites WHERE used_by IS NULL AND expires_at > ?`, now.Unix())
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return "", err
		}
		taken[l] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if !taken[base] {
		return base, nil
	}
	return freeName(base, taken), nil
}

// createNamedInvite stores an invitation an admin made, with the name
// written on it ("" for none) and when it was made.
func (s *store) createNamedInvite(secret, label, name string, admin bool, ttl time.Duration, createdBy string, now time.Time) error {
	var n any
	if name != "" {
		n = name
	}
	_, err := s.db.Exec(`INSERT INTO invites(secret_hash, label, admin, expires_at, created_by, name, created_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		protocol.HashSecret(secret), label, admin, now.Add(ttl).Unix(), createdBy, n, now.Unix())
	return err
}

// pendingInviteWhere selects the invitations an admin can see and withdraw:
// made by an admin (not the Hub's bootstrap invite), not a device link's
// invite, unused and unexpired.
const pendingInviteWhere = `created_by IS NOT NULL AND person IS NULL AND used_by IS NULL AND expires_at > ?`

func (s *store) pendingInvites(now time.Time) ([]protocol.PendingInvite, error) {
	rows, err := s.db.Query(`SELECT secret_hash, label, coalesce(name, ''), admin, created_by, coalesce(created_at, 0), expires_at FROM invites WHERE `+
		pendingInviteWhere+` ORDER BY coalesce(created_at, 0) DESC, secret_hash`, now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.PendingInvite{}
	for rows.Next() {
		var p protocol.PendingInvite
		if err := rows.Scan(&p.ID, &p.Label, &p.Name, &p.Admin, &p.CreatedBy, &p.CreatedAt, &p.Expires); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// revokeInvite deletes one pending invitation (pendingInviteWhere); used,
// expired, bootstrap and device invitations are never touched.
func (s *store) revokeInvite(id string, now time.Time) error {
	res, err := s.db.Exec(`DELETE FROM invites WHERE secret_hash = ? AND `+pendingInviteWhere, id, now.Unix())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// handleInvites answers any member: whether its device may invite people,
// and, for an admin's device only, the unused invitations admins made. A
// member who is not an admin learns nothing about other people's roles or
// invitations.
func (h *Hub) handleInvites(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	out := protocol.PendingInvites{Invites: []protocol.PendingInvite{}}
	if a, err := h.store.agent(caller); err == nil && a.Admin && !a.Pending {
		out.CanInvite = true
		list, err := h.store.pendingInvites(time.Now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "", "storage error")
			return
		}
		out.Invites = list
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Hub) handleInviteRevoke(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req protocol.InviteRevokeRequest
	if err := decodeStrict(body, &req); err != nil || req.ID == "" {
		writeError(w, http.StatusBadRequest, "", "malformed request")
		return
	}
	err := h.store.revokeInvite(req.ID, time.Now())
	if errors.Is(err, errNotFound) || errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "", "no such unused invitation (it was used, expired or withdrawn)")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	h.cfg.Logf("invitation withdrawn by %s", caller)
	writeJSON(w, http.StatusOK, map[string]string{"revoked": req.ID})
}
