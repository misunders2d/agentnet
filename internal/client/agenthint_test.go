package client

import (
	"slices"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// listedAgent reports whether a's member view lists address as running an
// agent.
func listedAgent(a *Agent, address string) (agent, listed bool) {
	for _, m := range a.MemberView().Members.Members {
		if m.Address == address {
			return m.Agent, true
		}
	}
	return false, false
}

// A device says it runs an agent only while a responder (or a named agent
// with one) is set: setting or removing it from another process on the
// same home makes the running daemon publish again through its local wake,
// and the other members follow without a reload. What they learned is kept
// for offline use.
func TestAgentHintFollowsResponder(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	eventually(t, "alice lists bob without an agent", func() bool {
		agent, listed := listedAgent(w.alice, w.bob.Address)
		return listed && !agent
	})
	cli, err := Open(w.bobHome) // a second process on bob's home, like the CLI
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice lists bob running an agent", func() bool {
		agent, _ := listedAgent(w.alice, w.bob.Address)
		return agent && slices.Contains(w.alice.AgentDevices(), w.bob.Address)
	})
	if slices.Contains(w.bob.AgentDevices(), w.bob.Address) {
		t.Fatal("a device lists itself among the other agent devices")
	}
	if !w.bob.AdvertisesAgent() {
		t.Fatal("bob runs an agent")
	}
	if err := cli.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice lists bob without an agent again", func() bool {
		agent, listed := listedAgent(w.alice, w.bob.Address)
		return listed && !agent && !slices.Contains(w.alice.AgentDevices(), w.bob.Address)
	})
	if err := cli.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the agent again", func() bool { return slices.Contains(w.alice.AgentDevices(), w.bob.Address) })
	// Kept offline: a fresh handle on alice's home, with no stream, still
	// knows bob runs an agent.
	reopened, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.AgentDevices(); !slices.Equal(got, []string{w.bob.Address}) {
		t.Fatalf("after reopen: %v", got)
	}
}

// Every caps record of a run gets a newer ts than the last, so a change
// within the same second as the waiting session's publish is not dropped
// by the relay (it keeps a session's record only when the ts is newer).
func TestAgentHintMonotonicPublish(t *testing.T) {
	w := newWorld(t, "")
	session := protocol.NewID()
	if err := w.bob.putCaps(tctx(t), session); err != nil {
		t.Fatal(err)
	}
	first := w.bob.capsPub.lastTS
	if err := w.bob.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if !w.bob.agentHintStale() {
		t.Fatal("the published hint should be stale after a responder was set")
	}
	if err := w.bob.putCaps(tctx(t), session); err != nil {
		t.Fatal(err)
	}
	if w.bob.capsPub.lastTS <= first {
		t.Fatalf("ts did not move on: %d then %d", first, w.bob.capsPub.lastTS)
	}
	m, err := w.alice.Members(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Members {
		if e.Address == w.bob.Address && !e.Agent {
			t.Fatal("the second record of the same second was dropped")
		}
	}
	if w.bob.agentHintStale() {
		t.Fatal("the hint was just published")
	}
	caps, agent := w.bob.advertisedCaps()
	if !agent || !slices.Contains(caps, protocol.CapAgent) || !slices.IsSorted(caps) || len(caps) > protocol.MaxAdvertisedCaps {
		t.Fatalf("advertised %v", caps)
	}
}

// The admin's workspace name reaches members on the members push and is
// kept offline; a pushed name that is not a workspace name is ignored and
// the member list kept.
func TestWorkspaceNameFromHub(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	if _, err := w.bob.SetWorkspaceName(tctx(t), "Mellanni"); err == nil {
		t.Fatal("a member named the workspace")
	}
	if got, err := w.alice.SetWorkspaceName(tctx(t), "  Mellanni "); err != nil || got != "Mellanni" {
		t.Fatalf("admin: %q %v", got, err)
	}
	if w.alice.WorkspaceName() != "Mellanni" {
		t.Fatal("the admin's own device does not keep the name at once")
	}
	eventually(t, "bob learns the name", func() bool { return w.bob.WorkspaceName() == "Mellanni" })
	if got, err := w.bob.HubWorkspace(tctx(t)); err != nil || got != "Mellanni" {
		t.Fatalf("hub workspace: %q %v", got, err)
	}
	if _, err := w.alice.SetWorkspaceName(tctx(t), "two\nlines"); err != ErrWorkspaceName {
		t.Fatalf("invalid name: %v", err)
	}
	w.bob.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}],"truncated":false,"workspace":"bad\nname"}`))
	if w.bob.WorkspaceName() != "Mellanni" {
		t.Fatalf("an invalid pushed name replaced the kept one: %q", w.bob.WorkspaceName())
	}
	if v := w.bob.MemberView(); !v.Current || len(v.Members.Members) != 1 {
		t.Fatalf("an invalid name dropped the list: %+v", v)
	}
	reopened, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.WorkspaceName() != "Mellanni" {
		t.Fatal("the name is not kept offline")
	}
	if _, err := w.alice.SetWorkspaceName(tctx(t), ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob learns it was cleared", func() bool { return w.bob.WorkspaceName() == "" })
}
