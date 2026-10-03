package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// dm show prints what people see now: an edited message's latest text,
// marked edited (never the text first sent), and a deleted message as
// deleted, without its files.
func TestDMShowPrintsEditsAndDeletions(t *testing.T) {
	var out bytes.Buffer
	printConvMessages(&out, []client.ConvMessage{
		{ID: strings.Repeat("1", 32), LID: strings.Repeat("a", 32), Dir: "in", From: "vitalii/desk", Kind: "message", State: "delivered", At: 1,
			Body: "ship 400 boxes", Controls: client.Controls{Edited: true, Revision: 1, Text: "ship 450 boxes"}},
		{ID: strings.Repeat("2", 32), LID: strings.Repeat("b", 32), Dir: "in", From: "vitalii/desk", Kind: "message", State: "delivered", At: 2,
			Body: "salaries attached", Controls: client.Controls{Deleted: true},
			Attachments: []client.FileInfo{{Name: "salaries.csv", Size: 10}}},
		{ID: strings.Repeat("3", 32), LID: strings.Repeat("c", 32), Dir: "in", From: "vitalii/desk", Kind: "message", State: "delivered", At: 3,
			Body: "unchanged"},
	})
	got := out.String()
	for _, want := range []string{"\n  ship 450 boxes (edited)\n", "\n  (deleted)\n", "\n  unchanged\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dm show lacks %q:\n%s", want, got)
		}
	}
	for _, stale := range []string{"400 boxes", "salaries", "unchanged (edited)"} {
		if strings.Contains(got, stale) {
			t.Fatalf("dm show still prints %q:\n%s", stale, got)
		}
	}
}

// dm list names a group by its title and members, and a DM this person
// only hosts (not a member) by both of its people, never as an empty
// "with" or as a DM with one of them.
func TestDMListNamesGroupsAndHostedDMs(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.Local).Unix()
	for _, tc := range []struct {
		c         client.ConversationInfo
		want, not string
	}{
		{client.ConversationInfo{ID: "g1", Kind: protocol.ConvKindGroup, Title: "Freight desk", Created: at,
			Members: []client.PersonInfo{{Label: "Anna"}, {Label: "Sergey"}, {Label: "Vitalii"}}},
			`g1  group "Freight desk" with "Anna", "Sergey", "Vitalii"  since 2026-10-03`, `with "" (, )`},
		{client.ConversationInfo{ID: "d1", Kind: protocol.ConvKindDM, Role: "visitor", Created: at,
			Peer:    client.PersonInfo{Label: "Vitalii", Address: "vitalii/desk", State: "pinned"},
			Members: []client.PersonInfo{{Label: "Sergey"}, {Label: "Vitalii"}}},
			`d1  between "Sergey" and "Vitalii" (you are not in it)  since 2026-10-03`, `with "Vitalii"`},
		{client.ConversationInfo{ID: "d2", Kind: protocol.ConvKindDM, Created: at,
			Peer: client.PersonInfo{Label: "Vitalii", Address: "vitalii/desk", State: "pinned"}},
			`d2  with "Vitalii" (vitalii/desk, pinned)  since 2026-10-03`, "between"},
	} {
		if got := convLine(tc.c); got != tc.want || strings.Contains(got, tc.not) {
			t.Errorf("dm list line: %q, want %q", got, tc.want)
		}
	}
}

// dm show of a conversation not held here is an error, not an empty
// success.
func TestDMShowUnknownConversationFails(t *testing.T) {
	a, _ := diagnosticAgent(t)
	for _, id := range []string{strings.Repeat("0", 64), "nonsense"} {
		var out bytes.Buffer
		if err := runDM(context.Background(), a, []string{"show", id}, &out); !errors.Is(err, client.ErrNoConversation) {
			t.Errorf("dm show %s: %v, printed %q", id, err, out.String())
		}
	}
}

// help dm says what happens to an assistant's reply without an emotion
// line: it is sent, shown neutral (agentjob.go, MEL-434); only a reply the
// agent marks for a person's decision is held for review.
func TestDMHelpSaysReplyWithoutEmotionIsNeutral(t *testing.T) {
	var out bytes.Buffer
	if err := printHelp(&out, []string{"dm"}); err != nil {
		t.Fatal(err)
	}
	help := strings.Join(strings.Fields(out.String()), " ")
	if strings.Contains(help, "without one, or when the agent says the person must decide, nothing is sent") || !strings.Contains(help, "shown neutral") {
		t.Fatalf("help dm misstates replies without an emotion line:\n%s", out.String())
	}
}
