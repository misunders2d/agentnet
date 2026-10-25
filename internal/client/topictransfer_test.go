package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func transferAll(t *testing.T, a *Agent, r TopicTransferReview) TopicTransferReview {
	t.Helper()
	for r.Cursor < r.TotalBatches {
		before := r.Cursor
		next, e := a.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: r.Cursor})
		if e != nil {
			h, _ := transferHeaderIn(a.store.db, r.ID)
			ref, _ := transferRefIn(a.store.db, r.ID, before)
			stamp, _, _, x := a.transferAuthority(a.store.db, h.Review.TopicTransferRequest)
			page, _, pe := a.transferPage(h, ref, before)
			t.Fatalf("batch %d: %v; authoritysame=%t autherr=%v pageerr=%v pageitems=%d", before, e, stamp == h.Authority, x, pe, len(page.Items))
		}
		r = next
		if r.Cursor <= before {
			t.Fatal("cursor did not advance")
		}
	}
	return r
}
func TestTopicTransferWholeSnapshotCapacityAndRetry(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	source := packet.State.Conv
	destination := newDM(t, w.alice, w.bob)
	for i := 0; i < 65; i++ {
		if _, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "ordinary whole-topic item"}); e != nil {
			t.Fatal(e)
		}
	}
	large := strings.Repeat("界\n\"", 12000) + " [@Agent](agentnet:agent/" + protocol.NewID() + ")"
	if _, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: large}); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "retained.bin")
	if e := os.WriteFile(path, bytes.Repeat([]byte{42}, 120<<10), 0600); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		if _, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "file item", Files: []OutgoingFile{{Name: "retained.bin", Path: path}}}); e != nil {
			t.Fatal(e)
		}
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: source, Destination: destination, NewTopic: true, Title: "Continued topic"})
	if e != nil {
		t.Fatal(e)
	}
	if r.TotalItems <= 64 || r.TotalFiles != 10 || r.TotalBytes <= 1<<20 || r.TotalBatches <= r.TotalItems {
		t.Fatalf("whole snapshot narrowed: %+v", r)
	}
	var raw string
	if e = w.alice.store.db.QueryRow(`SELECT header FROM topic_transfer_plans WHERE id=?`, r.ID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(raw, "ordinary whole-topic") || strings.Contains(raw, "界") {
		t.Fatal("preview plaintext retained in plan")
	}
	rows, e := w.alice.store.db.Query(`SELECT reference FROM topic_transfer_manifest WHERE plan=?`, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		if e = rows.Scan(&raw); e != nil {
			t.Fatal(e)
		}
		if strings.Contains(raw, "界") || strings.Contains(raw, "ordinary whole-topic") {
			t.Fatal("preview plaintext retained in manifest")
		}
	}
	rows.Close()
	if _, e = w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "arrived after cutoff"}); e != nil {
		t.Fatal(e)
	}
	first, e := w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0})
	if e != nil {
		h, _ := transferHeaderIn(w.alice.store.db, r.ID)
		ref, _ := transferRefIn(w.alice.store.db, r.ID, 0)
		stamp, _, _, ae := w.alice.transferAuthority(w.alice.store.db, h.Review.TopicTransferRequest)
		m, me := w.alice.transferMessage(h.Review.TopicTransferRequest, ref)
		item := transferItem(m)
		guard, ge := w.alice.transferGuard(w.alice.store.db, h.Review.TopicTransferRequest, m)
		_, _, pe := w.alice.transferPage(h, ref, 0)
		t.Fatalf("firstbatch %v authoritysame=%t autherr=%v messageerr=%v hashsame=%t author=%t ids=%t guard=%t guarderr=%v pageerr=%v files=%d", e, stamp == h.Authority, ae, me, item.Hash == ref.Hash, item.Author == ref.Author, m.LID == ref.LID, guard == ref.Guard, ge, pe, len(m.Attachments))
	}
	again, e := w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0})
	if e != nil || again.Cursor != first.Cursor {
		t.Fatalf("lost response retry advanced next batch: %+v %v", again, e)
	}
	r = transferAll(t, w.alice, again)
	if r.State != "queued" {
		t.Fatalf("queue completion: %+v", r)
	}
	pointer, e := w.alice.TopicTransferLocation(TopicTransferRequest{Source: source})
	if e != nil || pointer == nil || pointer.Operation != r.ID || !pointer.Available {
		t.Fatalf("completed private pointer: %+v %v", pointer, e)
	}
	msgs, e := w.alice.ConversationMessages(destination)
	if e != nil {
		t.Fatal(e)
	}
	if len(msgs) != r.TotalBatches {
		t.Fatalf("want %d inert copies, got %d", r.TotalBatches, len(msgs))
	}
	for _, m := range msgs {
		if m.Kind != envelope.KindMessage || m.Target != nil || m.PID != "" || m.AgentID != "" || m.ReplyTo != "" || strings.Contains(m.Body, "arrived after cutoff") || strings.Contains(m.Body, "agentnet:agent/") {
			t.Fatalf("active or late source context imported: %+v", m)
		}
	}
	if n := count(t, w.alice, "inbox WHERE conv='"+destination+"' AND kind IN ('question','task')"); n != 0 {
		t.Fatalf("%d quotation jobs", n)
	}
}

