package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Two explicit people consent through existing native signed state operations.
// Production grp1 remains off: this checks binding, not peer group delivery.
func TestLiveGroupReplyReceiverForwardAndRefusalKeepsFile(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, hubDir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	runDaemon(t, a)
	if _, err = a.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	code, err := a.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	person, err := bob.CreatePerson(ctx, "Bob")
	if err != nil {
		t.Fatal(err)
	}
	packet, err := a.CreateGroup(ctx, "Selected receiver group")
	if err != nil {
		t.Fatal(err)
	}
	admission, err := bob.SignGroupAdmission(packet.Root, 1, packet.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	packet.Proof = nil
	packet.State.Seq, packet.State.Prev = 1, packet.State.Hash()
	packet.State.Members = append(slices.Clone(packet.State.Members), protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.Person, Roster: person.Roster}, Admission: admission})
	slices.SortFunc(packet.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	packet, err = a.SignGroupState(ctx, packet)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := a.BuildGroupCommit(ctx, packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.PublishGroup(ctx, commit, packet); err != nil {
		t.Fatal(err)
	}
	l := NewLive(a)
	conv := packet.State.Conv
	nativeFile := filepath.Join(t.TempDir(), "native.jsonl")
	if err = os.WriteFile(nativeFile, []byte(`{"type":"session","id":"group-provider-fixture","version":3}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner, err := a.RegisterReplySession(client.ReplySessionRegistration{Harness: "pi", SessionID: "group-provider-fixture", File: nativeFile, Label: "Exact local receiver"})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.CloseReplySession(client.ReplySessionCall{Handle: owner.Handle, Generation: owner.Generation, OwnerToken: owner.OwnerToken, SessionID: "group-provider-fixture", File: nativeFile}); err != nil {
		t.Fatal(err)
	}
	fileID, err := l.StageFile("kept.txt", bytes.NewBufferString("EXACT_GROUP_PROVIDER_BYTES"))
	if err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range []*ReplyReceiverSelection{
		{Kind: "live_session", SessionHandle: "unknown-local-handle"},
		{Kind: "managed_agent", AgentID: strings.Repeat("f", 32), Instructions: "original local instruction", Mode: "question"},
	} {
		_, e := l.SendDM(DMDraft{Conv: conv, Body: "unavailable receiver", Files: []string{fileID}, ReplyReceiver: unavailable})
		if !errors.Is(e, ErrRefused) {
			t.Fatalf("unavailable selection: %v", e)
		}
		if _, ok := l.staged.files[fileID]; !ok {
			t.Fatal("receiver refusal consumed staged file")
		}
	}
	selection := &ReplyReceiverSelection{Kind: "live_session", SessionHandle: owner.Handle}
	sent, err := l.SendDM(DMDraft{Conv: conv, Body: "original group request", Files: []string{fileID}, ReplyReceiver: selection})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := l.ReplyReceiverBindings()
	if err != nil || len(rows) != 1 {
		t.Fatalf("exact bindings: %+v %v", rows, err)
	}
	msgs, err := a.ConversationMessages(conv)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("group request: %+v %v", msgs, err)
	}
	b := rows[0]
	if msgs[0].ID != sent.ID || b.Conv != conv || b.RequestRef != msgs[0].LID || b.Receiver.SessionHandle != owner.Handle || b.Receiver.Kind != "live_session" || b.State != "pending" {
		t.Fatalf("exact original request not bound: %+v %+v", b, msgs[0])
	}
	if _, ok := l.staged.files[fileID]; ok {
		t.Fatal("successful send retained consumed staging")
	}
}
