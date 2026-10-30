package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedTopicRegisterOrderingAndReset(t *testing.T) {
	s, e := openStore(filepath.Join(t.TempDir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.db.Close()
	conv, topic := strings.Repeat("c", 64), protocol.NewID()
	low := "11111111-22222222-33333333-44444444"
	high := "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"
	apply := func(rev int64, writer, title string) error {
		tx, e := s.db.Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		n := envelope.Inner{V: 2, Conv: conv, Topic: topic, LID: protocol.NewID(), Kind: envelope.KindMessage, TopicEvent: &envelope.TopicEvent{Action: "rename", Title: &title, Rev: rev}}
		if e = saveSharedTopicMetadata(tx, n, writer); e != nil {
			return e
		}
		return tx.Commit()
	}
	for _, order := range [][]string{{low, high}, {high, low}} {
		s.db.Exec(`DELETE FROM topic_shared_titles`)
		for _, fp := range order {
			if e = apply(1, fp, fp); e != nil {
				t.Fatal(e)
			}
		}
		r, ok, e := sharedTopicTitle(s.db, conv, topic)
		if e != nil || !ok || r.Writer != high {
			t.Fatalf("winner %v %v %v", r, ok, e)
		}
	}
	if e = apply(1, high, "conflict"); !errors.Is(e, ErrSharedTopicTitleConflict) {
		t.Fatal(e)
	}
	if e = apply(2, low, ""); e != nil {
		t.Fatal(e)
	}
	if e = apply(1, high, "delayed"); e != nil {
		t.Fatal(e)
	}
	r, ok, e := sharedTopicTitle(s.db, conv, topic)
	if e != nil || !ok || r.Title != "" || r.Rev != 2 {
		t.Fatalf("reset presence %v %v %v", r, ok, e)
	}
}
func TestSharedTopicDMMetadataAndRollback(t *testing.T) {
	w, conv, _ := dmFiles(t)
	a := w.alice
	ctx := tctx(t)
	s, e := a.SendConv(ctx, conv, ConvOutgoing{Body: "automatic topic", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	topic := convMsgByID(t, a, conv, s.ID).Topic
	if _, e = a.RenameSharedTopic(ctx, conv, topic, " shared\u202e title "); e != nil {
		t.Fatal(e)
	}
	eventually(t, "shared metadata arrives", func() bool {
		r, ok, e := w.bob.SharedTopicTitle(conv, topic)
		return e == nil && ok && r.Title == "shared title" && r.Writer == a.Self().Fingerprint()
	})
	for _, agent := range []*Agent{a, w.bob} {
		msgs, e := agent.ConversationMessages(conv)
		if e != nil {
			t.Fatal(e)
		}
		for _, m := range msgs {
			if envelope.TopicRename(m.TopicEvent) {
				t.Fatal("metadata timeline row")
			}
		}
	}
	old, _, _ := a.SharedTopicTitle(conv, topic)
	title := "must rollback"
	m := ConvOutgoing{Topic: topic, TopicEvent: &envelope.TopicEvent{Action: "rename", Title: &title, Rev: old.Rev + 1}}
	if e = a.prepareSharedTopicMetadata(conv, &m); e != nil {
		t.Fatal(e)
	}
	// An observed concurrent winner invalidates the allocation inside the commit.
	tx, _ := a.store.db.Begin()
	newTitle := "concurrent"
	n := envelope.Inner{V: 2, Conv: conv, Topic: topic, LID: protocol.NewID(), Kind: envelope.KindMessage, TopicEvent: &envelope.TopicEvent{Action: "rename", Title: &newTitle, Rev: old.Rev + 1}}
	if e = saveSharedTopicMetadata(tx, n, a.Self().Fingerprint()); e != nil {
		t.Fatal(e)
	}
	tx.Commit()
	var countBefore, countAfter int
	a.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&countBefore)
	e = a.store.addConvOutboxGuarded(nil, envelope.Inner{}, nil, "", m.beforeStore)
	if !errors.Is(e, ErrSharedTopicTitleStale) {
		t.Fatal(e)
	}
	a.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&countAfter)
	if countAfter != countBefore {
		t.Fatal("stale outbox write")
	}
	if _, e = a.RenameSharedTopic(ctx, conv, topic, ""); e != nil {
		t.Fatal(e)
	}
	if e = a.setTopicTitle(conv, topic, "private old label"); e != nil {
		t.Fatal(e)
	}
	r, ok, e := a.SharedTopicTitle(conv, topic)
	if e != nil || !ok || r.Title != "" {
		t.Fatal("private sync changed reset")
	}
	// A rejected normal authority claim must precede neutral before-store effects.
	called := false
	reject := errors.New("authority changed")
	e = a.store.addConvOutboxGuarded(nil, envelope.Inner{}, func(*sql.Tx, string) error { return reject }, "", func(*sql.Tx, []outCopy, envelope.Inner) error { called = true; return nil })
	if !errors.Is(e, reject) || called {
		t.Fatal("guard precedes authority")
	}
	in := envelope.Inner{V: 2, Kind: envelope.KindMessage, Origin: envelope.OriginUI, TopicEvent: &envelope.TopicEvent{Action: "rename"}}
	if asksAttention(in) {
		t.Fatal("metadata attention")
	}
}
func TestSharedTopicOwnErasedWinnerOnly(t *testing.T) {
	w, conv, _ := dmFiles(t)
	a := w.alice
	ctx := tctx(t)
	s, e := a.SendConv(ctx, conv, ConvOutgoing{Body: "topic", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	topic := convMsgByID(t, a, conv, s.ID).Topic
	first, e := a.RenameSharedTopic(ctx, conv, topic, "old")
	if e != nil {
		t.Fatal(e)
	}
	winner, e := a.RenameSharedTopic(ctx, conv, topic, "new")
	if e != nil {
		t.Fatal(e)
	}
	for _, lid := range []string{first.LID, winner.LID} {
		if _, e = a.store.db.Exec(`INSERT INTO conv_erased(conv,lid,key,deletion) VALUES(?,?,?,?)`, conv, lid, a.Self().Fingerprint(), protocol.NewID()); e != nil {
			t.Fatal(e)
		}
	}
	generic, e := a.historySourceRows(a.store.db, "conv=? AND lid IN (?,?)", "id", 10, conv, first.LID, winner.LID)
	if e != nil || len(generic) != 0 {
		t.Fatalf("selected leaked %d %v", len(generic), e)
	}
	own, e := a.historySourceRowsMode(a.store.db, true, "conv=? AND lid IN (?,?)", "id", 10, conv, first.LID, winner.LID)
	if e != nil || len(own) != 1 || own[0].in.LID != winner.LID || own[0].in.Body != "" {
		t.Fatalf("own winner %d %v", len(own), e)
	}
	if _, e = a.ownHistorySourceRows(a.store.db, w.bob.Self(), "conv=?", "id", 10, conv); e == nil {
		t.Fatal("other person own-source access")
	}
	phone := linked(t, a)
	eventually(t, "fresh own link recovers winner", func() bool {
		r, ok, e := phone.SharedTopicTitle(conv, topic)
		return e == nil && ok && r.Title == "new" && r.Writer == a.Self().Fingerprint()
	})
	rows, e := phone.ConversationMessages(conv)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range rows {
		if envelope.TopicRename(m.TopicEvent) {
			t.Fatal("recovered notice rendered")
		}
	}
}
func TestSharedTopicHistoryRequirementPersists(t *testing.T) {
	w, conv, _ := dmFiles(t)
	a := w.alice
	title := "retained"
	item := HistoryItem{V: 1, Topic: protocol.NewID(), TopicEvent: &envelope.TopicEvent{Action: "rename", Title: &title, Rev: 1}}
	body, _ := json.Marshal(item)
	id := protocol.NewID()
	c := outCopy{env: envelope.Envelope{ID: id, To: w.bob.Address}, in: envelope.Inner{Conv: conv, Sub: envelope.SubHistory, Body: string(body)}, state: stateQueued, required: protocol.CapGroup}
	tx, _ := a.store.db.Begin()
	if e := insertCopies(tx, []outCopy{c}); e != nil {
		t.Fatal(e)
	}
	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}
	if _, e := a.store.db.Exec(`UPDATE outbox SET body='' WHERE id=?`, id); e != nil {
		t.Fatal(e)
	}
	n, e := copyNeedsOf(a.store.db, id, 2)
	if e != nil || !n.sharedTitleCap || n.required != protocol.CapGroup {
		t.Fatalf("lost composite requirement %+v %v", n, e)
	}
}

func TestSharedTopicExactGuestLiveAndHistorical(t *testing.T) {
	for _, group := range []bool{false, true} {
		name := "DM"
		if group {
			name = "group"
		}
		t.Run(name, func(t *testing.T) {
			var w *world
			var guest *Agent
			var conv string
			var stub *agentStub
			if !group {
				w, guest, conv, _, stub = humanWorld(t)
			} else {
				var packet GroupContext
				var carol *Agent
				w, carol, packet, _ = groupTurnsFixture(t)
				conv = packet.State.Conv
				guest = proofReader(t, w, "renameguest")
				runAgent(t, guest)
				publishGroupFixtureCaps(t, guest, true)
				for _, a := range []*Agent{w.alice, w.bob, carol, guest} {
					roomReader(t, a)
				}
			}

			topic := protocol.NewID()
			selected, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "scoped context", Topic: topic})
			if e != nil {
				t.Fatal(e)
			}
			p, e := w.alice.InviteHumanInScope(WithQueuedSend(tctx(t), ""), conv, guest.Address, []string{selected.LID}, "", &topic)
			if e != nil {
				t.Fatal(e)
			}
			eventually(t, "exact scoped invitation", func() bool {
				info, e := guest.Participation(p.PID)
				return e == nil && info.State == PartInvited
			})
			if _, e = guest.AcceptParticipation(tctx(t), p.PID); e != nil {
				t.Fatal(e)
			}
			eventually(t, "exact scoped acceptance", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
			if _, e = w.alice.RenameSharedTopic(tctx(t), conv, topic, "member title"); e != nil {
				t.Fatal(e)
			}
			eventually(t, "following guest saves metadata", func() bool {
				r, ok, e := guest.SharedTopicTitle(conv, topic)
				return e == nil && ok && r.Title == "member title"
			})
			renamed, e := guest.RenameSharedTopic(tctx(t), conv, topic, "guest title")
			if e != nil {
				t.Fatal(e)
			}
			eventually(t, "both original members admit guest writer", func() bool {
				for _, a := range []*Agent{w.alice, w.bob} {
					r, ok, e := a.SharedTopicTitle(conv, topic)
					if e != nil || !ok || r.Title != "guest title" || r.Writer != guest.Self().Fingerprint() {
						return false
					}
				}
				return true
			})

			// Exercise the same exact original-author/topic/epoch verifier used
			// inside historical admission, with current signed fixture evidence.
			source, e := w.alice.historySourceRows(w.alice.store.db, "conv=? AND lid=? AND dir='in'", "id", 1, conv, renamed.LID)
			if e != nil || len(source) != 1 {
				t.Fatalf("original metadata source %d %v", len(source), e)
			}
			item, e := w.alice.historySourceItem(w.alice.store.db, source[0])
			if e != nil {
				t.Fatal(e)
			}
			fakeKey, e := identity.Generate()
			if e != nil {
				t.Fatal(e)
			}
			if group {
				if e = w.alice.store.db.QueryRow(`SELECT group_admission FROM inbox WHERE id=?`, item.ID).Scan(&item.GroupAdmission); e != nil {
					t.Fatal(e)
				}
				root, _, _, e := w.alice.store.conversation(conv)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = w.alice.groupParticipationHistoryCheck(w.alice.store.db, root, w.alice.Self(), item); e != nil {
					t.Fatal("valid historical source", e)
				}
				for name, mutate := range map[string]func(*HistoryItem){"key": func(i *HistoryItem) { i.FromKey = fakeKey.Public(guest.Address).Fingerprint() }, "topic": func(i *HistoryItem) { i.Topic = protocol.NewID() }, "epoch": func(i *HistoryItem) { i.GroupAdmission = strings.Repeat("e", 64) }, "PID": func(i *HistoryItem) { i.PID = protocol.NewID() }} {
					bad := item
					mutate(&bad)
					if _, e = w.alice.groupParticipationHistoryCheck(w.alice.store.db, root, w.alice.Self(), bad); e == nil {
						t.Fatal("historical group accepted wrong " + name)
					}
				}
			} else {
				for name, mutate := range map[string]func(*HistoryItem){"key": func(i *HistoryItem) { i.FromKey = fakeKey.Public(guest.Address).Fingerprint() }, "topic": func(i *HistoryItem) { i.Topic = protocol.NewID() }} {
					bad := item
					mutate(&bad)
					if e = humanTurnAuthorization(w.alice.store.db, bad.inner(conv), bad.From, bad.FromKey, w.alice.Address, w.alice.Self().Fingerprint(), true); e == nil {
						t.Fatal("historical DM accepted wrong " + name)
					}
				}
			}
			for _, wrong := range []string{"", protocol.NewID()} {
				if _, e = guest.RenameSharedTopic(tctx(t), conv, wrong, "wrong scope"); e == nil {
					t.Fatal("guest renamed outside exact topic")
				}
			}
			if stub != nil && stub.runs() != 0 {
				t.Fatal("rename invoked a native job")
			}
			// Own-linked history retains the captured original guest writer after dismissal.
			if _, e = w.alice.DismissParticipation(tctx(t), p.PID); e != nil {
				t.Fatal(e)
			}
			eventually(t, "dismissed exact guest", func() bool { return stateAt(t, guest, p.PID).State == PartDismissed })
			if _, e = guest.RenameSharedTopic(tctx(t), conv, topic, "post-dismiss"); e == nil {
				t.Fatal("dismissed guest renamed")
			}
			phone := humanLinked(t, w.alice)
			eventually(t, "own historical original guest title", func() bool {
				r, ok, e := phone.SharedTopicTitle(conv, topic)
				return e == nil && ok && r.Title == "guest title" && r.Writer == guest.Self().Fingerprint() && r.LID == renamed.LID
			})
			sibling := humanLinked(t, guest)
			if _, ok, e := sibling.SharedTopicTitle(conv, topic); e != nil || ok {
				t.Fatalf("guest sibling received ambient title %v %v", ok, e)
			}
		})
	}
}

func TestSharedTopicGroupErasedWinnerSelectedDenied(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	a := w.alice
	conv := packet.State.Conv
	topic := protocol.NewID()
	if _, e := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "group topic", Topic: topic}); e != nil {
		t.Fatal(e)
	}
	old, e := a.RenameSharedTopic(tctx(t), conv, topic, "old")
	if e != nil {
		t.Fatal(e)
	}
	winner, e := a.RenameSharedTopic(tctx(t), conv, topic, "winner")
	if e != nil {
		t.Fatal(e)
	}
	for _, lid := range []string{old.LID, winner.LID} {
		if _, e = a.store.db.Exec(`INSERT INTO conv_erased(conv,lid,key,deletion) VALUES(?,?,?,?)`, conv, lid, a.Self().Fingerprint(), protocol.NewID()); e != nil {
			t.Fatal(e)
		}
	}
	for _, original := range []bool{false, true} {
		for _, lid := range []string{old.LID, winner.LID} {
			rows, e := a.groupHistorySources(a.store.db, conv, lid, a.Self().Fingerprint(), 0, 3, original)
			want := 0
			if original && lid == winner.LID {
				want = 1
			}
			if e != nil || len(rows) != want {
				t.Fatalf("original=%v winner=%v len=%d err=%v", original, lid == winner.LID, len(rows), e)
			}
		}
	}
	phone := humanLinked(t, a)
	eventually(t, "fresh own group winner", func() bool {
		r, ok, e := phone.SharedTopicTitle(conv, topic)
		return e == nil && ok && r.Title == "winner"
	})
}

