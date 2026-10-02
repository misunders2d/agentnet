package ui

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBrowserPersonLabelEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/personlabel_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var result struct {
		Checks      int               `json:"checks"`
		Info        client.PersonInfo `json:"info"`
		Steps       []json.RawMessage `json:"steps"`
		OtherPerson string            `json:"other_person"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.Checks < 50 || len(result.Steps) != 3 {
		t.Fatalf("invalid person-label fixture result: %v\n%s", err, out)
	}
	var previous protocol.PersonRoster
	for i, raw := range result.Steps {
		r, err := protocol.ParsePersonRoster(raw)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err = r.VerifyFirst(); err != nil {
				t.Fatal(err)
			}
		} else if _, err = r.VerifyNext(previous); err != nil {
			t.Fatal("Go refuses browser signed rename", err)
		}
		previous = r
	}
	info := result.Info
	if info.Person != previous.Person || info.Person == result.OtherPerson || info.Label != previous.Label || info.Roster != previous.Hash() || info.Seq != previous.Seq || info.State != "self" || len(info.Devices) != len(previous.Devices) {
		t.Fatal("browser response differs from native PersonInfo")
	}
	for i, device := range previous.Devices {
		if info.Devices[i].Address != device.Address || info.Devices[i].Fingerprint != device.Fingerprint() || info.Devices[i].Added != int64(i) {
			t.Fatal("browser changed device/key metadata")
		}
	}
	if !previous.Has(info.Address, info.Fingerprint) {
		t.Fatal("response signer is not in accepted head")
	}
	t.Logf("real-key engine checks: %d; Go verified complete linked/label chain", result.Checks)
}
