package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestControlPreflightUsesOneCurrentSignedProfile(t *testing.T) {
	w := newWorld(t, "") // no daemon or background delivery mutates this fixture
	features := []string{protocol.FeatureEnv3, protocol.FeatureCaps}
	profile := func(caps []string, wrongKey bool) protocol.Profile {
		t.Helper()
		caps = slices.Clone(caps)
		slices.Sort(caps)
		record := protocol.CapsRecord{Address: w.bob.Address, Session: protocol.NewID(), Caps: caps, TS: time.Now().Unix()}
		if wrongKey {
			record.Sign(w.alice.id.Sign)
		} else {
			record.Sign(w.bob.id.Sign)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		return protocol.Profile{Sessions: []string{record.Session}, Caps: []json.RawMessage{raw}}
	}
	current := profile([]string{protocol.CapControl, protocol.CapGroup}, false)
	var transportError error
	requests := 0
	base := w.alice.hub.http.Transport
	w.alice.hub.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Path != "/v1/agents/bob/laptop/profile" {
			t.Fatalf("unexpected preflight request: %s %s", req.Method, req.URL.Path)
		}
		requests++
		if transportError != nil {
			return nil, transportError
		}
		raw, _ := json.Marshal(current)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	defer func() { w.alice.hub.http.Transport = base }()
	check := func(primary string, want bool, why string) {
		t.Helper()
		before := requests
		ok, got := w.alice.capSupport(tctx(t), w.bob.Address, w.bob.Self(), features, primary, protocol.CapGroup)
		if requests != before+1 || ok != want || why != "" && got != why {
			t.Fatalf("profile requests=%d ok=%v why=%q; want one request, %v, %q", requests-before, ok, got, want, why)
		}
	}
	check(protocol.CapControl, true, "")
	// A new decision fetches again; an earlier successful snapshot grants no
	// reuse when the next signed profile no longer permits the group reader.
	current = profile([]string{protocol.CapControl}, false)
	groupWhy := w.bob.Address + ": " + (&NeedsUpdateError{Address: w.bob.Address, Cap: protocol.CapGroup}).Error()
	check(protocol.CapControl, false, groupWhy)
	current = profile([]string{protocol.CapGroup}, false)
	controlWhy := WaitPeerUpdate + w.bob.Address + " cannot read reactions, edits or deletions yet. Update AgentNet on that computer and reconnect all its open sessions."
	check(protocol.CapControl, false, controlWhy)
	current = profile([]string{protocol.CapControl, protocol.CapGroup}, true)
	check(protocol.CapControl, false, controlWhy)
	current = profile([]string{protocol.CapControl, protocol.CapGroup}, false)
	current.Sessions = append(current.Sessions, protocol.NewID())
	check(protocol.CapControl, false, controlWhy) // every listed session must verify
	current = profile([]string{protocol.CapHeadless, protocol.CapGroup}, false)
	check(protocol.CapHeadless, true, "")
	transportError = errors.New("profile transport unavailable")
	_, beforeWhy := w.alice.capSupport(tctx(t), w.bob.Address, w.bob.Self(), features, protocol.CapControl)
	if !strings.HasPrefix(beforeWhy, WaitServerUnavailable) {
		t.Fatalf("transport failure lost its original classification: %q", beforeWhy)
	}
	check(protocol.CapControl, false, beforeWhy)
}

// A queued copy must not combine capabilities from mutually incompatible
// signed session snapshots, or retain yesterday's snapshot for a new decision.
func TestQueuedReaderRequiresConsistentCurrentProfile(t *testing.T) {
	w := newWorld(t, "") // no daemon or background sender uses this transport
	groupOnly := socialReaderProfile(t, w.bob, []string{protocol.CapGroup}, nil)
	contextOnly := socialReaderProfile(t, w.bob, []string{protocol.CapTopicContext}, nil)
	full := socialReaderProfile(t, w.bob, []string{protocol.CapGroup, protocol.CapTopicContext}, nil)
	current := []protocol.Profile{groupOnly, contextOnly}
	var fetchError error
	reads := 0
	base := w.alice.hub.http.Transport
	w.alice.hub.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Path != "/v1/agents/bob/laptop/profile" {
			t.Fatalf("unexpected reader request: %s %s", req.Method, req.URL.Path)
		}
		if fetchError != nil {
			return nil, fetchError
		}
		profile := current[reads%len(current)]
		reads++
		raw, err := json.Marshal(profile)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})
	defer func() { w.alice.hub.http.Transport = base }()
	needs := copyNeeds{required: protocol.CapGroup, sub: envelope.SubGroupContext, snapshotCap: true}
	missing := func(n copyNeeds, cap string) {
		t.Helper()
		err := w.alice.readerCheck(tctx(t), w.bob.Address, w.bob.Self(), n)
		var update *NeedsUpdateError
		if !errors.As(err, &update) || update.Cap != cap {
			t.Fatalf("incompatible current reader: got %v, want missing %s", err, cap)
		}
	}
	// The old implementation accepted Group from one session and TopicContext
	// from another, although no returned snapshot could read the whole copy.
	missing(needs, protocol.CapTopicContext)
	current = []protocol.Profile{full}
	if err := w.alice.readerCheck(tctx(t), w.bob.Address, w.bob.Self(), needs); err != nil {
		t.Fatalf("compatible current reader: %v", err)
	}
	current = []protocol.Profile{groupOnly}
	missing(needs, protocol.CapTopicContext)
	current = []protocol.Profile{contextOnly}
	missing(needs, protocol.CapGroup)
	current = []protocol.Profile{full}
	current[0].Sessions = append(current[0].Sessions, protocol.NewID())
	missing(needs, protocol.CapGroup)
	current = []protocol.Profile{socialReaderProfile(t, w.bob, []string{protocol.CapExternalParticipation}, nil)}
	missing(copyNeeds{required: protocol.CapExternalParticipation}, protocol.CapExternalParticipation)
	fetchError = errors.New("unavailable profile fixture")
	if err := w.alice.readerCheck(tctx(t), w.bob.Address, w.bob.Self(), copyNeeds{required: "unknown-fixture-capability"}); err == nil || errors.Is(err, fetchError) {
		t.Fatalf("unknown requirement reached the profile instead of failing locally: %v", err)
	}
	if err := w.alice.readerCheck(tctx(t), w.bob.Address, w.bob.Self(), needs); !errors.Is(err, fetchError) {
		t.Fatalf("profile failure changed classification: %v", err)
	}
	fetchError = nil
	current = []protocol.Profile{full}
	if err := w.alice.readerCheck(tctx(t), w.bob.Address, w.bob.Self(), needs); err != nil {
		t.Fatalf("fresh decision did not recover after profile failure: %v", err)
	}
}