func TestSharedTopicOldReaderWaitsAndReleases(t *testing.T) {
	w, conv, _ := dmFiles(t)
	a, b := w.alice, w.bob
	ctx := tctx(t)
	topic := protocol.NewID()
	if _, e := a.SendConv(ctx, conv, ConvOutgoing{Body: "cap topic", Topic: topic}); e != nil {
		t.Fatal(e)
	}
	humanTestCaps(t, b)
	signCapsAfter(t, b, without(ownCaps, protocol.CapSharedTopicTitle))
	sent, e := a.RenameSharedTopic(ctx, conv, topic, "queued title")
	if e != nil {
		t.Fatal(e)
	}
	var id string
	for _, c := range sent.Copies {
		if c.To == b.Address {
			id = c.ID
		}
	}
	if id == "" {
		t.Fatal("missing exact copy")
	}
	state, _, _, e := a.store.outboxState(id)
	if e != nil || state != stateConvWaiting {
		t.Fatalf("old reader handed metadata %s %v", state, e)
	}
	if _, ok, e := b.SharedTopicTitle(conv, topic); e != nil || ok {
		t.Fatalf("old reader applied metadata %v %v", ok, e)
	}
	signCapsAfter(t, b, ownCaps)
	features, e := a.relayFeatures(ctx)
	if e != nil {
		t.Fatal(e)
	}
	a.releaseConv(ctx, features)
	if e = a.FlushOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	eventually(t, "queued title after reader upgrade", func() bool {
		r, ok, e := b.SharedTopicTitle(conv, topic)
		return e == nil && ok && r.Title == "queued title"
	})
}

