package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOwnCapabilitySignedByteBudget(t *testing.T) {
	key, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	caps := append(slices.Clone(ownCaps), protocol.CapAgent)
	slices.Sort(caps)
	want := []string{"agent1", "amr1", "ams1", "aorg1", "cau1", "cont1", "crs1", "ctl3", "drv1", "env2", "flw2", "gic2", "gpa1", "ha1", "hdl1", "hgg1", protocol.CapModelSyncV2, "notify1", "org1", "own2", "own3", "person2", "rd2", "rm1", "sg1", "tps1", "tss1", "ttl1", "typing1"}
	if !slices.Equal(caps, want) {
		t.Fatalf("advertised capabilities=%v, want %v", caps, want)
	}
	record := protocol.CapsRecord{Address: strings.Repeat("a", 32) + "/" + strings.Repeat("b", 32), Session: protocol.NewID(), Caps: caps, TS: 1790000000}
	record.Sign(key.Sign)
	if err := record.ValidateAdvertisement(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if protocol.MaxCapsRecord != 1024 || len(raw) > 1024 || len(caps) != 29 || protocol.MaxAdvertisedCaps != 29 || protocol.MaxCaps-len(caps) != 3 {
		t.Fatalf("caps=%d bytes=%d headroom=%d", len(caps), len(raw), protocol.MaxCaps-len(caps))
	}
	t.Logf("caps=%d signed bytes=%d/1024 headroom=%d", len(caps), len(raw), protocol.MaxCaps-len(caps))
	parsed, err := protocol.ParseCapsRecord(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = parsed.Verify(key.Public(record.Address).SignKey); err != nil {
		t.Fatal(err)
	}
}
