package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	gdrive "github.com/misunders2d/agentnet/internal/drivecontract"
)

// Append this named SQL step to schema in the owning store file. Never rewrite
// shipped migrations; migration index is assigned by the store owner.
const driveStorageSchema = `
CREATE TABLE drive_storage(
 id INTEGER PRIMARY KEY CHECK(id=1),
 config TEXT NOT NULL,
 revision INTEGER NOT NULL,
 set_by TEXT NOT NULL,
 set_at INTEGER NOT NULL);
INSERT INTO drive_storage(id,config,revision,set_by,set_at) VALUES(1,'{"enabled":false}',0,'',0);
`

var errDriveConfigConflict = errors.New("workspace Drive configuration changed; reload settings")

func (s *store) driveStorage(ctx context.Context) (gdrive.StorageSettings, error) {
	var v gdrive.StorageSettings
	var raw string
	e := s.db.QueryRowContext(ctx, `SELECT config,revision FROM drive_storage WHERE id=1`).Scan(&raw, &v.Revision)
	if e != nil {
		return v, e
	}
	if e = json.Unmarshal([]byte(raw), &v.Config); e != nil {
		return v, e
	}
	return v, v.Config.Validate()
}
func (s *store) setDriveStorage(ctx context.Context, u gdrive.StorageUpdate, by string) (gdrive.StorageSettings, error) {
	var v gdrive.StorageSettings
	if e := u.Config.Validate(); e != nil {
		return v, e
	}
	data, e := json.Marshal(u.Config)
	if e != nil {
		return v, e
	}
	res, e := s.db.ExecContext(ctx, `UPDATE drive_storage SET config=?,revision=revision+1,set_by=?,set_at=? WHERE id=1 AND revision=?`, string(data), by, time.Now().Unix(), u.Expect)
	if e != nil {
		return v, e
	}
	n, e := res.RowsAffected()
	if e != nil {
		return v, e
	}
	if n != 1 {
		return v, errDriveConfigConflict
	}
	return s.driveStorage(ctx)
}
func (h *Hub) handleDriveStorageGet(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	a, e := h.store.agent(caller)
	if e != nil {
		writeError(w, 500, "", "file storage settings unavailable")
		return
	}
	v, e := h.store.driveStorage(r.Context())
	if e != nil {
		writeError(w, 500, "", "file storage settings unavailable")
		return
	}
	v.CanAdmin = a.Admin
	writeJSON(w, 200, v)
}
func (h *Hub) handleDriveStoragePut(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var u gdrive.StorageUpdate
	if len(body) > 16<<10 || decodeStrict(body, &u) != nil {
		writeError(w, 400, "", "malformed file storage settings; only public project/client IDs and origins allowed")
		return
	}
	if e := u.Config.Validate(); e != nil {
		writeError(w, 400, "", e.Error())
		return
	}
	v, e := h.store.setDriveStorage(r.Context(), u, caller)
	if errors.Is(e, errDriveConfigConflict) {
		writeError(w, 409, "", e.Error())
		return
	}
	if e != nil {
		writeError(w, 500, "", "file storage settings could not be saved")
		return
	}
	v.CanAdmin = true
	writeJSON(w, 200, v)
}