func TestSharedTopicArchiveRetainsAdditionalGate(t *testing.T) {
	_, a, phone, _ := archiveFixture(t, 2)
	ctx := tctx(t)
	if _, e := a.historyCatchupPage(ctx, phone.Self()); e != nil {
		t.Fatal(e)
	}
	// The flag was derived from admitted metadata before plaintext disappears.
	// Packing and repacking must keep it even with no classifiable child body.
	if _, e := a.store.db.Exec(`UPDATE outbox SET shared_title_cap=1,body='' WHERE state=?`, archiveStaged); e != nil {
		t.Fatal(e)
	}
	if e := a.archivePack(ctx); e != nil {
		t.Fatal(e)
	}
	env := archiveDescriptor(t, a)
	n, e := copyNeedsOf(a.store.db, env.ID, 2)
	if e != nil || !n.sharedTitleCap || n.required != protocol.CapHistoryArchive {
		t.Fatalf("archive lost extra gate %+v %v", n, e)
	}

	// A failed descriptor may be repacked after its original child plaintext
	// and classification flags are gone; inherit the prior descriptor gate.
	if _, e = a.store.db.Exec(`UPDATE outbox SET shared_title_cap=0 WHERE state=?`, archiveStaged); e != nil {
		t.Fatal(e)
	}
	if _, e = a.store.db.Exec(`UPDATE outbox SET state='failed' WHERE id=?`, env.ID); e != nil {
		t.Fatal(e)
	}
	first := env.ID
	if e = a.archivePack(ctx); e != nil {
		t.Fatal(e)
	}
	env = archiveDescriptor(t, a)
	if env.ID == first {
		t.Fatal("repack did not create replacement")
	}
	n, e = copyNeedsOf(a.store.db, env.ID, 2)
	if e != nil || !n.sharedTitleCap {
		t.Fatal("prior manifest gate lost", e)
	}
	humanTestCaps(t, phone)
	signCapsAfter(t, phone, without(ownCaps, protocol.CapSharedTopicTitle))
	if e = a.readerCheck(ctx, phone.Address, phone.Self(), n); e == nil {
		t.Fatal("archive reached old rename reader")
	}

	if _, e = a.archiveStep(ctx); e != nil {
		t.Fatal(e)
	}
	if e = a.FlushOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	state, _, _, e := a.store.outboxState(env.ID)
	if e != nil || state != stateConvWaiting {
		t.Fatalf("old reader archive handover: %s %v", state, e)
	}
	home := a.home
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	a, e = Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	n, e = copyNeedsOf(a.store.db, env.ID, 2)
	if e != nil || !n.sharedTitleCap {
		t.Fatal("restart narrowed archive gate", e)
	}
	signCapsAfter(t, phone, ownCaps)
	if e = a.readerCheck(ctx, phone.Address, phone.Self(), n); e != nil {
		t.Fatal(e)
	}
	features, e := a.relayFeatures(ctx)
	if e != nil {
		t.Fatal(e)
	}
	a.releaseConv(ctx, features)
	if e = a.FlushOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	state, _, _, e = a.store.outboxState(env.ID)
	if e != nil || state != protocol.StateCustody {
		t.Fatalf("updated reader descriptor: %s %v", state, e)
	}
}

