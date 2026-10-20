package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Repeating valid discovery data changes no directory evidence. Actual
// catalog changes, including only the separately signed program, still wake
// readers. Validation remains ahead of the comparison.
func TestAgentCatalogPublicationChanges(t *testing.T) {
	h, _, _ := testHub(t)
	host := enroll(t, h, "catalog-host")
	empty := []protocol.AgentRecord{}
	put := func(what string, body any, status int, wake int64, want []protocol.AgentRecord) {
		t.Helper()
		before := h.membersGen.Load()
		if code, raw := host.call(t, h, "PUT", "/v1/agent-catalog", body); code != status {
			t.Fatalf("%s: status %d, want %d: %s", what, code, status, raw)
		}
		if delta := h.membersGen.Load() - before; delta != wake {
			t.Errorf("%s: directory wakes=%d, want %d", what, delta, wake)
		}
		code, raw := host.call(t, h, "GET", "/v1/agents/catalog-host/x/agent-catalog", nil)
		var got []protocol.AgentRecord
		if code != http.StatusOK || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: catalog changed incorrectly: status=%d records=%s", what, code, raw)
		}
	}
	put("first empty publication", empty, http.StatusNoContent, 1, empty)
	put("same empty publication", empty, http.StatusNoContent, 0, empty)
	put("same decoded empty publication", []byte("[ \n ]"), http.StatusNoContent, 0, empty)
	pi := protocol.AgentRecord{V: 1, ID: protocol.NewID(), Host: host.addr, HostKey: host.id.Public(host.addr).Fingerprint(), Label: "Work", TS: 1700000000, Harness: "pi"}
	pi.Sign(host.id.Sign)
	put("first configured agent", []protocol.AgentRecord{pi}, http.StatusNoContent, 1, []protocol.AgentRecord{pi})
	put("same configured agent", []protocol.AgentRecord{pi}, http.StatusNoContent, 0, []protocol.AgentRecord{pi})
	claude := pi
	claude.Harness = "claude"
	claude.Sign(host.id.Sign)
	if !bytes.Equal(pi.Canonical(), claude.Canonical()) || bytes.Equal(pi.HarnessSig, claude.HarnessSig) {
		t.Fatal("fixture did not isolate the separately signed harness extension")
	}
	put("harness extension changed", []protocol.AgentRecord{claude}, http.StatusNoContent, 1, []protocol.AgentRecord{claude})
	put("same harness extension", []protocol.AgentRecord{claude}, http.StatusNoContent, 0, []protocol.AgentRecord{claude})
	second := protocol.AgentRecord{V: 1, ID: protocol.NewID(), Host: host.addr, HostKey: pi.HostKey, Label: "Review", TS: pi.TS, Harness: "codex"}
	second.Sign(host.id.Sign)
	put("another concrete identity", []protocol.AgentRecord{claude, second}, http.StatusNoContent, 1, []protocol.AgentRecord{claude, second})
	put("catalog order changed", []protocol.AgentRecord{second, claude}, http.StatusNoContent, 1, []protocol.AgentRecord{second, claude})
	put("same catalog order", []protocol.AgentRecord{second, claude}, http.StatusNoContent, 0, []protocol.AgentRecord{second, claude})
	put("configured agents removed", empty, http.StatusNoContent, 1, empty)
	put("same removed catalog", empty, http.StatusNoContent, 0, empty)
	badExtension := claude
	badExtension.HarnessSig = append([]byte(nil), claude.HarnessSig...)
	badExtension.HarnessSig[0] ^= 1
	put("invalid extension", []protocol.AgentRecord{badExtension}, http.StatusBadRequest, 0, empty)
	put("duplicate identity", []protocol.AgentRecord{claude, claude}, http.StatusBadRequest, 0, empty)
	other := enroll(t, h, "other-catalog-host")
	wrongKey := claude
	wrongKey.HostKey = other.id.Public(other.addr).Fingerprint()
	wrongKey.Sign(host.id.Sign)
	put("wrong exact host key", []protocol.AgentRecord{wrongKey}, http.StatusBadRequest, 0, empty)
	wrongSigner := claude
	wrongSigner.Sign(other.id.Sign)
	put("another device signature", []protocol.AgentRecord{wrongSigner}, http.StatusBadRequest, 0, empty)
}

// Even an empty catalog is scoped to the authenticated host key. Replacing
// that exact key changes discovery; the old key cannot publish afterwards.
func TestAgentCatalogPublicationUsesExactHostKey(t *testing.T) {
	h, _, _ := testHub(t)
	host := enroll(t, h, "catalog-rekey")
	put := func(caller member, status int, wake int64) {
		t.Helper()
		before := h.membersGen.Load()
		if code, raw := caller.call(t, h, "PUT", "/v1/agent-catalog", []protocol.AgentRecord{}); code != status {
			t.Fatalf("status %d, want %d: %s", code, status, raw)
		}
		if delta := h.membersGen.Load() - before; delta != wake {
			t.Errorf("directory wakes=%d, want %d", delta, wake)
		}
	}
	put(host, http.StatusNoContent, 1)
	replacement, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public := replacement.Public(host.addr)
	raw, _ := json.Marshal(public)
	if _, err = h.store.db.Exec(`UPDATE agents SET public=? WHERE address=?`, string(raw), host.addr); err != nil {
		t.Fatal(err)
	}
	current := member{id: replacement, addr: host.addr}
	put(current, http.StatusNoContent, 1)
	put(current, http.StatusNoContent, 0)
	put(host, http.StatusUnauthorized, 0)
	var key string
	if err = h.store.db.QueryRow(`SELECT host_key FROM agent_catalogs WHERE host=?`, host.addr).Scan(&key); err != nil || key != public.Fingerprint() {
		t.Fatalf("catalog crossed the host-key boundary: %s %v", key, err)
	}
}
