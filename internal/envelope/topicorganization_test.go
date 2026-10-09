package envelope

import (
	"strings"
	"testing"
)

func TestTopicOrganizationShape(t *testing.T) {
	valid := func() Inner {
		return Inner{V: Version2, Kind: KindMessage, Topic: strings.Repeat("a", 32), TopicEvent: &TopicEvent{Action: "move", Moves: []TopicMove{{LID: strings.Repeat("b", 32), Author: "11111111-22222222-33333333-44444444", Hash: strings.Repeat("c", 64)}}}}
	}
	if err := CheckTopic(valid()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Inner){
		"agent request": func(in *Inner) { in.PID = strings.Repeat("d", 32) },
		"agent close":   func(in *Inner) { in.TopicDone = true },
		"private scope": func(in *Inner) { in.Human = &HumanTurn{} },
		"followup": func(in *Inner) {
			in.Followup = &Ref{ID: strings.Repeat("d", 32), Fingerprint: in.TopicEvent.Moves[0].Author}
		},
		"duplicate":           func(in *Inner) { in.TopicEvent.Moves = append(in.TopicEvent.Moves, in.TopicEvent.Moves[0]) },
		"same destination":    func(in *Inner) { in.TopicEvent.Moves[0].Topic = in.Topic },
		"unbounded selection": func(in *Inner) { in.TopicEvent.Moves = make([]TopicMove, MaxTopicMoves+1) },
		"wrong merge source":  func(in *Inner) { in.TopicEvent.Action = "merge"; in.TopicEvent.Merge = strings.Repeat("d", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			in := valid()
			change(&in)
			if CheckTopic(in) == nil {
				t.Fatal("accepted unrelated or malformed organization fields")
			}
		})
	}
}
