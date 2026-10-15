package ui

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestDeletionNoteProvesOnlyLocalCommit(t *testing.T) {
	for _, tc := range []struct{ state, want string }{
		{"queued", "Deletion queued for other devices."},
		{"waiting", "Deletion queued for other devices."},
		{"failed", "Some deletion copies could not be sent."},
		{"custody", "Other devices apply deletion when their copies arrive."},
		{"delivered", "Other devices apply deletion when their copies arrive."},
	} {
		t.Run(tc.state, func(t *testing.T) {
			note := deletionNote(client.ControlSent{State: tc.state, Skipped: []string{"peer cannot read it"}})
			for _, want := range []string{"Deleted here.", tc.want, "nothing running was stopped", "Not sent to 1 device(s)"} {
				if !strings.Contains(note, want) {
					t.Fatalf("note %q does not contain %q", note, want)
				}
			}
			if strings.Contains(note, "Deleted here and on devices") {
				t.Fatal("local queued result claimed remote deletion")
			}
		})
	}
}
