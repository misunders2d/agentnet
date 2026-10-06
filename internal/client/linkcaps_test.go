package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A device already made a member while its link waits (the relay answers
// its stream with the members header) advertises the same capabilities as
// its daemon will: the relay takes a device to support only what all its
// live sessions do, so a smaller list here kept senders holding controls,
// Drive records and statuses until that session's grace ran out.
func TestAwaitLinkMemberSessionAdvertisesOwnCaps(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	realm, err := a.RealmID()
	if err != nil {
		t.Fatal(err)
	}
	var put protocol.CapsRecord
	a.hub.http.Transport = workspaceRealmTransport(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		switch {
		case r.URL.Path == "/v1/version":
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(`{"protocol":1,"realm_id":"` + realm + `"}`)), Request: r}, nil
		case r.URL.Path == "/v1/stream":
			h.Set(protocol.MembersHeader, "1")
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		case r.Method == "PUT" && r.URL.Path == "/v1/caps":
			if err := json.NewDecoder(r.Body).Decode(&put); err != nil {
				t.Error(err)
			}
			return &http.Response{StatusCode: 204, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		return &http.Response{StatusCode: 404, Header: h, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	session := protocol.NewID()
	linked, err := a.pendingStream(tctx(t), "", session)
	if err != nil || !linked {
		t.Fatalf("member stream: linked=%v %v", linked, err)
	}
	if caps, _ := a.advertisedCaps(); put.Session != session || strings.Join(put.Caps, " ") != strings.Join(caps, " ") {
		t.Fatalf("waiting session advertised %v, daemon advertises %v", put.Caps, caps)
	}
	if err := put.Verify(a.id.Public(a.Address).SignKey); err != nil {
		t.Fatal("capability record not signed by this device")
	}
}
