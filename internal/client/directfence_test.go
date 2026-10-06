package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type directFenceTransport struct {
	base        http.RoundTripper
	afterUpload func()
	posts       int
}

func (x *directFenceTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/v1/direct/messages" {
		x.posts++
	}
	resp, e := x.base.RoundTrip(r)
	if e == nil && strings.HasSuffix(r.URL.Path, "/complete") && x.afterUpload != nil {
		f := x.afterUpload
		x.afterUpload = nil
		f()
	}
	return resp, e
}

func TestDirectInvitationFinalFenceDoesNotFallback(t *testing.T) {
	for _, mode := range []string{"downgrade", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			w, p := groupLifecycleFixture(t, false)
			runWith(t, w, w.bob, RunOptions{Listen: "127.0.0.1:0"})
			inv := groupLifecycleInvite(t, w, p, nil)
			sessions, e := w.alice.sessions(tctx(t), w.bob.Address)
			if e != nil || len(sessions) == 0 {
				t.Fatal(e)
			}
			var route protocol.SessionAd
			for _, s := range sessions {
				if s.Ad.Endpoint != "" {
					route = s.Ad
				}
			}
			if route.Endpoint == "" {
				t.Fatal("no direct route")
			}
			session := protocol.NewID()
			rec := protocol.CapsRecord{Address: w.bob.Address, Session: session, Caps: []string{protocol.CapEnv2, protocol.CapGroupInvitationControl, protocol.CapRoom}, TS: time.Now().Unix()}
			rec.Sign(w.bob.id.Sign)
			caps, _ := json.Marshal(rec)
			hubRT := &invitationCapsTransport{base: w.alice.hub.http.Transport, profile: protocol.Profile{Live: true, Sessions: []string{session}, Caps: []json.RawMessage{caps}}}
			w.alice.hub.http.Transport = hubRT
			var raw []byte
			if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND id IN(SELECT id FROM group_invitation_copies WHERE invitation=?)`, envelope.SubGroupInvite, inv.ID).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			var env envelope.Envelope
			json.Unmarshal(raw, &env)
			oldWrap := wrapTransport
			var directRT *directFenceTransport
			wrapTransport = func(base http.RoundTripper) http.RoundTripper {
				directRT = &directFenceTransport{base: base, afterUpload: func() {
					if mode == "cancel" {
						if e := w.alice.CancelGroupInvitation(tctx(t), inv.ID); e != nil {
							t.Error(e)
						}
					} else {
						rec.Caps = []string{protocol.CapEnv2, protocol.CapRoom}
						rec.Sign(w.bob.id.Sign)
						b, _ := json.Marshal(rec)
						hubRT.profile.Caps = []json.RawMessage{b}
					}
				}}
				return directRT
			}
			defer func() { wrapTransport = oldWrap }()
			result, e := w.alice.deliver(tctx(t), env, &route)
			want := stateConvWaiting
			if mode == "cancel" {
				want = stateNotDelivered
			}
			if e != nil || result.State != want || directRT == nil || directRT.afterUpload != nil || directRT.posts != 0 || hubRT.posts != 0 {
				t.Fatalf("final direct fence: %+v %v direct=%+v relay=%d", result, e, directRT, hubRT.posts)
			}
			var after []byte
			if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&after); e != nil || !bytes.Equal(raw, after) {
				t.Fatal("sealed copy changed at refused direct handoff")
			}
		})
	}
}
