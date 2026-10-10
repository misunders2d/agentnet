package client

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// versionRT counts the Hub requests that do not say this program's version.
type versionRT struct {
	rt            http.RoundTripper
	seen, missing *atomic.Int64
}

func (v versionRT) RoundTrip(r *http.Request) (*http.Response, error) {
	v.seen.Add(1)
	if r.Header.Get(protocol.VersionHeader) != protocol.Version {
		v.missing.Add(1)
	}
	return v.rt.RoundTrip(r)
}

// Every Hub request says which program version makes it: the joins, the
// push streams and everything a daemon and a send do. The Hub shows each
// device's version in its member list.
func TestHubRequestsSayVersion(t *testing.T) {
	var seen, missing atomic.Int64
	wrapTransport = func(rt http.RoundTripper) http.RoundTripper { return versionRT{rt, &seen, &missing} }
	t.Cleanup(func() { wrapTransport = nil })
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	if _, err := w.alice.Send(tctx(t), w.bob.Address, "which version?", ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice's version in bob's member list", func() bool {
		for _, m := range w.bob.MemberView().Members.Members {
			if m.Address == w.alice.Address {
				return m.Version == protocol.Version
			}
		}
		return false
	})
	if seen.Load() == 0 || missing.Load() != 0 {
		t.Fatalf("%d of %d Hub requests did not say the version", missing.Load(), seen.Load())
	}
}