func TestTopicTransferSourceChangeCancelAndErasure(t *testing.T) {
	w, _, packet, _ := groupTurnsFixture(t)
	source := newDM(t, w.alice, w.bob)
	dest := packet.State.Conv
	first, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "first"})
	if e != nil {
		t.Fatal(e)
	}
	second, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "second"})
	if e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: source, Destination: dest})
	if e != nil {
		t.Fatal(e)
	}
	r, e = w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0})
	if e != nil {
		t.Fatal(e)
	}
	remaining, _ := transferRefIn(w.alice.store.db, r.ID, r.Cursor)
	if _, e = w.alice.Revise(tctx(t), ControlRef{Conv: source, ID: remaining.LID, Fingerprint: w.alice.Self().Fingerprint()}, "changed after review"); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: r.Cursor}); !errors.Is(e, ErrTopicTransferChanged) {
		t.Fatalf("edited unqueued source accepted: %v", e)
	}
	if p, e := w.alice.TopicTransferLocation(TopicTransferRequest{Source: source}); e != nil || p != nil {
		t.Fatalf("partial transfer marked moved: %+v %v", p, e)
	}
	canceled, e := w.alice.CancelTopicTransfer(TopicTransferAction{ID: r.ID, Token: r.Token})
	if e != nil || canceled.State != "canceled" || canceled.Cursor != 1 {
		t.Fatalf("partial cancel: %+v %v", canceled, e)
	}
	if _, e = w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 1}); e == nil {
		t.Fatal("canceled transfer advanced")
	}
	r, e = w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: source, Destination: dest})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.DeleteConversation(tctx(t), source); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0}); e == nil {
		t.Fatal("erased source disclosed")
	}
	_ = first
	_ = second
}

