package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkspaceNameRules(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"Mellanni", "Mellanni", true},
		{"  Mellanni  ", "Mellanni", true},
		{"Café Ünïcode 工作", "Café Ünïcode 工作", true},
		{strings.Repeat("é", MaxWorkspaceName), strings.Repeat("é", MaxWorkspaceName), true},
		{strings.Repeat("a", MaxWorkspaceName+1), "", false},
		{"", "", false},
		{"   ", "", false},
		{"a\nb", "", false},
		{"a\tb", "", false},
		{"a\x7fb", "", false},
		{"a\u0085b", "", false}, // C1 (NEL)
		{"a\u009fb", "", false},
		{"bad\xffutf8", "", false},
	} {
		got, ok := ValidWorkspaceName(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ValidWorkspaceName(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A bad workspace name never drops the member list: Valid does not look at
// it, so receivers can ignore the name and keep the members.
func TestMembersValidIgnoresWorkspace(t *testing.T) {
	m := Members{Members: []Member{{Address: "a/b", Presence: PresenceConnected, Agent: true}}, Workspace: "bad\nname"}
	if err := m.Valid(); err != nil {
		t.Fatalf("Valid refused a list for its workspace name: %v", err)
	}
	data, _ := json.Marshal(Members{Members: []Member{{Address: "a/b", Presence: PresenceOffline}}})
	if strings.Contains(string(data), "workspace") || strings.Contains(string(data), "agent") {
		t.Fatalf("empty workspace and false agent must be omitted: %s", data)
	}
}

// The agent hint is never something a session reads: rm1 does not imply
// it, so no send decision can be made on it.
func TestCapAgentIsOnlyAHint(t *testing.T) {
	for _, c := range RoomImplies {
		if c == CapAgent {
			t.Fatal("rm1 must not imply the agent hint")
		}
	}
	rec := CapsRecord{Address: "a/b", Session: NewID(), Caps: []string{CapRoom}, TS: 1}
	if rec.Reads(CapAgent) {
		t.Fatal("a room reader must not count as running an agent")
	}
}
