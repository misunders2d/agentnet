package client

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTopicOrganizationNoticeTitle(t *testing.T) {
	topic := protocol.NewID()
	if got := NewTopicOrganizationNotice(topic, "Fixture organization"); got != "Moved selected messages to new topic “Fixture organization”." {
		t.Fatal("explicit new-topic notice wording changed", got)
	}
	for _, title := range []string{"Fixture organization", "Розбір помилки", "embedded” quote", strings.Repeat("界", TopicTitleMax)} {
		if got := topicOrganizationNoticeTitle(topic, NewTopicOrganizationNotice(topic, title)); got != title {
			t.Fatalf("round trip %q: %q", title, got)
		}
	}
	for _, body := range []string{
		TopicOrganizationNotice("", ""), TopicOrganizationNotice(topic, ""), TopicOrganizationNotice(topic, "Legacy private name"), "unrelated prose",
		"Moved selected messages to new topic “”.", "Moved selected messages to new topic “ spaced ”.",
		"Moved selected messages to new topic “two  spaces”.", "Moved selected messages to new topic “line\nbreak”.",
		"Moved selected messages to new topic “bid\u202ei”.", "Moved selected messages to new topic “broken suffix”",
		"Moved selected messages to new topic “" + strings.Repeat("界", TopicTitleMax+1) + "”.",
		NewTopicOrganizationNotice(topic, "Named") + " more",
	} {
		if got := topicOrganizationNoticeTitle(topic, body); got != "" {
			t.Fatalf("noncanonical notice %q supplied title %q", body, got)
		}
	}
	if got := topicOrganizationNoticeTitle("", NewTopicOrganizationNotice(topic, "Named")); got != "" {
		t.Fatal("Main acquired a topic name", got)
	}
}

func TestAgentOrganizationAutomaticTitle(t *testing.T) {
	conv, topic := strings.Repeat("c", 64), protocol.NewID()
	first := ConvMessage{ID: protocol.NewID(), LID: protocol.NewID(), Kind: envelope.KindMessage, Body: "Original automatic name", Topic: topic, Sent: 1}
	notice := ConvMessage{ID: protocol.NewID(), LID: protocol.NewID(), Kind: envelope.KindMessage, Topic: topic, Sent: 2, PID: protocol.NewID(), Origin: envelope.OriginAgentOrganization, Body: NewTopicOrganizationNotice(topic, "Fixture organization"), TopicEvent: &envelope.TopicEvent{Action: "move", Request: &envelope.TopicRequest{LID: protocol.NewID()}}}
	later := notice
	later.ID, later.LID, later.Sent, later.Body = protocol.NewID(), protocol.NewID(), 3, NewTopicOrganizationNotice(topic, "Later move cannot rename")
	for _, msgs := range [][]ConvMessage{{first, notice, later}, {later, first, notice}} {
		got := summarizeChatTopics(conv, msgs, nil, 10)
		if len(got) != 1 || got[0].Title != "Fixture organization" || got[0].Renamed {
			t.Fatalf("first admitted notice automatic title: %+v", got)
		}
	}
	for _, mutate := range []func(*ConvMessage){
		func(m *ConvMessage) { m.Origin = "ui" },
		func(m *ConvMessage) { m.PID = "" },
		func(m *ConvMessage) { m.TopicEvent = &envelope.TopicEvent{Action: "move"} },
		func(m *ConvMessage) {
			m.TopicEvent = &envelope.TopicEvent{Action: "merge", Request: notice.TopicEvent.Request}
		},
		func(m *ConvMessage) { m.Topic = protocol.NewID() },
		func(m *ConvMessage) { m.Body = "Arbitrary agent text" },
	} {
		bad := notice
		mutate(&bad)
		if got := summarizeChatTopics(conv, []ConvMessage{first, bad}, nil, 10); len(got) != 1 || got[0].Title != first.Body {
			t.Fatalf("unrelated/noncanonical event renamed original: %+v", got)
		}
	}
	// Exercise the actual shared/private presentation order without a network
	// fixture. These inputs represent stored admitted events; admission itself
	// is covered by TestAgentTopicOrganizationAdmittedRun and the joined journey.
	s, err := openStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	a := &Agent{store: s}
	msgs := []ConvMessage{first, notice, later}
	locals := map[string]topicLocal{topic: {Title: "Private name"}}
	got := summarizeChatTopics(conv, msgs, locals, 10)
	if got[0].Title != "Private name" || got[0].AutoTitle != "Fixture organization" || !got[0].Renamed {
		t.Fatalf("private name precedence: %+v", got)
	}
	for _, title := range []string{"Shared name", ""} {
		if _, err := s.db.Exec(`INSERT OR REPLACE INTO topic_shared_titles(conv,topic,title,rev,writer,lid) VALUES(?,?,?,1,?,?)`, conv, topic, title, "11111111-22222222-33333333-44444444", protocol.NewID()); err != nil {
			t.Fatal(err)
		}
		got = summarizeChatTopics(conv, msgs, locals, 10)
		if err := a.projectSharedTopicTitles(conv, got); err != nil {
			t.Fatal(err)
		}
		if title == "" {
			if got[0].Title != "Fixture organization" || got[0].Renamed || got[0].AutoTitle != "" {
				t.Fatalf("explicit reset restores notice automatic title: %+v", got)
			}
		} else if got[0].Title != title || got[0].AutoTitle != "Fixture organization" || !got[0].Renamed {
			t.Fatalf("shared name precedence: %+v", got)
		}
	}
}

func TestAgentOrganizationTitleNeverUsesLegacyPrivateName(t *testing.T) {
	conv, topic := strings.Repeat("c", 64), protocol.NewID()
	author := "11111111-22222222-33333333-44444444"
	selected := ConvMessage{ID: protocol.NewID(), LID: protocol.NewID(), Key: author, Kind: envelope.KindMessage, Body: "Selected ordinary original", Sent: 2}
	existing := ConvMessage{ID: protocol.NewID(), LID: protocol.NewID(), Key: author, Kind: envelope.KindMessage, Body: "Existing destination original", Topic: topic, Sent: 1}
	move := ConvMessage{ID: protocol.NewID(), LID: protocol.NewID(), Kind: envelope.KindMessage, Topic: topic, Sent: 4, PID: protocol.NewID(), Origin: envelope.OriginAgentOrganization, Body: TopicOrganizationNotice(topic, "HOST PRIVATE LABEL"), TopicEvent: &envelope.TopicEvent{Action: "move", Moves: []envelope.TopicMove{{LID: selected.LID, Author: author}}, Request: &envelope.TopicRequest{LID: protocol.NewID()}}}
	away := ConvMessage{ID: protocol.NewID(), LID: protocol.NewID(), Kind: envelope.KindMessage, Topic: protocol.NewID(), Sent: 3, TopicEvent: &envelope.TopicEvent{Action: "move", Moves: []envelope.TopicMove{{LID: existing.LID, Author: author, Topic: topic}}}}
	for _, c := range []struct {
		name string
		msgs []ConvMessage
	}{
		{"existing unselected destination", []ConvMessage{existing, selected, move}},
		{"earlier destination moved away", []ConvMessage{existing, selected, away, move}},
		// Exact selected history cannot prove that the destination never
		// existed. It is indistinguishable from creation on the old wire.
		{"earlier destination absent from grant", []ConvMessage{selected, move}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, got := range summarizeChatTopics(conv, c.msgs, nil, 10) {
				if got.ID == topic && (got.Title == "HOST PRIVATE LABEL" || got.Renamed) {
					t.Fatalf("legacy move disclosed host private title: %+v", got)
				}
			}
		})
	}
}