func TestTopicTransferLegacySourceAndPrivateLinkedLocation(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	destination := newDM(t, w.alice, w.bob)
	first, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "legacy exact source"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "legacy child", ReplyTo: first.ID}); e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	if r.TotalItems != 2 {
		t.Fatalf("legacy whole source: %+v", r)
	}
	r = transferAll(t, w.alice, r)
	phone := linked(t, w.alice)
	eventually(t, "completed source location reaches late own phone", func() bool {
		p, e := phone.TopicTransferLocation(TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID})
		return e == nil && p != nil && p.Operation == r.ID
	})
	if n := count(t, w.bob, "topic_locations"); n != 0 {
		t.Fatalf("private destination leaked to another person: %d", n)
	}
	bobSource, e := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "Bob's independent source"})
	if e != nil {
		t.Fatal(e)
	}
	bobReview, e := w.bob.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.alice.Address, Thread: bobSource.ID, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	bobReview = transferAll(t, w.bob, bobReview)
	bobPhone := linked(t, w.bob)
	eventually(t, "second person's private pointer follows own phone", func() bool {
		p, e := bobPhone.TopicTransferLocation(TopicTransferRequest{Peer: w.alice.Address, Thread: bobSource.ID})
		return e == nil && p != nil && p.Operation == bobReview.ID
	})
	if p, e := phone.TopicTransferLocation(TopicTransferRequest{Peer: w.alice.Address, Thread: bobSource.ID}); e != nil || p != nil {
		t.Fatalf("second person's pointer crossed person boundary: %+v %v", p, e)
	}
	if n := count(t, phone, "inbox WHERE sub='topic-state-sync'"); n != 0 {
		t.Fatalf("%d pointer carriers became chat messages", n)
	}
	var copies int
	if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM topic_location_copies WHERE owner=(SELECT person FROM persons WHERE state='self')`).Scan(&copies); e != nil || copies == 0 {
		t.Fatalf("late pointer copy ledger %d %v", copies, e)
	}
	// The strict V1 parser must continue to refuse a V2 location carrier.
	var fact protocol.TopicLocation
	if e = w.alice.store.db.QueryRow(`SELECT fact FROM topic_locations LIMIT 1`).Scan(new(string)); e != nil {
		t.Fatal(e)
	}
	fact = protocol.TopicLocation{Scope: w.bob.Address, Topic: first.ID, Destination: destination, Operation: r.ID, At: 1, Writer: w.alice.Self().Fingerprint(), Revision: 1}
	person, _, _ := w.alice.Person()
	raw, _ := json.Marshal(protocol.TopicLocationSync{V: 2, Person: person.Person, Roster: person.Roster, Locations: []protocol.TopicLocation{fact}})
	if _, e = protocol.ParseTopicStateSync(raw); e == nil {
		t.Fatal("V1 parser accepted V2")
	}
}

func TestTopicTransferNewTopicKeepsAuthority(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	source := newDM(t, w.alice, w.bob)
	for i := 0; i < 2; i++ {
		if _, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "small source"}); e != nil {
			t.Fatal(e)
		}
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: source, Destination: p.State.Conv, NewTopic: true, Title: "New destination"})
	if e != nil {
		t.Fatal(e)
	}
	transferAll(t, w.alice, r)
}

func TestTopicTransferLateSourceDoesNotInvalidate(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	dest := newDM(t, w.alice, w.bob)
	if _, e := w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "first"}); e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: p.State.Conv, Destination: dest, NewTopic: true, Title: "Target"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.SendConv(tctx(t), p.State.Conv, ConvOutgoing{Body: "late"}); e != nil {
		t.Fatal(e)
	}
	transferAll(t, w.alice, r)
}

func TestTopicTransferRestartKeepsExactCursorAndEnvelope(t *testing.T) {
	w, destination := queuedDMWorld(t)
	first, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "original first"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "original child", ReplyTo: first.ID}); e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	pause := pausePosts(w.alice)
	queued, e := w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0})
	if e != nil {
		t.Fatal(e)
	}
	<-pause.entered
	ref, e := transferRefIn(w.alice.store.db, r.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	var before string
	if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND lid=? LIMIT 1`, destination, ref.LIDOut).Scan(&before); e != nil {
		t.Fatal(e)
	}
	w.alice.posting.Lock()
	w.alice.posting.cancel()
	w.alice.posting.Unlock()
	if e = w.alice.Close(); e != nil {
		t.Fatal(e)
	}
	resumed, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer resumed.Close()
	status, e := resumed.TopicTransferStatus(TopicTransferAction{Peer: w.bob.Address, Thread: first.ID})
	if e != nil || status == nil || status.ID != r.ID || status.Cursor != queued.Cursor {
		t.Fatalf("durable discoverable snapshot: %+v %v", status, e)
	}
	again, e := resumed.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0})
	if e != nil || again.Cursor != 1 {
		t.Fatalf("lost response retry after reopen: %+v %v", again, e)
	}
	var after string
	if e = resumed.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND lid=? LIMIT 1`, destination, ref.LIDOut).Scan(&after); e != nil || after != before {
		t.Fatal("reopen regenerated queued envelope", e)
	}
	r = transferAll(t, resumed, again)
	if r.State != "queued" {
		t.Fatal(r.State)
	}
	if e = resumed.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	eventually(t, "only two importer quotations after reopen", func() bool { m, e := w.bob.ConversationMessages(destination); return e == nil && len(m) == 2 })
}

func TestTopicTransferClaimRefusesChangedAudience(t *testing.T) {
	w, carol, p, _ := groupTurnsFixture(t)
	source := newDM(t, w.alice, w.bob)
	if _, e := w.alice.SendConv(tctx(t), source, ConvOutgoing{Body: "reviewed context"}); e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: source, Destination: p.State.Conv})
	if e != nil {
		t.Fatal(e)
	}
	previous := beforeOutbox
	defer func() { beforeOutbox = previous }()
	beforeOutbox = func() {
		beforeOutbox = previous
		if _, e := w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, carol.Address); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0}); !errors.Is(e, ErrTopicTransferChanged) {
		t.Fatalf("audience changed during encryption: %v", e)
	}
	status, e := w.alice.TopicTransferStatus(TopicTransferAction{ID: r.ID})
	if e != nil || status.Cursor != 0 {
		t.Fatalf("refused claim advanced cursor: %+v %v", status, e)
	}
}

func TestTopicTransferThousandRowsUsesBoundedApply(t *testing.T) {
	w, destination := queuedDMWorld(t)
	// Synthetic offline producer: signed originals use the normal durable
	// outbox, while no daemon posts this capacity fixture to its peer.
	w.alice.posting.Lock()
	w.alice.posting.closed = true
	w.alice.posting.Unlock()
	anchor := ""
	body := strings.Repeat("snapshot capacity ", 80)
	for i := 0; i < 1000; i++ {
		sent, e := w.alice.SendMessage(WithQueuedSend(tctx(t), protocol.NewID()), Outgoing{To: w.bob.Address, Body: body, ReplyTo: anchor})
		if e != nil {
			t.Fatal(e)
		}
		if anchor == "" {
			anchor = sent.ID
		}
	}
	start := time.Now()
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.bob.Address, Thread: anchor, Destination: destination})
	preview := time.Since(start)
	if e != nil {
		t.Fatal(e)
	}
	if r.TotalItems != 1000 || r.TotalBytes < 1<<20 {
		t.Fatalf("capacity source narrowed: %+v", r)
	}
	start = time.Now()
	next, e := w.alice.ApplyTopicTransfer(tctx(t), TopicTransferAction{ID: r.ID, Token: r.Token, Cursor: 0})
	apply := time.Since(start)
	if e != nil || next.Cursor != 1 {
		t.Fatalf("bounded apply: %+v %v", next, e)
	}
	t.Logf("synthetic signed1000rows bytes=%d preview=%v one_apply=%v; exact original/control hydration after one source enumeration", r.TotalBytes, preview, apply)
}

func TestTopicTransferQuotedCanonicalMentionCannotReactOrRun(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	host := w.bob
	for _, a := range []*Agent{w.alice, w.bob, carol} {
		roomReader(t, a)
		addCapSuccessor(t, a, protocol.CapMentionReaction)
	}
	setResponder(t, host, "agentstub", stub.dir, time.Minute)
	p, e := w.alice.InviteAgent(tctx(t), packet.State.Conv, host.Address, nil, nil, "")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "destination helper invitation", func() bool { return stateAt(t, host, p.PID).Invite != "" })
	if !stateAt(t, host, p.PID).Claimable() {
		if _, e = host.AcceptParticipation(tctx(t), p.PID); e != nil {
			t.Fatal(e)
		}
	}
	eventually(t, "destination helper admitted", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	source := newDM(t, w.alice, w.bob)
	if _, e = w.alice.SendConv(tctx(t), source, ConvOutgoing{Kind: envelope.KindTask, Body: "Old task for [@Agent](agentnet:agent/" + p.PID + "). Email a@b.example and code @value stay readable."}); e != nil {
		t.Fatal(e)
	}
	before := count(t, host, "inbox WHERE conv='"+packet.State.Conv+"' AND kind IN ('question','task')")
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Source: source, Destination: packet.State.Conv})
	if e != nil {
		t.Fatal(e)
	}
	r = transferAll(t, w.alice, r)
	ref, _ := transferRefIn(w.alice.store.db, r.ID, 0)
	eventually(t, "inert quotation reaches following helper host", func() bool {
		m, n := convMsg(t, host, packet.State.Conv, func(m ConvMessage) bool { return m.LID == ref.LIDOut })
		return n == 1 && m.Kind == envelope.KindMessage && strings.Contains(m.Body, "a@b.example") && strings.Contains(m.Body, "@value")
	})
	if _, e = host.SendAgentReaction(tctx(t), p.PID, ControlRef{Conv: packet.State.Conv, ID: ref.LIDOut, Fingerprint: w.alice.Self().Fingerprint()}, "👍", false); e == nil || !strings.Contains(e.Error(), "canonical agent mention") {
		t.Fatalf("quoted canonical mention was not refused by exact mention guard: %v", e)
	}
	if stub.runs() != 0 {
		t.Fatalf("quoted old task ran helper %d times", stub.runs())
	}
	if n := count(t, host, "inbox WHERE conv='"+packet.State.Conv+"' AND kind IN ('question','task')"); n != before {
		t.Fatalf("quotation replayed a request: before%d after%d", before, n)
	}
}

func TestTopicTransferLocationWaitsForV2WithoutBlockingV1(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "phone capabilities", func() bool {
		return w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile) == nil && len(profile.Sessions) > 0
	})
	publish := func(ts int64, caps []string) {
		for _, session := range profile.Sessions {
			r := protocol.CapsRecord{Address: phone.Address, Session: session, TS: ts, Caps: caps}
			r.Sign(phone.id.Sign)
			if e := phone.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); e != nil {
				t.Fatal(e)
			}
		}
	}
	old := slices.Clone(ownCaps)
	for i, c := range old {
		if c == protocol.CapTopicStateSyncV2 {
			old[i] = protocol.CapTopicStateSync
		}
	}
	publish(time.Now().Unix()+100, old)
	destination := newDM(t, w.alice, w.bob)
	first, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "source topic"})
	if e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	r = transferAll(t, w.alice, r)
	if _, e = w.alice.syncTopicLocations(); e != nil {
		t.Fatal(e)
	}
	var raw string
	if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND required_cap=? LIMIT 1`, envelope.SubTopicStateSync, protocol.CapTopicStateSyncV2).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var carrier envelope.Envelope
	_ = json.Unmarshal([]byte(raw), &carrier)
	if result, e := w.alice.deliver(tctx(t), carrier, nil); e != nil || result.State != stateConvWaiting {
		t.Fatalf("V1 reader consumed V2 location: %+v %v", result, e)
	}
	second, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	second = transferAll(t, w.alice, second)
	if _, e = w.alice.syncTopicLocations(); e != nil {
		t.Fatal(e)
	}
	if n := count(t, w.alice, "outbox WHERE sub='topic-state-sync' AND required_cap='tss2'"); n != 1 {
		t.Fatalf("older reader accumulated %d V2 carriers", n)
	}
	if e = w.alice.changeTopicMark(w.bob.Address, first.ID, protocol.TopicMarkArchived, 1); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.syncTopicMarks(); e != nil {
		t.Fatal(e)
	}
	if e = w.alice.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	eventually(t, "V1 marks flow past waiting V2 location", func() bool { return markAt(t, phone, w.bob.Address, first.ID).Mark == protocol.TopicMarkArchived })
	if pointer, e := phone.TopicTransferLocation(TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID}); e != nil || pointer != nil {
		t.Fatalf("old reader got location: %+v %v", pointer, e)
	}
	publish(time.Now().Unix()+200, slices.Clone(ownCaps))
	features, e := w.alice.relayFeatures(tctx(t))
	if e != nil {
		t.Fatal(e)
	}
	w.alice.releaseConv(tctx(t), features)
	if e = w.alice.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.syncTopicLocations(); e != nil {
		t.Fatal(e)
	}
	if e = w.alice.FlushOutbox(tctx(t)); e != nil {
		t.Fatal(e)
	}
	eventually(t, "upgrade drains exact location backlog", func() bool {
		pointer, e := phone.TopicTransferLocation(TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID})
		return e == nil && pointer != nil && pointer.Operation == second.ID
	})
	if n := count(t, phone, "inbox WHERE sub='topic-state-sync'"); n != 0 {
		t.Fatalf("%d private carriers became chat", n)
	}
}

