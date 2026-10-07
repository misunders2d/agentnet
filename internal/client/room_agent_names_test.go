package client

import (
	"context"
	"testing"
)

func TestRoomAgentNamesMatchSignedCatalog(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	first, err := w.alice.CreateLocalAgent("Pi", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.alice.CreateLocalAgent("Codex", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	parts := []ParticipationInfo{
		{PID: "pi-pid", State: PartActive, HostHere: true, AgentID: first.ID, Host: PersonInfo{Address: w.alice.Address}},
		{PID: "codex-pid", State: PartActive, HostHere: true, AgentID: second.ID, Host: PersonInfo{Address: w.alice.Address}},
		{PID: "unknown-pid", State: PartActive, HostHere: true, AgentID: "missing", Host: PersonInfo{Address: w.alice.Address}},
	}
	got := w.alice.roomAgentNames(context.Background(), parts)
	if got["pi-pid"] != "Pi" || got["codex-pid"] != "Codex" || got["unknown-pid"] != "name unavailable; use verified PID" {
		t.Fatalf("names=%v", got)
	}
}
