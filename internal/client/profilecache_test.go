package client

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A device profile is reused only between member lists of an open stream: the
// Hub pushes a list whenever capabilities, sessions or rosters change.
func TestPeerProfileCachedUntilMemberList(t *testing.T) {
	w := newWorld(t, "") // no daemon: lists are injected as the stream delivers them
	a, bob := w.alice, w.bob.Address
	list := []byte(`{"members":[{"address":"` + bob + `","presence":"connected","joined":5}],"truncated":false}`)
	served, fail := 0, false
	var during func()
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/v1/agents/bob/laptop/profile" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		served++
		if during != nil {
			during()
			during = nil
		}
		if fail {
			fail = false
			return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":"injected"}`))}, nil
		}
		// Each served profile is distinguishable, so a stale reuse shows.
		raw, _ := json.Marshal(protocol.Profile{Sessions: []string{strconv.Itoa(served)}, Live: true})
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	read := func(why string, wantGets int, wantServed string) {
		t.Helper()
		before := served
		p, err := a.peerProfile(tctx(t), bob)
		if err != nil {
			t.Fatalf("%s: %v", why, err)
		}
		if got := served - before; got != wantGets || len(p.Sessions) != 1 || p.Sessions[0] != wantServed {
			t.Fatalf("%s: %d reads returning %v, want %d returning [%s]", why, got, p.Sessions, wantGets, wantServed)
		}
	}
	read("without a stream", 1, "1")
	read("again without a stream", 1, "2")

	a.onMembers(list)
	read("first read after a list", 1, "3")
	read("before the next list", 0, "3")
	a.onMembers(list) // e.g. the device published new capabilities
	read("after the next list", 1, "4")
	read("still current", 0, "4")

	// A list arriving while a read is in flight makes that read too old to keep.
	a.onMembers(list)
	during = func() { a.onMembers(list) }
	read("read overtaken by a list", 1, "5")
	read("after the overtaken read", 1, "6")

	a.onMembers(list)
	fail = true
	if _, err := a.peerProfile(tctx(t), bob); err == nil {
		t.Fatal("injected profile failure was not reported")
	}
	read("a failure is not kept", 1, "8")
	read("the next success is kept", 0, "8")

	a.onMembers([]byte(`{"members":`)) // unreadable: something was missed
	read("after an unreadable list", 1, "9")
	read("nothing kept after an unreadable list", 1, "10")

	a.onMembers(list)
	read("stream current again", 1, "11")
	a.membersDisconnected()
	read("after disconnect", 1, "12")
	read("nothing kept while disconnected", 1, "13")

	// An omitted device can change presence without changing the truncated
	// list's bytes, so the Hub may suppress that list's next push.
	a.onMembers([]byte(`{"members":[],"truncated":true}`))
	read("after a truncated list", 1, "14")
	read("nothing kept with incomplete discovery", 1, "15")
}
