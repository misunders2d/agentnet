package ui

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func topicNoticeDMFixture(t *testing.T, home string) (*Live, *client.Agent, *sql.DB, string) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	hubDir := t.TempDir()
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, home, testhub.BootstrapCode(t, hubDir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	if _, err := alice.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.CreatePerson(ctx, "Bob"); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, bob)
	live := NewLive(alice)
	conv, err := live.NewDM(bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return live, alice, db, conv
}

func TestLiveTopicOrganizationNoticeDestination(t *testing.T) {
	a, b, _ := liveWorld(t)
	if _, err := a.CreatePerson(t.Context(), "Alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.CreatePerson(t.Context(), "Bob"); err != nil {
		t.Fatal(err)
	}
	l := NewLive(a)
	conv, err := l.NewDM(b.Address)
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.SendConv(t.Context(), conv, client.ConvOutgoing{Body: "source"})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := a.SendConv(t.Context(), conv, client.ConvOutgoing{Body: "destination", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := l.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	topic := ""
	for _, m := range view.Messages {
		if m.LID == dest.LID {
			topic = m.Topic
		}
	}
	if topic == "" {
		t.Fatal("destination topic absent")
	}
	if _, err = a.ChangeChatTopic(t.Context(), conv, topic, "rename", "Reviewed destination", 0); err != nil {
		t.Fatal(err)
	}
	for _, main := range []bool{false, true} {
		request := client.TopicOrganizationRequest{Conv: conv, IDs: []string{first.LID}, Topic: topic}
		want := "Moved selected messages to “Reviewed destination”."
		if main {
			request.Topic = ""
			request.Main = true
			want = "Moved selected messages to Main flow."
		}
		review, e := a.PreviewTopicOrganization(request)
		if e != nil {
			t.Fatal(e)
		}
		moved, e := a.ApplyTopicOrganization(t.Context(), review)
		if e != nil {
			t.Fatal(e)
		}
		view, e = l.DM(conv)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range view.Messages {
			if m.LID == moved.LID {
				found = true
				if m.Body != want {
					t.Fatalf("move notice=%q want %q", m.Body, want)
				}
			}
		}
		if !found {
			t.Fatal("move notice absent")
		}
		overview, e := l.Overview()
		if e != nil {
			t.Fatal(e)
		}
		for _, d := range overview.DMs {
			if d.ID == conv && d.Last != want {
				t.Fatalf("preview=%q want %q", d.Last, want)
			}
		}
		if !main {
			if _, e = a.ChangeChatTopic(t.Context(), conv, topic, "rename", "Renamed destination", 0); e != nil {
				t.Fatal(e)
			}
			view, e = l.DM(conv)
			if e != nil {
				t.Fatal(e)
			}
			for _, m := range view.Messages {
				if m.LID == moved.LID && m.Body != "Moved selected messages to “Renamed destination”." {
					t.Fatalf("renamed notice=%q", m.Body)
				}
			}
		}
	}
	for _, automatic := range []string{"Automatic destination", ""} {
		name := "automatic"
		if automatic == "" {
			name = "raw_topic_id"
		}
		t.Run(name, func(t *testing.T) {
			live, agent, db, conv := topicNoticeDMFixture(t, t.TempDir())
			clearUnread := func() {
				t.Helper()
				view, err := live.DM(conv)
				if err != nil {
					t.Fatal(err)
				}
				var ids []string
				attention := map[string]protocol.AttentionGeneration{}
				for _, m := range view.Messages {
					if m.Unread {
						ids = append(ids, m.ID)
						if m.Attention != nil {
							attention[m.ID] = *m.Attention
						}
					}
				}
				if len(ids) > 0 {
					if err := agent.MarkReadWithAttention(ids, attention); err != nil {
						t.Fatal(err)
					}
				}
			}
			clearUnread()
			draft := client.ConvOutgoing{Body: automatic}
			if automatic == "" {
				draft.Files = []client.OutgoingFile{{Path: goFile(t, "topic.txt", []byte("file-only topic"))}}
			}
			first, err := agent.SendConv(t.Context(), conv, draft)
			if err != nil {
				t.Fatal(err)
			}
			draft.Topic = "new"
			dest, err := agent.SendConv(t.Context(), conv, draft)
			if err != nil {
				t.Fatal(err)
			}
			view, err := live.DM(conv)
			if err != nil {
				t.Fatal(err)
			}
			topic, firstID := "", ""
			for _, m := range view.Messages {
				if m.LID == dest.LID {
					topic = m.Topic
				}
				if m.LID == first.LID {
					firstID = m.ID
				}
			}
			if topic == "" || firstID == "" {
				t.Fatal("topic or canonical source absent")
			}
			// A retained pre-v33 private label is presentation state, not a shared rename.
			if _, err := db.Exec(`INSERT INTO topic_state(peer,topic,title,updated_at) VALUES(?,?,?,1) ON CONFLICT(peer,topic) DO UPDATE SET title=excluded.title`, conv, topic, "Legacy private destination"); err != nil {
				t.Fatal(err)
			}
			agent.NoteChange()
			review, err := agent.PreviewTopicOrganization(client.TopicOrganizationRequest{Conv: conv, IDs: []string{first.LID}, Topic: topic})
			if err != nil {
				t.Fatal(err)
			}
			moved, err := agent.ApplyTopicOrganization(t.Context(), review)
			if err != nil {
				t.Fatal(err)
			}
			check := func(want, title string, unread int) {
				t.Helper()
				view, err := live.DM(conv)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, m := range view.Messages {
					if m.LID == moved.LID {
						found = true
						if m.Body != want {
							t.Fatalf("notice=%q want %q", m.Body, want)
						}
					}
				}
				if !found {
					t.Fatal("projected notice absent")
				}
				for _, overview := range []func() (Overview, error){live.Overview, live.TopicOverview} {
					o, err := overview()
					if err != nil {
						t.Fatal(err)
					}
					found = false
					for _, d := range o.DMs {
						if d.ID == conv {
							found = true
							if d.Last != want || d.Unread != unread {
								t.Fatalf("overview last=%q unread=%d want %q/%d", d.Last, d.Unread, want, unread)
							}
						}
					}
					if !found {
						t.Fatal("chat absent from overview")
					}
				}
				if unread > 0 {
					page, err := live.UnreadPage(UnreadQuery{Limit: 200})
					if err != nil {
						t.Fatal(err)
					}
					found = false
					for _, row := range page.Rows {
						if row.Conv == conv && row.LID == first.LID {
							found = true
							if row.Topic != topic || row.TopicTitle != title {
								t.Fatalf("unread topic=%q title=%q want %q/%q", row.Topic, row.TopicTitle, topic, title)
							}
						}
					}
					if !found {
						t.Fatal("deliberately unread source absent")
					}
				}
			}
			rename := func(title string) {
				t.Helper()
				if _, err := agent.ChangeChatTopic(t.Context(), conv, topic, "rename", title, 0); err != nil {
					t.Fatal(err)
				}
			}
			privateNotice := "Moved selected messages to “Legacy private destination”."
			sharedNotice := "Moved selected messages to “Shared destination”."
			resetNotice := "Moved selected messages to “Automatic destination”."
			if automatic == "" {
				resetNotice = "Moved selected messages to topic " + topic + "."
			}
			check(privateNotice, "Legacy private destination", 0)
			if err := agent.MarkUnread(firstID, []string{firstID}); err != nil {
				t.Fatal(err)
			}
			check(privateNotice, "Legacy private destination", 1)
			rename("Shared destination")
			check(sharedNotice, "Shared destination", 1)
			rename("")
			check(resetNotice, automatic, 1)
			clearUnread()
			check(resetNotice, automatic, 0)
			rename("Shared destination")
			check(sharedNotice, "Shared destination", 0)
			rename("")
			check(resetNotice, automatic, 0)
			var private string
			if err := db.QueryRow(`SELECT title FROM topic_state WHERE peer=? AND topic=?`, conv, topic).Scan(&private); err != nil || private != "Legacy private destination" {
				t.Fatalf("shared projection changed retained private title: %q %v", private, err)
			}
		})
	}
}