func TestSharedTopicCurrentAuthorityFences(t *testing.T) {
	for _, change := range []string{"held", "key"} {
		t.Run(change, func(t *testing.T) {
			w, guest, conv, _, _ := humanWorld(t)
			ctx := tctx(t)
			topic := protocol.NewID()
			p, e := w.alice.InviteHumanInScope(ctx, conv, guest.Address, nil, "", &topic)
			if e != nil {
				t.Fatal(e)
			}
			eventually(t, "invitation", func() bool { return stateAt(t, guest, p.PID).State == PartInvited })
			if _, e = guest.AcceptParticipation(ctx, p.PID); e != nil {
				t.Fatal(e)
			}
			eventually(t, "acceptance", func() bool { return stateAt(t, w.alice, p.PID).HumanActive() })
			if _, e = guest.RenameSharedTopic(ctx, conv, topic, "before"); e != nil {
				t.Fatal(e)
			}
			old, _, _ := guest.SharedTopicTitle(conv, topic)
			if change == "held" {
				stray := protocol.ParticipationEvent{V: 1, Conv: conv, PID: p.PID, Type: protocol.EventDismiss, Prev: strings.Repeat("ab", 32), TS: time.Now().Unix(), Author: stateAuthor(t, w.alice)}
				stray.Sign(w.alice.id.Sign)
				raw, _ := json.Marshal(stray)
				if e = guest.store.addParticipationEvent(stray, raw); e != nil {
					t.Fatal(e)
				}
				if stateAt(t, guest, p.PID).Held != 1 {
					t.Fatal("fixture did not hold exact PID")
				}
			} else {
				key, e := identity.Generate()
				if e != nil {
					t.Fatal(e)
				}
				eventually(t, "original accepted baseline", func() bool {
					r, ok, e := w.alice.SharedTopicTitle(conv, topic)
					return e == nil && ok && r.LID == old.LID
				})
				baseline, _, _ := w.alice.SharedTopicTitle(conv, topic)
				rows, e := guest.historySourceRows(guest.store.db, "conv=? AND lid=? AND dir='out'", "id", 1, conv, old.LID)
				if e != nil || len(rows) != 1 {
					t.Fatalf("source %v %d", e, len(rows))
				}
				item, e := guest.historySourceItem(guest.store.db, rows[0])
				if e != nil {
					t.Fatal(e)
				}
				n := item.inner(conv)
				_, n.Root = rootOf(t, w.alice, conv)
				n.ID = protocol.NewID()
				n.LID = protocol.NewID()
				title := "changed-key rejected"
				n.TopicEvent = &envelope.TopicEvent{Action: "rename", Title: &title, Rev: old.Rev + 1}
				n.V, n.From, n.To, n.TS = 2, guest.Address, w.alice.Address, time.Now().Unix()
				recipient, e := w.alice.Self().Recipient()
				if e != nil {
					t.Fatal(e)
				}
				env, e := envelope.Seal(n, key.Sign, recipient)
				if e != nil {
					t.Fatal(e)
				}
				if e = w.alice.accept(ctx, env); e != nil {
					t.Fatal(e)
				}
				got, ok, e := w.alice.SharedTopicTitle(conv, topic)
				if e != nil || !ok || got != baseline || inboxCount(t, w.alice, `id=?`, env.ID) != 0 {
					t.Fatal("changed original host key admitted metadata")
				}
				return
			}
			var before, after int
			guest.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND topic_event IS NOT NULL`, conv).Scan(&before)
			if _, e = guest.RenameSharedTopic(ctx, conv, topic, "must reject"); e == nil {
				t.Fatal("changed authority renamed")
			}
			guest.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND topic_event IS NOT NULL`, conv).Scan(&after)
			got, ok, e := guest.SharedTopicTitle(conv, topic)
			if e != nil || !ok || got != old || before != after {
				t.Fatalf("failed authority advanced metadata: %+v %v counts %d/%d", got, e, before, after)
			}
		})
	}
	t.Run("group transaction and removed member", func(t *testing.T) {
		w, carol, p, stops := groupTurnsFixture(t)
		a := w.alice
		stops[a]()
		ctx := tctx(t)
		conv := p.State.Conv
		topic := protocol.NewID()
		old, found, e := a.SharedTopicTitle(conv, topic)
		if e != nil || found {
			t.Fatal("unexpected seed")
		}
		next := p
		next.State.Seq++
		next.State.Prev = p.State.Hash()
		next.State.Title = "authority successor"
		next, e = a.SignGroupState(ctx, next)
		if e != nil {
			t.Fatal(e)
		}
		beforeOutbox = func() {
			a.NoteGroupHead(protocol.GroupHead{Conv: conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: next.State.Seq, Hash: next.State.Hash()})
		}
		defer func() { beforeOutbox = func() {} }()
		_, e = a.RenameSharedTopic(ctx, conv, topic, "cannot commit")
		beforeOutbox = func() {}
		if !errors.Is(e, ErrGroupContextPending) {
			t.Fatal("changed group authority committed", e)
		}
		got, ok, e := a.SharedTopicTitle(conv, topic)
		if e != nil || ok != found || got != old {
			t.Fatal("group CAS advanced on rejection")
		}
		// Another current member loses its own authority by leaving normally.
		if _, e = carol.LeaveGroup(ctx, conv); e != nil {
			t.Fatal(e)
		}
		if _, e = carol.RenameSharedTopic(ctx, conv, topic, "departed author"); e == nil {
			t.Fatal("removed member renamed")
		}
		if _, ok, e = carol.SharedTopicTitle(conv, topic); e != nil || ok {
			t.Fatal("removed author register advanced")
		}
	})
}


