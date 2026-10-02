package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func storageView(t *testing.T, h *Hub, caller member, path string) (protocol.HubStorage, string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.handleStorage(w, signed(t, caller.id, caller.addr, http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("storage: %d %s", w.Code, w.Body)
	}
	var view protocol.HubStorage
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view, w.Body.String()
}

func TestStorageSummaryCallerIsolationAndAdmin(t *testing.T) {
	h, alice, bob, carol := blobHub(t, 4096)
	h.cfg.UploadTTL = 2 * time.Hour // synthetic running config
	aID := alice.upload(t, h, bob.addr, []byte(strings.Repeat("a", 20)))
	cID := carol.upload(t, h, bob.addr, []byte(strings.Repeat("c", 70)))
	partial := func(owner member, size, received int) string {
		t.Helper()
		id := protocol.NewID()
		if code, body := owner.call(t, h, http.MethodPost, "/v1/blobs", protocol.BlobReserve{ID: id, Recipient: bob.addr, Size: int64(size), SHA256: digest(nil)}); code != http.StatusOK {
			t.Fatalf("reserve: %d %s", code, body)
		}
		if code, body := owner.call(t, h, http.MethodPut, "/v1/blobs/"+id+"?offset=0", []byte(strings.Repeat("p", received))); code != http.StatusOK {
			t.Fatalf("partial: %d %s", code, body)
		}
		return id
	}
	aPartial := partial(alice, 100, 20)
	cPartial := partial(carol, 200, 30)
	reclaiming := protocol.NewID()
	if _, err := h.store.reserveBlob(alice.addr, protocol.BlobReserve{ID: reclaiming, Recipient: bob.addr, Size: 9, SHA256: digest(nil)}, h.cfg.StorageQuota); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.Exec(`UPDATE blobs SET state = ?, updated_at = 1 WHERE id = ?`, blobReclaiming, reclaiming); err != nil {
		t.Fatal(err)
	}
	ids := []string{aID, cID, aPartial, cPartial, reclaiming}
	before := make(map[string]blobRow)
	files := make(map[string][]byte)
	for _, id := range ids {
		row, err := h.store.blob(id)
		if err != nil {
			t.Fatal(err)
		}
		before[id] = row
		path := h.blobPath(id, row.State == protocol.BlobStored)
		if data, err := os.ReadFile(path); err == nil {
			files[path] = data
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	view, raw := storageView(t, h, alice, "/v1/storage?owner="+carol.addr)
	if view.Scope != "caller-owned-ciphertext" || view.QuotaScope != "hub-global" || view.Global != nil || strings.Contains(raw, `"global"`) {
		t.Fatalf("member/global scope confused: %s", raw)
	}
	if view.Own.Stored != (protocol.StorageBucket{Files: 1, ReservedBytes: 20, RecordedReceivedBytes: 20}) || view.Own.Incomplete != (protocol.StorageBucket{Files: 2, ReservedBytes: 109, RecordedReceivedBytes: 20}) {
		t.Fatalf("wrong own reservations/progress: %+v", view.Own)
	}
	for _, secret := range append(ids, carol.addr, h.cfg.DataDir) {
		if strings.Contains(raw, secret) {
			t.Fatalf("summary disclosed private identifier/path %q", secret)
		}
	}
	if view.QuotaBytes != 4096 || view.MaxFileBytes != 1<<20 || view.UploadIdleTTLSeconds != (2*time.Hour).Seconds() {
		t.Fatalf("configured policy not reported: %+v", view)
	}
	if view.Policy.ManualDeliveredAgeDefaultSeconds != (720*time.Hour).Seconds() || view.Policy.ManualUnattachedAgeDefaultSeconds != (24*time.Hour).Seconds() || !strings.Contains(view.Policy.IncompleteUploads, "opportunistically") || !strings.Contains(view.Policy.DeliveredAttachments, "no automatic expiry") || !strings.Contains(view.Policy.MessageEnvelopes, "No automatic deletion") {
		t.Fatalf("invented retention policy: %+v", view.Policy)
	}
	other, _ := storageView(t, h, carol, "/v1/storage")
	if other.Global != nil || other.Own.Stored.ReservedBytes != 70 || other.Own.Incomplete.ReservedBytes != 200 {
		t.Fatalf("other sender saw wrong usage: %+v", other)
	}
	recipient, _ := storageView(t, h, bob, "/v1/storage")
	if recipient.Own != (protocol.BlobStorageUsage{}) {
		t.Fatalf("recipient's storage conflated with ownership: %+v", recipient.Own)
	}
	adminLooking := enroll(t, h, "admin")
	pretend, _ := storageView(t, h, adminLooking, "/v1/storage")
	if pretend.Global != nil {
		t.Fatal("address label granted aggregate visibility")
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.createInvite("storage-admin", "ops", true, time.Hour, alice.addr); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.store.enroll("storage-admin", id.Public("ops/worker"), "ops", nil); err != nil {
		t.Fatal(err)
	}
	admin, _ := storageView(t, h, member{id: id, addr: "ops/worker"}, "/v1/storage")
	if admin.Global == nil || admin.Global.Stored != (protocol.StorageBucket{Files: 2, ReservedBytes: 90, RecordedReceivedBytes: 90}) || admin.Global.Incomplete != (protocol.StorageBucket{Files: 3, ReservedBytes: 309, RecordedReceivedBytes: 50}) || admin.Own != (protocol.BlobStorageUsage{}) {
		t.Fatalf("verified admin aggregate wrong: %+v", admin)
	}
	for _, blob := range ids {
		after, err := h.store.blob(blob)
		if err != nil || !reflect.DeepEqual(before[blob], after) {
			t.Fatalf("inspection mutated/reclaimed blob %s: %+v %v", blob, after, err)
		}
	}
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("inspection changed blob file: %v", err)
		}
	}
}

func TestStorageSummaryRequiresAuthenticatedGET(t *testing.T) {
	h, id, addr := testHub(t)
	for _, tc := range []struct {
		request *http.Request
		want    int
	}{
		{httptest.NewRequest(http.MethodGet, "/v1/storage", nil), http.StatusUnauthorized},
		{signed(t, id, addr, http.MethodPost, "/v1/storage", nil), http.StatusMethodNotAllowed},
	} {
		w := httptest.NewRecorder()
		h.handleStorage(w, tc.request)
		if w.Code != tc.want || strings.Contains(w.Body.String(), "quota_bytes") {
			t.Fatalf("unauthorized storage disclosed: %d %s", w.Code, w.Body)
		}
	}
}
