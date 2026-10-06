package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type invitationCapsTransport struct {
	base        http.RoundTripper
	profile     protocol.Profile
	posts       int
	afterUpload func()
}

func (x *invitationCapsTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/profile") {
		b, _ := json.Marshal(x.profile)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(b)), Request: r}, nil
	}
	if r.Method == "POST" && r.URL.Path == "/v1/messages" {
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

func TestGroupInvitationControlMixedVersionFinalHandoff(t *testing.T) {
	w, p := groupLifecycleFixture(t, false)
	inv := groupLifecycleInvite(t, w, p, nil)
	oldSession, newSession := protocol.NewID(), protocol.NewID()
	old := protocol.CapsRecord{Address: w.bob.Address, Session: oldSession, Caps: []string{protocol.CapEnv2, protocol.CapRoom}, TS: time.Now().Unix()}
	old.Sign(w.bob.id.Sign)
	current := protocol.CapsRecord{Address: w.bob.Address, Session: newSession, Caps: []string{protocol.CapEnv2, protocol.CapGroupInvitationControl, protocol.CapRoom}, TS: time.Now().Unix()}
	current.Sign(w.bob.id.Sign)
	oldRaw, _ := json.Marshal(old)
	newRaw, _ := json.Marshal(current)
	x := &invitationCapsTransport{base: w.alice.hub.http.Transport, profile: protocol.Profile{Live: true, Sessions: []string{oldSession, newSession}, Caps: []json.RawMessage{oldRaw, newRaw}}}
	w.alice.hub.http.Transport = x
	x.profile.Sessions = []string{newSession}
	x.profile.Caps = []json.RawMessage{newRaw}
	if e := w.alice.requireParticipationCaps(tctx(t), w.bob.Self(), protocol.CapGroupInvitationControl); e != nil {
		t.Fatal(e)
	}
	x.profile.Sessions = []string{oldSession, newSession}
	x.profile.Caps = []json.RawMessage{oldRaw, newRaw}
	var raw []byte
	var required string
	if e := w.alice.store.db.QueryRow(`SELECT envelope,required_cap FROM outbox WHERE sub=? AND id IN(SELECT id FROM group_invitation_copies WHERE invitation=?)`, envelope.SubGroupInvite, inv.ID).Scan(&raw, &required); e != nil {
		t.Fatal(e)
	}
	if required != protocol.CapGroupInvitationControl {
		t.Fatal(required)
	}
	var env envelope.Envelope
	json.Unmarshal(raw, &env)
	result, e := w.alice.deliver(tctx(t), env, nil)
	if e != nil || result.State != stateConvWaiting || !strings.Contains(result.Detail, "refreshable group invitations") || x.posts != 0 {
		t.Fatalf("old reader handoff: %+v %v posts=%d", result, e, x.posts)
	}
	var after []byte
	if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&after); e != nil || !bytes.Equal(raw, after) {
		t.Fatal("waiting copy changed sealed bytes")
	}
	x.profile.Sessions = []string{newSession}
	x.profile.Caps = []json.RawMessage{newRaw}
	if e = w.alice.store.setOutboxState(env.ID, stateQueued, "", ""); e != nil {
		t.Fatal(e)
	}
	x.afterUpload = func() {
		x.profile.Sessions = []string{oldSession, newSession}
		x.profile.Caps = []json.RawMessage{oldRaw, newRaw}
	}
	result, e = w.alice.deliver(tctx(t), env, nil)
	if e != nil || result.State != stateConvWaiting || x.posts != 0 || x.afterUpload != nil {
		t.Fatalf("downgrade during upload crossed final fence: %+v %v posts=%d", result, e, x.posts)
	}
	if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&after); e != nil || !bytes.Equal(raw, after) {
		t.Fatal("final fence changed sealed bytes")
	}
	if e = w.alice.CancelGroupInvitation(tctx(t), inv.ID); e != nil {
		t.Fatal(e)
	}
	var cancelRaw []byte
	if e = w.alice.store.db.QueryRow(`SELECT envelope,required_cap FROM outbox WHERE sub=? AND id IN(SELECT id FROM group_invitation_copies WHERE invitation=?)`, envelope.SubGroupConsent, inv.ID).Scan(&cancelRaw, &required); e != nil {
		t.Fatal(e)
	}
	if required != protocol.CapGroupInvitationControl {
		t.Fatal("cancel lacks explicit capability")
	}
	var cancelEnv envelope.Envelope
	json.Unmarshal(cancelRaw, &cancelEnv)
	result, e = w.alice.deliver(tctx(t), cancelEnv, nil)
	if e != nil || result.State != stateConvWaiting || x.posts != 0 {
		t.Fatalf("old-reader cancellation sent: %+v %v", result, e)
	}
	x.profile.Sessions = []string{newSession}
	x.profile.Caps = []json.RawMessage{newRaw}
	if e = w.alice.requireParticipationCaps(tctx(t), w.bob.Self(), protocol.CapGroupInvitationControl); e != nil {
		t.Fatal(e)
	}
	legacy := inv.Proposal
	legacy.Nonce = ""
	copy, e := w.alice.groupLifecycleCopy(legacy.Root, envelope.SubGroupInvite, protocol.GroupCarrier{V: 1, Seq: legacy.State.Seq, Hash: legacy.State.Hash()}, legacy, w.bob.Self())
	if e != nil {
		t.Fatal(e)
	}
	defer w.alice.releaseGroupCopies([]outCopy{copy})
	if copy.required != protocol.CapGroup {
		t.Fatal("legacy invitation lost grp1 compatibility")
	}
}

func TestGroupInvitationControlAdvertisementBound(t *testing.T) {
	w := newWorld(t, "")
	caps, agent := w.alice.advertisedCaps()
	if agent {
		t.Fatal("fixture unexpectedly advertises an agent")
	}
	if !slices.IsSorted(caps) || len(caps) > protocol.MaxAdvertisedCaps || !slices.Contains(caps, protocol.CapGroupInvitationControl) {
		t.Fatalf("invalid non-agent caps: %v", caps)
	}
}
