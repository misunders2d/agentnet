package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParticipationTopicScopeCanonical(t *testing.T) {
	old := vecInvite()
	raw, _ := json.Marshal(old)
	for _, topic := range []string{"", strings.Repeat("a", 32)} {
		var fields map[string]any
		json.Unmarshal(raw, &fields)
		fields["topic"] = topic
		scoped, _ := json.Marshal(fields)
		e, err := ParseParticipationEvent(scoped)
		if err != nil {
			t.Fatalf("explicit scope %q: %v", topic, err)
		}
		if e.Hash() == old.Hash() {
			t.Fatal("scope absent from signed identity")
		}
		e.Sign(vecKey())
		s := ScopeOf(e, e.TS)
		if !s.Projects(e) {
			t.Fatal("scope projection lost topic")
		}
		altered := s
		b, _ := json.Marshal(altered)
		var value map[string]any
		json.Unmarshal(b, &value)
		delete(value, "topic")
		b, _ = json.Marshal(value)
		json.Unmarshal(b, &altered)
		// Decode into a fresh value: JSON omission must mean whole chat.
		altered = ParticipationEvent{}
		json.Unmarshal(b, &altered)
		if altered.Projects(e) {
			t.Fatal("whole chat projection accepted for topic invite")
		}
	}
}
