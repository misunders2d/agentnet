package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTopicCLIReviewMoveUndoAndMerge(t *testing.T) {
	a, b, _, _, _ := dmWorld(t)
	send := func(body string) string {
		t.Helper()
		v, err := a.SendMessage(t.Context(), client.Outgoing{To: b.Address, Kind: envelope.KindMessage, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	source, destination := send("original source"), send("existing destination")
	call := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := runTopic(t.Context(), a, args, &out); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	count := func(want int) {
		t.Helper()
		var page client.TopicPage
		if err := json.Unmarshal(call("list", "--peer", b.Address), &page); err != nil || page.Matched != want {
			t.Fatalf("topics %+v err=%v want=%d", page, err, want)
		}
	}
	count(2)
	review := call("preview", "--peer", b.Address, "--to", destination, source)
	count(2)
	path := filepath.Join(t.TempDir(), "review.json")
	if err := os.WriteFile(path, review, 0600); err != nil {
		t.Fatal(err)
	}
	var moved client.ConvSent
	if err := json.Unmarshal(call("apply", path), &moved); err != nil {
		t.Fatal(err)
	}
	if moved.State != "stored" || !protocol.ValidID(moved.ID) {
		t.Fatalf("move misreports delivery: %+v", moved)
	}
	count(1)
	var replay client.ConvSent
	if err := json.Unmarshal(call("apply", path), &replay); err != nil || replay.ID != moved.ID {
		t.Fatalf("retry %+v %v", replay, err)
	}
	var display client.Conversation
	if err := json.Unmarshal(call("show", source), &display); err != nil || display.Peer != b.Address || len(display.Messages) != 2 {
		t.Fatalf("display %+v %v", display, err)
	}
	undoID := protocol.NewID()
	call("undo", "--peer", b.Address, "--id", undoID, moved.ID)
	call("undo", "--peer", b.Address, "--id", undoID, moved.ID)
	count(2)
	call("apply", path)
	count(2) // an old exact retry cannot repeat an undone effect
	call("merge", "--peer", b.Address, "--to", destination, source)
	count(1)
	third := send("another related original")
	count(2)
	var fresh client.TopicOrganizationReview
	raw := call("preview", "--peer", b.Address, "--new", "Build decisions", source, destination, third)
	if err := json.Unmarshal(raw, &fresh); err != nil || !fresh.New || fresh.Topic != fresh.Operation || fresh.Title != "Build decisions" {
		t.Fatalf("new topic review %+v %v", fresh, err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	call("apply", path)
	call("apply", path)
	count(1)
	if err := json.Unmarshal(call("show", fresh.Topic), &display); err != nil || len(display.Messages) != 3 {
		t.Fatalf("new topic navigation %+v %v", display, err)
	}
	call("undo", "--peer", b.Address, "--id", protocol.NewID(), fresh.Operation)
	count(2)
}
