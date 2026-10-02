package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gdrive "github.com/misunders2d/agentnet/internal/drivecontract"
)

func TestDriveStorageAdminConfigDefaultOffAndNoSecrets(t *testing.T) {
	h, id, addr := testHub(t)
	var exists int
	if e := h.store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='drive_storage'`).Scan(&exists); e != nil {
		t.Fatal(e)
	}
	if exists == 0 {
		if _, e := h.store.db.Exec(driveStorageSchema); e != nil {
			t.Fatal(e)
		}
	}
	get := func() gdrive.StorageSettings {
		w := httptest.NewRecorder()
		h.handleDriveStorageGet(w, signed(t, id, addr, "GET", "/v1/storage/drive", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var v gdrive.StorageSettings
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	initial := get()
	if initial.Config.Enabled || initial.CanAdmin || initial.Revision != 0 {
		t.Fatal("initial provider not off/member authority")
	}
	cfg := gdrive.StorageConfig{Enabled: true, Project: "agentnet-fixture", DesktopClientID: "123-fixture.apps.googleusercontent.com", APIConfirmed: true, ConsentConfirmed: true, ClientsConfirmed: true}
	raw, _ := json.Marshal(gdrive.StorageUpdate{Config: cfg, Expect: 0})
	w := httptest.NewRecorder()
	h.handleDriveStoragePut(w, signed(t, id, addr, "PUT", "/v1/admin/storage/drive", raw))
	if w.Code != http.StatusForbidden {
		t.Fatal("member enabled Drive", w.Code)
	}
	if _, e := h.store.db.Exec(`UPDATE agents SET admin=1 WHERE address=?`, addr); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	h.handleDriveStoragePut(w, signed(t, id, addr, "PUT", "/v1/admin/storage/drive", raw))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	v := get()
	if !v.Config.Enabled || !v.CanAdmin || v.Revision != 1 {
		t.Fatal("admin enable not persisted")
	}
	w = httptest.NewRecorder()
	h.handleDriveStoragePut(w, signed(t, id, addr, "PUT", "/v1/admin/storage/drive", raw))
	if w.Code != 409 {
		t.Fatal("stale config silently overwrote", w.Code)
	}
	bad := []byte(`{"config":{"enabled":false,"refresh_token":"PRIVATE"},"expect":1}`)
	w = httptest.NewRecorder()
	h.handleDriveStoragePut(w, signed(t, id, addr, "PUT", "/v1/admin/storage/drive", bad))
	if w.Code != 400 || strings.Contains(w.Body.String(), "PRIVATE") {
		t.Fatal("secret accepted/leaked")
	}
	if get().Revision != 1 {
		t.Fatal("malformed config mutated")
	}
	cfg.Enabled = false
	raw, _ = json.Marshal(gdrive.StorageUpdate{Config: cfg, Expect: 1})
	w = httptest.NewRecorder()
	h.handleDriveStoragePut(w, signed(t, id, addr, "PUT", "/v1/admin/storage/drive", raw))
	if w.Code != 200 || get().Config.Enabled {
		t.Fatal("disable failed")
	}
}
