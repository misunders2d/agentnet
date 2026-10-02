package hub

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Agent catalog is discovery data in the existing Hub store, not grants or
// execution configuration. Each immutable identity record is host-signed.
const agentCatalogSchema = `
CREATE TABLE agent_catalogs(
 host TEXT PRIMARY KEY,
 host_key TEXT NOT NULL,
 records TEXT NOT NULL,
 updated_at INTEGER NOT NULL);
`

func (h *Hub) handleAgentCatalogPut(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	member, err := h.store.agent(caller)
	if err != nil {
		writeError(w, 500, "", "agent catalog unavailable")
		return
	}
	var records []protocol.AgentRecord
	if len(body) > 64<<10 || decodeStrict(body, &records) != nil || len(records) > protocol.MaxAgentCatalog {
		writeError(w, 400, "", "invalid agent catalog")
		return
	}
	seen := map[string]bool{}
	for _, record := range records {
		if seen[record.ID] || record.Verify(member.Public) != nil {
			writeError(w, 400, "", "agent catalog must contain distinct identities signed by caller's exact device key")
			return
		}
		seen[record.ID] = true
	}
	raw, _ := json.Marshal(records)
	_, err = h.store.db.ExecContext(r.Context(), `INSERT INTO agent_catalogs(host,host_key,records,updated_at) VALUES(?,?,?,?)
 ON CONFLICT(host) DO UPDATE SET host_key=excluded.host_key,records=excluded.records,updated_at=excluded.updated_at`, caller, member.Public.Fingerprint(), string(raw), time.Now().Unix())
	if err != nil {
		writeError(w, 500, "", "agent catalog unavailable")
		return
	}
	h.membersChanged() // push directory refresh, no polling or job
	w.WriteHeader(http.StatusNoContent)
}
func (h *Hub) handleAgentCatalogGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	host := protocol.Address(r.PathValue("label"), r.PathValue("agent"))
	member, err := h.store.agent(host)
	if err != nil || member.Revoked || member.Pending {
		writeError(w, 404, "", "unknown catalog host")
		return
	}
	records := make([]protocol.AgentRecord, 0)
	var raw, key string
	err = h.store.db.QueryRowContext(r.Context(), `SELECT records,host_key FROM agent_catalogs WHERE host=?`, host).Scan(&raw, &key)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, 500, "", "agent catalog unavailable")
		return
	}
	if err == nil && key == member.Public.Fingerprint() {
		if json.Unmarshal([]byte(raw), &records) != nil {
			writeError(w, 500, "", "agent catalog unavailable")
			return
		}
	}
	writeJSON(w, 200, records)
}
