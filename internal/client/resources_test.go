package client

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestConversationResourcesMembersCASRestartAndGuestBoundary(t *testing.T) {
	w, carol, packet, stops := groupTurnsFixture(t)
	conv := packet.State.Conv
	guest := proofReader(t, w, "links-guest")
	runAgent(t, guest)
	for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
		roomReader(t, a)
	}
	invited, err := w.alice.InviteHuman(tctx(t), conv, guest.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest invitation", func() bool { return stateAt(t, guest, invited.PID).State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), invited.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "guest active", func() bool { return stateAt(t, w.alice, invited.PID).HumanActive() })

	// Carol is a non-administrator. Link names need not be unique or providers.
	links := []protocol.ResourceLink{{Name: "Notes", URL: "https://example.com/notes?a=1&b=2"}, {Name: "Notes", URL: "https://example.org/other"}}
	first, err := carol.SetConversationResources(tctx(t), conv, links, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Rev != 1 || first.Writer != carol.Self().Fingerprint() || !reflect.DeepEqual(first.Links, links) {
		t.Fatalf("saved register: %+v", first)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "member sees links", func() bool {
			r, err := a.ConversationResources(conv)
			return err == nil && reflect.DeepEqual(r, first)
		})
	}
	if _, err = w.alice.SetConversationResources(tctx(t), conv, nil, 0, ""); !errors.Is(err, ErrConversationResourcesStale) {
		t.Fatalf("stale edit: %v", err)
	}
	if _, err = guest.ConversationResources(conv); err == nil {
		t.Fatal("guest read member links")
	}
	if _, err = guest.SetConversationResources(tctx(t), conv, links, 0, ""); err == nil {
		t.Fatal("guest edited member links")
	}
	var leaked int
	if err = guest.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND json_extract(topic_event,'$.action')='resources'`, conv).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("guest copies=%d: %v", leaked, err)
	}
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		rows, err := a.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if envelope.TopicResources(row.TopicEvent) {
				t.Fatal("links rendered as a conversation message")
			}
		}
		refs, err := a.SelectGroupHistory(tctx(t), conv, GroupHistorySelection{Last: 64})
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range refs {
			if ref.LID == first.LID {
				t.Fatal("links became selectable message history")
			}
		}
	}

	var restarted *Agent
	t.Cleanup(func() {
		if restarted != nil {
			restarted.Close()
		}
	})
	restarted = reopen(t, w.bob, stops[w.bob])
	r, err := restarted.ConversationResources(conv)
	if err != nil || !reflect.DeepEqual(r, first) {
		t.Fatalf("restart register: %+v, %v", r, err)
	}
	phone := humanLinked(t, w.alice)
	eventually(t, "new linked member recovers links", func() bool {
		r, err := phone.ConversationResources(conv)
		return err == nil && reflect.DeepEqual(r, first)
	})
	cleared, err := w.alice.SetConversationResources(tctx(t), conv, []protocol.ResourceLink{}, first.Rev, first.Writer)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "clear propagates to restarted member", func() bool {
		r, err := restarted.ConversationResources(conv)
		return err == nil && r.Rev == cleared.Rev && r.Writer == cleared.Writer && len(r.Links) == 0
	})
}

func TestConversationResourcesOldReaderWaits(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	a, b, conv := w.alice, w.bob, packet.State.Conv
	signCapsAfter(t, b, without(ownCaps, protocol.CapConversationResources))
	links := []protocol.ResourceLink{{Name: "Plan", URL: "https://example.com/plan"}}
	saved, err := a.SetConversationResources(tctx(t), conv, links, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err = a.store.db.QueryRow(`SELECT state FROM outbox WHERE conv=? AND lid=? AND recipient=?`, conv, saved.LID, b.Address).Scan(&state); err != nil || state != stateConvWaiting {
		t.Fatalf("old reader copy=%q: %v", state, err)
	}
	before, err := b.ConversationResources(conv)
	if err != nil || before.Rev != 0 {
		t.Fatalf("old reader received unsupported metadata: %+v %v", before, err)
	}
	ordinary, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "Ordinary chat still works"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "ordinary group traffic bypasses capability wait", func() bool {
		for _, row := range groupTurns(t, b, conv) {
			if row.LID == ordinary.LID && row.Body == "Ordinary chat still works" {
				return true
			}
		}
		return false
	})
	signCapsAfter(t, b, ownCaps)
	features, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.releaseConv(tctx(t), features)
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reader upgrade releases retained links", func() bool {
		r, err := b.ConversationResources(conv)
		return err == nil && reflect.DeepEqual(r, saved)
	})
}

func TestConversationResourcesMemberAgentContext(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	setResponder(t, w.bob, "agentstub", stub.dir, time.Minute)
	conv := packet.State.Conv
	p := p6Member(t, w.bob, w.bob, conv)
	var requestLID string
	for _, url := range []string{"https://example.com/FIRST_SHARED_REFERENCE", "https://example.com/UPDATED_SHARED_REFERENCE"} {
		current, err := w.alice.ConversationResources(conv)
		if err != nil {
			t.Fatal(err)
		}
		saved, err := w.alice.SetConversationResources(tctx(t), conv, []protocol.ResourceLink{{Name: "Member notes", URL: url}}, current.Rev, current.Writer)
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "member host sees current resources", func() bool { r, err := w.bob.ConversationResources(conv); return err == nil && r.Rev == saved.Rev })
		q, err := w.bob.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "Review the chat's shared reference")
		if err != nil {
			t.Fatal(err)
		}
		replyAt(t, w.bob, conv, q.ID)
		requestLID = q.LID
		prompt := stub.last()
		if !strings.Contains(prompt, url) {
			t.Fatalf("native member prompt omitted current reference: %s", prompt)
		}
	}

	guest := proofReader(t, w, "member-context-guest")
	runAgent(t, guest)
	for _, a := range []*Agent{w.alice, w.bob, guest} {
		roomReader(t, a)
	}
	invited, err := w.alice.InviteHuman(tctx(t), conv, guest.Address, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "context guest invited", func() bool { return stateAt(t, guest, invited.PID).State == PartInvited })
	if _, err = guest.AcceptParticipation(tctx(t), invited.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "member host knows active context guest", func() bool {
		return stateAt(t, w.bob, invited.PID).HumanActive()
	})
	// Capture the real accepted guest audience on the stored request to
	// exercise prompt disclosure independently of dispatch.
	human, err := w.bob.roomAudience(conv, "", "")
	if err != nil {
		t.Fatal(err)
	}
	request := job{From: w.bob.Address, Key: w.bob.Self().Fingerprint(), Conv: conv, PID: p.PID,
		Kind: envelope.KindQuestion, Body: "Review the chat's shared reference", Local: true}
	if err = w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND local=1`, conv, requestLID).Scan(&request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.store.db.Exec(`UPDATE inbox SET human=? WHERE id=?`, humanJSON(human), request.ID); err != nil {
		t.Fatal(err)
	}
	prompt, err := w.bob.agentPrompt(request, &Responder{Dir: stub.dir}, "", tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "https://example.com/UPDATED_SHARED_REFERENCE") || strings.Contains(prompt, "Member notes") {
		t.Fatalf("guest-visible member reply received private links: %s", prompt)
	}
}

