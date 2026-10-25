package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTopicLocationSyncStrictAndCapability(t *testing.T) {
	p := TopicLocation{Scope: strings.Repeat("a", 64), Topic: NewID(), Destination: strings.Repeat("b", 64), DestinationTopic: "", Operation: NewID(), At: 1, Writer: "11111111-22222222-33333333-44444444", Revision: 1}
	r := TopicLocationSync{V: 2, Person: NewID(), Roster: strings.Repeat("c", 64), Locations: []TopicLocation{p}}
	raw, _ := json.Marshal(r)
	if _, e := ParseTopicLocationSync(raw); e != nil {
		t.Fatal(e)
	}
	if e := CheckTopicStateCarrier(raw); e != nil {
		t.Fatal(e)
	}
	if _, e := ParseTopicStateSync(raw); e == nil {
		t.Fatal("V1 parser weakened")
	}
	for name, mutate := range map[string]func(map[string]any){"unknown": func(v map[string]any) { v["extra"] = true }, "v1": func(v map[string]any) { v["v"] = 1 }, "missing": func(v map[string]any) { delete(v["locations"].([]any)[0].(map[string]any), "destination_topic") }, "duplicate": func(v map[string]any) { v["locations"] = append(v["locations"].([]any), v["locations"].([]any)[0]) }, "other-shape": func(v map[string]any) { v["marks"] = []any{} }, "bad-scope": func(v map[string]any) { v["locations"].([]any)[0].(map[string]any)["scope"] = "unknown" }} {
		t.Run(name, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal(raw, &v)
			mutate(v)
			b, _ := json.Marshal(v)
			if _, e := ParseTopicLocationSync(b); e == nil {
				t.Fatal("invalid V2 location accepted")
			}
		})
	}
	if !(CapsRecord{Caps: []string{CapTopicStateSyncV2}}).Reads(CapTopicStateSync) {
		t.Fatal("V2 cannot read V1 marks")
	}
	if (CapsRecord{Caps: []string{CapTopicStateSync}}).Reads(CapTopicStateSyncV2) {
		t.Fatal("V1 inferred V2 support")
	}
}
