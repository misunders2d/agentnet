package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An admitted file request arriving in a bulk flush gets the next upkeep turn.
// A retryable older history copy must not prevent later copies making progress.
func TestHistoryFlushYieldsToRequestedFile(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	path, want := writeFile(t, t.TempDir(), "catchup.bin", 128)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "kept file", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	phone, awaited, _ := linkPhone(t, w.alice, "file-fairness")
	request := pendingLink(t, w.alice)
	stop()
	if err = w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	// This regression exercises legacy per-carrier flushing. The link's
	// waiting session may advertise modern archive support; explicitly
	// publish the legacy reader before constructing its history backlog.
	stopPhone := runAgent(t, phone)
	waitNamedAgentCaps(t, phone)
	signCapsAfter(t, phone, without(ownCaps, protocol.CapHistoryArchive))
	label, name, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "signed legacy history reader", func() bool {
		var profile protocol.Profile
		err := phone.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile)
		return err == nil && profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapOwnSyncV3) && !profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapHistoryArchive)
	})
	stopPhone()
	a := w.alice
	// From here the fixture drives sends/upkeep manually. Join the
	// independent workers before installing mutable HTTP hooks.
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	phone.stopBackgroundPosts()
	phone.stopArchivePosts()
	_, root := rootOf(t, a, conv)
	for range 3 {
		env := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Body: "backfill", Conv: conv, Root: root, LID: protocol.NewID(), Origin: envelope.OriginUI})
		if err = a.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	envs, err := a.store.queued()
	if err != nil {
		t.Fatal(err)
	}
	var history []string
	for _, env := range envs {
		if env.To != phone.Address {
			continue
		}
		if err = phone.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		var sub string
		a.store.db.QueryRow(`SELECT coalesce(sub,'') FROM outbox WHERE id=?`, env.ID).Scan(&sub)
		if sub == envelope.SubHistory {
			history = append(history, env.ID)
		}
	}
	if len(history) < 3 {
		var rows string
		a.store.db.QueryRow(`SELECT coalesce(group_concat(sub || ':' || state), '') FROM outbox WHERE recipient=?`, phone.Address).Scan(&rows)
		t.Fatalf("history backlog: %d; source rows: %s", len(history), rows)
	}
	// A turn only the phone received gives its bulk upkeep a copy to make
	// for alice: what came from alice is never copied back to her.
	only := craft(t, w.bob, phone, envelope.Inner{Kind: envelope.KindMessage, Body: "only on the phone", Conv: conv, Root: root, LID: protocol.NewID(), Origin: envelope.OriginUI})
	if err = phone.verifyAndStore(tctx(t), only); err != nil {
		t.Fatal(err)
	}
	msgs, err := phone.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var fileID string
	for _, msg := range msgs {
		if msg.LID == sent.LID {
			fileID = msg.ID
		}
	}
	if fileID == "" {
		t.Fatal("file history missing")
	}
	if err = phone.RequestFile(tctx(t), fileID, 0); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='file'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var requestEnv envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &requestEnv); err != nil {
		t.Fatal(err)
	}
	// The outgoing request must get its turn before a fresh bulk-history
	// upkeep pass, even though the phone already has history work due.
	phoneBase := phone.hub.http.Transport
	beforeUpkeep := false
	phone.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/messages" {
			body, e := r.GetBody()
			if e != nil {
				return nil, e
			}
			var env envelope.Envelope
			e = json.NewDecoder(body).Decode(&env)
			body.Close()
			if e != nil {
				return nil, e
			}
			if env.ID == requestEnv.ID {
				var copies int
				if e = phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub='history'`).Scan(&copies); e != nil {
					return nil, e
				}
				beforeUpkeep = copies == 0
			}
		}
		return phoneBase.RoundTrip(r)
	})
	phone.convWork.due(convHistory)
	phone.sync(tctx(t))
	phone.stopBackgroundPosts()
	phone.stopArchivePosts()
	phone.hub.http.Transport = phoneBase
	if !beforeUpkeep {
		t.Fatal("queued file request waited behind bulk upkeep")
	}
	var catchupCopies int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub='history'`).Scan(&catchupCopies); err != nil || catchupCopies == 0 {
		t.Fatalf("file priority prevented bulk history upkeep: %d %v", catchupCopies, err)
	}
	base := a.hub.http.Transport
	injected := false
	var posted []string
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/messages" {
			return base.RoundTrip(r)
		}
		body, e := r.GetBody()
		if e != nil {
			return nil, e
		}
		defer body.Close()
		var env envelope.Envelope
		if e = json.NewDecoder(body).Decode(&env); e != nil {
			return nil, e
		}
		if env.ID == history[0] {
			return nil, errors.New("synthetic retryable first-copy outage")
		}
		posted = append(posted, env.ID)
		resp, e := base.RoundTrip(r)
		if e == nil && env.ID == history[1] && !injected {
			injected = true
			e = a.verifyAndStore(tctx(t), requestEnv)
		}
		return resp, e
	})
	defer func() {
		a.stopBackgroundPosts()
		a.stopArchivePosts()
		a.hub.http.Transport = base
	}()
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if !injected {
		t.Fatal("retryable first copy starved reachable later history")
	}
	state, _, _, err := a.store.outboxState(history[2])
	if err != nil || state != stateQueued {
		t.Fatalf("bulk flush did not yield with remaining history: %s %v", state, err)
	}
	var pending int
	a.store.db.QueryRow(`SELECT count(*) FROM file_serves WHERE state='pending'`).Scan(&pending)
	if pending != 1 {
		t.Fatalf("signed request not pending: %d", pending)
	}
	a.convSync(tctx(t))
	var offerID string
	if err = a.store.db.QueryRow(`SELECT id FROM outbox WHERE sub='file'`).Scan(&offerID); err != nil {
		t.Fatal(err)
	}
	posted = nil
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if len(posted) == 0 || posted[0] != offerID {
		t.Fatal("prepared file waited behind bulk history")
	}
	state, _, _, err = a.store.outboxState(history[2])
	if err != nil || state == stateQueued {
		t.Fatalf("file response starved later history: %s %v", state, err)
	}
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, offerID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var offer envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &offer); err != nil {
		t.Fatal(err)
	}
	if err = phone.verifyAndStore(tctx(t), offer); err != nil {
		t.Fatal(err)
	}
	r, _, err := phone.OpenAttachment(tctx(t), fileID, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(got) != string(want) {
		t.Fatal("requested encrypted file differs")
	}
}