func TestConversationResourcesNewMember(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-links", true: "cleared-register"}[cleared], func(t *testing.T) {
			w, carol, packet, _ := groupTurnsFixture(t)
			conv := packet.State.Conv
			saved, err := carol.SetConversationResources(tctx(t), conv, []protocol.ResourceLink{{Name: "Current plan", URL: "https://example.com/current-plan"}}, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			if cleared {
				saved, err = carol.SetConversationResources(tctx(t), conv, nil, saved.Rev, saved.Writer)
				if err != nil {
					t.Fatal(err)
				}
			}
			eventually(t, "publisher has other author's current register", func() bool {
				got, err := w.alice.ConversationResources(conv)
				return err == nil && reflect.DeepEqual(got, saved)
			})
			guest := proofReader(t, w, "bootstrap-links-guest")
			runAgent(t, guest)
			for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
				roomReader(t, a)
			}
			guestInvite, err := w.alice.InviteHuman(tctx(t), conv, guest.Address, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "bootstrap guest invitation", func() bool { return stateAt(t, guest, guestInvite.PID).State == PartInvited })
			if _, err = guest.AcceptParticipation(tctx(t), guestInvite.PID); err != nil {
				t.Fatal(err)
			}
			eventually(t, "bootstrap guest active", func() bool { return stateAt(t, w.alice, guestInvite.PID).HumanActive() })
			joiner := proofReader(t, w, "new-resource-member")
			runAgent(t, joiner)
			publishGroupFixtureCaps(t, joiner, true)
			person, _, err := joiner.store.selfPerson(joiner.Address)
			if err != nil {
				t.Fatal(err)
			}
			invited, err := w.alice.InviteGroup(tctx(t), conv, person.roster.Person, nil)
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "new resource member invitation", func() bool {
				rows, err := joiner.GroupInvitations()
				for _, row := range rows {
					if err == nil && row.ID == invited.ID {
						return true
					}
				}
				return false
			})
			if err = joiner.DecideGroupInvitation(tctx(t), invited.ID, true); err != nil {
				t.Fatal(err)
			}
			eventually(t, "new resource member admitted", func() bool {
				p, err := joiner.GroupContext(conv)
				if err != nil {
					return false
				}
				_, present := p.State.Member(person.roster.Person)
				return present
			})
			var bootstrapped ConversationResources
			eventually(t, "new member receives fresh signed current links without earlier history", func() bool {
				links, err := joiner.ConversationResources(conv)
				bootstrapped = links
				return err == nil && reflect.DeepEqual(links.Links, saved.Links) && links.Rev == saved.Rev+1 &&
					links.Writer == w.alice.Self().Fingerprint() && links.LID != saved.LID && protocol.ValidID(links.LID)
			})
			for _, a := range []*Agent{w.alice, w.bob, carol} {
				eventually(t, "all members converge on one admission publication", func() bool {
					got, err := a.ConversationResources(conv)
					return err == nil && reflect.DeepEqual(got, bootstrapped)
				})
			}
			var leaked int
			if err = guest.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND json_extract(topic_event,'$.action')='resources'`, conv).Scan(&leaked); err != nil || leaked != 0 {
				t.Fatalf("bootstrap guest copies=%d: %v", leaked, err)
			}
			// Replay the exact durable publication, as recovery after a lost completion
			// response would. It must be a receipt, not another resources revision.
			var raw, payload []byte
			if err = w.alice.store.db.QueryRow(`SELECT record,payload FROM group_publications WHERE conv=? AND invitation=? AND published=1`, conv, invited.ID).Scan(&raw, &payload); err != nil {
				t.Fatal(err)
			}
			var commit protocol.GroupCommit
			var published GroupContext
			if err = json.Unmarshal(raw, &commit); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(payload, &published); err != nil {
				t.Fatal(err)
			}
			if _, err = w.alice.PublishGroup(tctx(t), commit, published, invited.ID); err != nil {
				t.Fatal(err)
			}
			after, err := w.alice.ConversationResources(conv)
			if err != nil || !reflect.DeepEqual(after, bootstrapped) {
				t.Fatalf("publication replay changed register: %+v, %v", after, err)
			}
			var events int
			if err = w.alice.store.db.QueryRow(`SELECT count(DISTINCT lid) FROM outbox WHERE conv=? AND json_extract(topic_event,'$.action')='resources'`, conv).Scan(&events); err != nil || events != 1 {
				t.Fatalf("admission publications=%d: %v", events, err)
			}
			if cleared {
				added, err := joiner.SetConversationResources(tctx(t), conv, []protocol.ResourceLink{{Name: "New member plan", URL: "https://example.org/new-plan"}}, bootstrapped.Rev, bootstrapped.Writer)
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range []*Agent{w.alice, w.bob, carol} {
					eventually(t, "new member edit after clear converges", func() bool {
						got, err := a.ConversationResources(conv)
						return err == nil && reflect.DeepEqual(got, added)
					})
				}
			}
		})
	}
}