func TestTopicTransferLargeOriginalEncodedFragments(t *testing.T) {
	w, destination := queuedDMWorld(t)
	body := strings.Repeat("雪", 58<<10) + strings.Repeat("\n\"", 1000)
	if len(body) <= HistoryContributionMaxBody {
		t.Fatal("fixture must exceed the old single-quote body limit")
	}
	first, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: body})
	if e != nil {
		t.Fatal(e)
	}
	r, e := w.alice.PreviewTopicTransfer(tctx(t), TopicTransferRequest{Peer: w.bob.Address, Thread: first.ID, Destination: destination})
	if e != nil {
		t.Fatal(e)
	}
	var reconstructed strings.Builder
	for after := 0; after < r.TotalBatches; after++ {
		page, e := w.alice.TopicTransferPage(TopicTransferAction{ID: r.ID, Token: r.Token, After: after})
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(page)
		if e != nil || len(raw) > 48<<10 || !utf8.ValidString(page.Items[0].Body) {
			t.Fatalf("encoded UTF8 preview bound: %d %v", len(raw), e)
		}
		if strings.Contains(page.Body, "?conv=)") || strings.Contains(page.Body, "parent not shared") {
			t.Fatal("legacy source navigation/reply attribution is false")
		}
		reconstructed.WriteString(page.Items[0].Body)
	}
	if reconstructed.String() != body {
		t.Fatal("oversized original fragments lost source text")
	}
	r = transferAll(t, w.alice, r)
	if r.State != "queued" {
		t.Fatal(r.State)
	}
	t.Logf("valid original bytes=%d; bounded quote parts=%d", len(body), r.TotalBatches)
}
