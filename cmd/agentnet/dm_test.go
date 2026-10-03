package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
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
