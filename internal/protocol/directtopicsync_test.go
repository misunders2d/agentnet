package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDirectTopicSyncStrictAndConvergent(t *testing.T) {
	p := DirectTopicPlacement{Peer: "alice/desk", ID: strings.Repeat("a", 32), Author: strings.Repeat("11111111-", 3) + "11111111", Topic: strings.Repeat("b", 32), Revision: 1, Writer: strings.Repeat("22222222-", 3) + "22222222"}
	p.Destination = DirectTopicDestination{Host: p.Peer, Fingerprint: p.Author}
	r := DirectTopicSync{V: 1, Person: strings.Repeat("c", 32), Roster: strings.Repeat("d", 64), Placements: []DirectTopicPlacement{p}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseDirectTopicSync(raw); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]func(*DirectTopicSync){
		"other scope":         func(r *DirectTopicSync) { r.Placements[0].Peer = strings.Repeat("e", 64) },
		"missing host":        func(r *DirectTopicSync) { r.Placements[0].Destination.Host = "" },
		"missing host key":    func(r *DirectTopicSync) { r.Placements[0].Destination.Fingerprint = "" },
		"invalid named agent": func(r *DirectTopicSync) { r.Placements[0].Destination.AgentID = "Pi" },
		"missing author":      func(r *DirectTopicSync) { r.Placements[0].Author = "" },
		"missing destination": func(r *DirectTopicSync) { r.Placements[0].Topic = "" },
		"missing writer":      func(r *DirectTopicSync) { r.Placements[0].Writer = "" },
		"zero revision":       func(r *DirectTopicSync) { r.Placements[0].Revision = 0 },
		"overflow":            func(r *DirectTopicSync) { r.Placements[0].Revision = MaxTopicTitleRevision + 1 },
		"duplicate":           func(r *DirectTopicSync) { r.Placements = append(r.Placements, r.Placements[0]) },
		"unbounded":           func(r *DirectTopicSync) { r.Placements = make([]DirectTopicPlacement, MaxTopicMarks+1) },
	} {
		t.Run(name, func(t *testing.T) {
			var copy DirectTopicSync
			_ = json.Unmarshal(raw, &copy)
			bad(&copy)
			data, _ := json.Marshal(copy)
			if _, err := ParseDirectTopicSync(data); err == nil {
				t.Fatal("accepted invalid placements")
			}
		})
	}
	if _, err := ParseDirectTopicSync(append(raw[:len(raw)-1], []byte(",\"unexpected\":true}")...)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if p.Newer(p) {
		t.Fatal("replay changes placement")
	}
	newer := p
	newer.Revision++
	if !newer.Newer(p) || p.Newer(newer) {
		t.Fatal("revision order")
	}
	newer = p
	newer.Writer = "ffffffff-ffffffff-ffffffff-ffffffff"
	if !newer.Newer(p) || p.Newer(newer) {
		t.Fatal("writer tie break")
	}
}
