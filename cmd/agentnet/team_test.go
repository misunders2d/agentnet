package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTeamCLIUsesStableIDsAndExplicitManagerOperations(t *testing.T) {
	id, person := protocol.NewID(), protocol.NewID()
	for _, args := range [][]string{
		{"create", "Support", "crew"}, {"rename", id, "New", "name"},
		{"join", id}, {"leave", id}, {"archive", id}, {"restore", id},
		{"remove", id, person}, {"manager-add", id, person}, {"manager-remove", id, person},
	} {
		c, err := parseTeamChange(args)
		if err != nil || c.Op != args[0] {
			t.Fatalf("parse %v: %+v %v", args, c, err)
		}
		if args[0] == "create" && (c.Team != "" || c.Name != "Support crew") {
			t.Fatal("create inferred identity from its name")
		}
		if args[0] == "rename" && c.Name != "New name" {
			t.Fatal("rename lost the display name")
		}
	}
	for _, args := range [][]string{
		nil, {"create"}, {"join", "Support"}, {"join", id, person},
		{"leave"}, {"manager-add", id, "Alice"}, {"manager-remove", id},
		{"remove", id, person, "extra"}, {"rename", "Support", "New name"}, {"invite", id},
	} {
		if _, err := parseTeamChange(args); err == nil {
			t.Fatalf("invalid or inferred authority accepted: %v", args)
		}
	}
	for _, policy := range []string{"last manager", "explicitly", "invites nobody", "no conversation history"} {
		if !strings.Contains(teamHelp, policy) {
			t.Fatalf("help omitted %q", policy)
		}
	}
}

func TestTeamCLIShowsDirectoryAndReviewableSnapshot(t *testing.T) {
	a, home := diagnosticAgent(t)
	if _, err := a.CreatePerson(context.Background(), "Alice"); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if err := runTeam(context.Background(), a, []string{"create", "Support"}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out.String())
	if len(fields) == 0 || !protocol.ValidID(fields[0]) {
		t.Fatalf("create did not show stable ID: %q", out.String())
	}
	id := fields[0]
	out.Reset()
	if err := runTeam(context.Background(), a, []string{"list"}, &out, &stderr); err != nil || !strings.Contains(out.String(), id) || !strings.Contains(out.String(), "manager") {
		t.Fatalf("list: %q %v", out.String(), err)
	}
	out.Reset()
	if err := runTeam(context.Background(), a, []string{"snapshot", id, id}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	var snapshot protocol.TeamSnapshot
	if err := json.Unmarshal(out.Bytes(), &snapshot); err != nil || len(snapshot.Sources) != 1 || len(snapshot.Persons) != 1 || snapshot.Sources[0].ID != id {
		t.Fatalf("review snapshot: %q %v", out.String(), err)
	}
	dispatched, err := diagnosticOutput(t, func() error { return run([]string{"--home", home, "team", "snapshot", id}) })
	if err != nil || json.Unmarshal([]byte(dispatched), &snapshot) != nil || len(snapshot.Sources) != 1 || snapshot.Sources[0].ID != id {
		t.Fatalf("command dispatch: %q %v", dispatched, err)
	}
}
