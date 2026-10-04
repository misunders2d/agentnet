package main

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

// group help has no run-together words.
func TestGroupHelpSpacing(t *testing.T) {
	if strings.Contains(groupHelp, "most64") || !strings.Contains(groupHelp, "at most 64 ") {
		t.Fatalf("group help: %q", groupHelp)
	}
}

// group request-file says in plain words which of its conditions failed:
// no such message here, a message that did not come with the group's
// history (its files download as they are), or no file at that index.
func TestRequestFileSaysWhy(t *testing.T) {
	id := strings.Repeat("1", 32)
	hist := client.ConvMessage{ID: id, History: true, SyncedFrom: "sergey/laptop", Attachments: []client.FileInfo{{Name: "q3.xlsx", BlobID: "history-0"}}}
	own := client.ConvMessage{ID: id, Attachments: []client.FileInfo{{Name: "q3.xlsx"}}}
	for _, tc := range []struct {
		msgs  []client.ConvMessage
		index int
		want  string
	}{
		{nil, 0, "no message " + id + " in this group here"},
		{[]client.ConvMessage{own}, 0, "did not come with this group's history"},
		{[]client.ConvMessage{hist}, 1, "has 1 file(s), numbered from 0"},
	} {
		if _, err := historyFile(tc.msgs, id, tc.index); err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "exact scoped") {
			t.Errorf("request-file %d of %d message(s): %v, want %q", tc.index, len(tc.msgs), err, tc.want)
		}
	}
	if f, err := historyFile([]client.ConvMessage{hist}, id, 0); err != nil || f.Name != "q3.xlsx" {
		t.Fatalf("the selected history file: %+v %v", f, err)
	}
}
