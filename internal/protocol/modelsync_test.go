package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestModelSyncStrictReportedMetadata(t *testing.T) {
	report := AgentModel{AgentID: NewID(), Model: "gpt-6.1-sol", Harness: "codex", Executor: strings.Repeat("a", 64), At: 1, Revision: 1}
	record := ModelSync{V: 1, Person: NewID(), Roster: strings.Repeat("b", 64), Reports: []AgentModel{report}}
	raw, _ := json.Marshal(record)
	if _, err := ParseModelSync(raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", " leading", "trailing ", "foo\nbar", "foo\x85bar", strings.Repeat("x", 121), string([]byte{0xff})} {
		if ValidReportedModel(name) {
			t.Fatalf("accepted invalid model %q", name)
		}
	}
	for _, name := range []string{"gpt-6.1-sol", "Claude Opus 4.6", strings.Repeat("型", 120)} {
		if !ValidReportedModel(name) {
			t.Fatalf("rejected report %q", name)
		}
	}
	for _, bad := range []string{strings.Replace(string(raw), `"model":"gpt-6.1-sol"`, `"model":"gpt-6.1-sol","effort":"high"`, 1), strings.Replace(string(raw), `"revision":1`, `"revision":0`, 1), strings.Replace(string(raw), `"at":1`, `"at":9007199254740992`, 1)} {
		if _, err := ParseModelSync([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	record.Reports = append(record.Reports, report)
	raw, _ = json.Marshal(record)
	if _, err := ParseModelSync(raw); err == nil {
		t.Fatal("duplicate exact agent")
	}
	record.Reports = []AgentModel{report}
	record.Reports[0].AgentID = ""
	raw, _ = json.Marshal(record)
	if _, err := ParseModelSync(raw); err != nil {
		t.Fatal("default agent", err)
	}
}