func TestSharedTopicConflictingPairIsQuarantined(t *testing.T) {
	w, conv, _ := dmFiles(t)
	ctx := tctx(t)
	topic := protocol.NewID()
	if _, e := w.alice.RenameSharedTopic(ctx, conv, topic, "admitted"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "baseline title", func() bool {
		r, ok, e := w.bob.SharedTopicTitle(conv, topic)
		return e == nil && ok && r.Title == "admitted"
	})
	old, _, _ := w.bob.SharedTopicTitle(conv, topic)
	_, raw := rootOf(t, w.alice, conv)
	title := "conflicting"
	env := craft(t, w.alice, w.bob, envelope.Inner{Conv: conv, Root: raw, LID: protocol.NewID(), Kind: envelope.KindMessage, Origin: envelope.OriginUI, Topic: topic, TopicEvent: &envelope.TopicEvent{Action: "rename", Title: &title, Rev: old.Rev}})
	if e := w.bob.accept(ctx, env); e != nil {
		t.Fatal("invalid metadata escaped quarantine", e)
	}
	got, ok, e := w.bob.SharedTopicTitle(conv, topic)
	if e != nil || !ok || got != old || inboxCount(t, w.bob, `id=?`, env.ID) != 0 {
		t.Fatal("conflict advanced register or inbox")
	}
	if heldReason(t, w.bob, env.ID) != reasonInvalid {
		t.Fatal("conflict not held as invalid")
	}
}
