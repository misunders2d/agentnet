package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestQueuedSocialControlRequiresTrustedLocalReader(t *testing.T) {
	for _, mode := range []string{"unknown", "changed", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, "")
			a := w.alice
			a.stopBackgroundPosts()
			original, err := a.Send(tctx(t), w.bob.Address, "synthetic signed original", "")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unknown":
				_, err = a.store.db.Exec(`DELETE FROM peers WHERE address=?`, w.bob.Address)
			case "changed":
				var replacement *identity.Identity
				replacement, err = identity.Generate()
				if err == nil {
					err = a.store.setPending(replacement.Public(w.bob.Address))
				}
			case "revoked":
				_, err = a.store.db.Exec(`INSERT INTO config(k,v) VALUES(?,?)`, revokedDevicePrefix+w.bob.Address+"/"+w.bob.Self().Fingerprint(), w.bob.Address)
			}
			if err != nil {
				t.Fatal(err)
			}
			base := a.hub.http.Transport
			requests := 0
			a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if mode != "unknown" || r.URL.Path != "/v1/agents/bob/laptop" {
					t.Errorf("local refusal read availability: %s", r.URL.Path)
				}
				return nil, context.DeadlineExceeded
			})
			defer func() { a.hub.http.Transport = base }()
			_, err = a.React(WithQueuedSend(tctx(t), ""), ControlRef{ID: original.ID, Fingerprint: a.Self().Fingerprint()}, "👍", false)
			if err == nil {
				t.Fatal("untrusted local reader accepted")
			}
			if mode == "unknown" && requests != 1 || mode != "unknown" && requests != 0 {
				t.Fatalf("unexpected identity discovery count: %d", requests)
			}
			var count int
			if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE ref_id=?`, original.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refusal left control copies: %d %v", count, err)
			}
		})
	}
}

// UI controls must commit using their already verified local identity and
// audience. Slow/unavailable profile, directory and chain reads belong to
// handover, where the sealed copy is checked again before leaving this home.
func TestQueuedSocialControlCommitDoesNotReadRelay(t *testing.T) {
	for _, scope := range []string{"device", "dm", "group"} {
		t.Run(scope, func(t *testing.T) {
			var w *world
			var conv string
			if scope == "group" {
				var packet GroupContext
				var stops map[*Agent]func()
				w, _, packet, stops = groupTurnsFixture(t)
				conv = packet.State.Conv
				for _, stop := range stops {
					stop()
				}
			} else {
				w = newWorld(t, "")
				if scope == "dm" {
					stop := runAgent(t, w.bob)
					persons(t, w.alice, w.bob)
					conv = newDM(t, w.alice, w.bob)
					stop()
				}
			}
			a := w.alice
			a.stopBackgroundPosts()
			a.stopArchivePosts()
			ref := ControlRef{Conv: conv, Fingerprint: a.Self().Fingerprint()}
			if scope == "device" {
				sent, err := a.Send(tctx(t), w.bob.Address, "synthetic local original", "")
				if err != nil {
					t.Fatal(err)
				}
				ref.ID = sent.ID
			} else {
				sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "synthetic local original"})
				if err != nil {
					t.Fatal(err)
				}
				ref.ID = sent.LID
			}
			base := a.hub.http.Transport
			calls := 0
			a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				// A timeout after a successful /version is the reported failure
				// seam; count all reads so removing only that seam cannot pass.
				time.Sleep(25 * time.Millisecond)
				if strings.HasSuffix(r.URL.Path, "/profile") {
					return nil, context.DeadlineExceeded
				}
				return base.RoundTrip(r)
			})
			defer func() { a.hub.http.Transport = base }()
			ctx := WithQueuedSend(tctx(t), "")
			for _, action := range []struct {
				name string
				run  func() (ControlSent, error)
			}{
				{"react", func() (ControlSent, error) { return a.React(ctx, ref, "👍", false) }},
				{"edit", func() (ControlSent, error) { return a.Revise(ctx, ref, "synthetic corrected text") }},
				{"delete", func() (ControlSent, error) { return a.Retract(ctx, ref, "") }},
			} {
				before, start := calls, time.Now()
				sent, err := action.run()
				t.Logf("%s local commit: %s, relay calls=%d, state=%s, err=%v", action.name, time.Since(start), calls-before, sent.State, err)
				if err != nil || sent.State != stateQueued || calls != before {
					t.Fatalf("%s depended on relay availability: %+v %v, requests=%d", action.name, sent, err, calls-before)
				}
			}
			controls, err := a.controlsOf(ref)
			if err != nil || !controls.Deleted {
				t.Fatalf("durable local controls missing: %+v %v", controls, err)
			}
		})
	}
}

func socialReaderProfile(t *testing.T, a *Agent, caps []string, wrongSigner *Agent) protocol.Profile {
	t.Helper()
	caps = slices.Clone(caps)
	slices.Sort(caps)
	record := protocol.CapsRecord{Address: a.Address, Session: protocol.NewID(), Caps: caps, TS: time.Now().Unix()}
	key := a.id.Sign
	if wrongSigner != nil {
		key = wrongSigner.id.Sign
	}
	record.Sign(key)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Profile{Sessions: []string{record.Session}, Caps: []json.RawMessage{raw}, Live: false}
}

// An unrelated waiting ordinary turn is not a control FIFO barrier. An
// offline reader with retained signed ctl3 works; unavailable support waits.
func TestSocialControlsOfflineReaderAndOrdinaryWait(t *testing.T) {
	for _, mode := range []string{"retained-supported", "zero-sessions", "unknown", "known-old"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, "")
			stopBob := runAgent(t, w.bob)
			persons(t, w.alice, w.bob)
			conv := newDM(t, w.alice, w.bob)
			original, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "original signed turn"})
			if err != nil {
				t.Fatal(err)
			}
			eventually(t, "recipient retains original", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
			stopBob()
			a := w.alice
			a.stopBackgroundPosts()
			a.stopArchivePosts()
			profile := protocol.Profile{}
			base := a.hub.http.Transport
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == "GET" && r.URL.Path == "/v1/agents/bob/laptop/profile" {
					raw, _ := json.Marshal(profile)
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
				}
				return base.RoundTrip(r)
			})
			a.hub.http.Transport = transport
			defer func() { a.hub.http.Transport = base }()
			pending, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "ordinary turn waiting for its reader"})
			if err != nil || pending.State != stateConvWaiting {
				t.Fatalf("ordinary waiting precondition: %+v %v", pending, err)
			}
			profile = socialReaderProfile(t, w.bob, ownCaps, nil)
			switch mode {
			case "zero-sessions":
				profile.Sessions = nil
			case "unknown":
				profile.Caps = nil
			case "known-old":
				profile = socialReaderProfile(t, w.bob, without(ownCaps, protocol.CapControl), nil)
			}
			ref := ControlRef{Conv: conv, ID: original.LID, Fingerprint: a.Self().Fingerprint()}
			// Composition knows only that an exact sealed copy is durable.
			// The first handover below establishes supported vs waiting.
			want := stateQueued
			ctx := WithQueuedSend(tctx(t), "")
			reaction, err := a.React(ctx, ref, "👍", false)
			if err != nil || reaction.State != want || len(reaction.Skipped) != 0 {
				t.Fatalf("local reaction: %+v %v, want %s", reaction, err, want)
			}
			if controls, err := a.controlsOf(ref); err != nil || len(controls.Reactions) != 1 || !controls.Reactions[0].Mine {
				t.Fatalf("durable local reaction: %+v %v", controls, err)
			}
			if edit, err := a.Revise(ctx, ref, "corrected locally"); err != nil || edit.State != want {
				t.Fatalf("local revision: %+v %v", edit, err)
			}
			if controls, err := a.controlsOf(ref); err != nil || !controls.Edited || controls.Text != "corrected locally" {
				t.Fatalf("durable local revision: %+v %v", controls, err)
			}
			if deletion, err := a.Retract(ctx, ref, ""); err != nil || deletion.State != want {
				t.Fatalf("local deletion: %+v %v", deletion, err)
			}
			if controls, err := a.controlsOf(ref); err != nil || !controls.Deleted {
				t.Fatalf("durable local deletion: %+v %v", controls, err)
			}
			rows, err := a.store.db.Query(`SELECT id,envelope,state,required_cap,recipient_fp FROM outbox WHERE conv=? AND ref_id=? ORDER BY rowid`, conv, original.LID)
			if err != nil {
				t.Fatal(err)
			}
			wires := map[string]string{}
			for rows.Next() {
				var id, wire, state, required, fp string
				if err = rows.Scan(&id, &wire, &state, &required, &fp); err != nil {
					t.Fatal(err)
				}
				if state != want || required != protocol.CapControl || fp != w.bob.Self().Fingerprint() {
					t.Fatalf("copy lost waiting/key/capability fence: %s %s %s", state, required, fp)
				}
				wires[id] = wire
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			if len(wires) != 3 {
				t.Fatalf("expected three retained sealed controls, got %d", len(wires))
			}
			if err = a.FlushOutbox(tctx(t)); err != nil {
				t.Fatal(err)
			}
			if outboxState(t, a, pending.ID) != stateConvWaiting {
				t.Fatal("control handover changed the unrelated ordinary waiting turn")
			}
			for id := range wires {
				state := outboxState(t, a, id)
				if mode == "retained-supported" && state != protocol.StateCustody || mode != "retained-supported" && state != stateConvWaiting {
					t.Fatalf("offline signed support vs ordinary wait: %s", state)
				}
			}
			if mode != "retained-supported" {
				// Wrong signed proof cannot release an already-sealed waiting copy.
				profile = socialReaderProfile(t, w.bob, ownCaps, a)
				features, err := a.relayFeatures(tctx(t))
				if err != nil {
					t.Fatal(err)
				}
				a.releaseConv(tctx(t), features)
				for id := range wires {
					if outboxState(t, a, id) != stateConvWaiting {
						t.Fatal("invalid signer released a waiting control")
					}
				}
				profile = socialReaderProfile(t, w.bob, ownCaps, nil)
				a.releaseConv(tctx(t), features)
				if err = a.FlushOutbox(tctx(t)); err != nil {
					t.Fatal(err)
				}
			}
			for id, originalWire := range wires {
				var wire string
				if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, id).Scan(&wire); err != nil || wire != originalWire || outboxState(t, a, id) != protocol.StateCustody {
					t.Fatalf("release replaced a sealed copy or invented admission: %v", err)
				}
				var env envelope.Envelope
				if err = json.Unmarshal([]byte(wire), &env); err != nil {
					t.Fatal(err)
				}
				if err = w.bob.verifyAndStore(tctx(t), env); err != nil {
					t.Fatal(err)
				}
			}
			if message := convMsgByID(t, w.bob, conv, original.ID); !message.Deleted {
				t.Fatal("recipient did not apply the retained signed controls")
			}
		})
	}
}
