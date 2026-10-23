package client

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A stopped session can still advertise signed capabilities. Wait for the
// newly started daemon, rather than accepting that last offline profile.
func waitFreshNamedAgentSession(t *testing.T, host *Agent, previous protocol.Profile) string {
	t.Helper()
	label, device, _ := protocol.SplitAddress(host.Address)
	var session string
	eventually(t, "new live signed session for "+host.Address, func() bool {
		var profile protocol.Profile
		if host.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile) != nil ||
			!profile.Live || !profile.Supports(host.Address, host.Self().SignKey, protocol.CapAgentIdentity) {
			return false
		}
		for _, id := range profile.Sessions {
			if !slices.Contains(previous.Sessions, id) {
				session = id
				return true
			}
		}
		return false
	})
	t.Logf("new live signed session observed; previous_sessions=%d", len(previous.Sessions))
	return session
}

func TestAgentCatalogOldRelayPublicationFallback(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	if _, err := w.bob.CreateLocalAgent("Pi", Responder{Harness: "stub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	base := w.bob.hub.http.Transport
	puts := 0
	w.bob.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/version" {
			response, err := base.RoundTrip(r)
			if err != nil {
				return nil, err
			}
			raw, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil {
				return nil, err
			}
			var version protocol.VersionInfo
			if err = json.Unmarshal(raw, &version); err != nil {
				return nil, err
			}
			features := version.Features[:0]
			for _, f := range version.Features {
				if f != protocol.FeatureAgentHarness {
					features = append(features, f)
				}
			}
			version.Features = features
			raw, _ = json.Marshal(version)
			response.Body = io.NopCloser(bytes.NewReader(raw))
			response.ContentLength = int64(len(raw))
			return response, nil
		}
		if r.Method == "PUT" && r.URL.Path == "/v1/agent-catalog" {
			puts++
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				return nil, err
			}
			var old []struct {
				V       int    `json:"v"`
				ID      string `json:"id"`
				Host    string `json:"host"`
				HostKey string `json:"host_key"`
				Label   string `json:"label"`
				TS      int64  `json:"ts"`
				Sig     []byte `json:"sig"`
			}
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if err = d.Decode(&old); err != nil {
				t.Errorf("old strict relay rejected publication: %v", err)
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			r.ContentLength = int64(len(raw))
		}
		return base.RoundTrip(r)
	})
	defer func() { w.bob.hub.http.Transport = base }()
	if err := w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if puts != 1 {
		t.Fatal("catalog not published", puts)
	}
}

func TestLocalAgentIdentityMappingsAndImmutableSelection(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	defaultConfig := &Responder{Harness: "pi", Dir: stub.dir, Timeout: time.Minute}
	if err := w.bob.SetResponder(defaultConfig); err != nil {
		t.Fatal(err)
	}
	before, err := w.bob.Responder()
	if err != nil {
		t.Fatal(err)
	}
	a, err := w.bob.CreateLocalAgent("Builder", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("Builder", Responder{Harness: "stub2", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID || a.Label != b.Label || a.Verify(w.bob.Self()) != nil || b.Verify(w.bob.Self()) != nil {
		t.Fatal("stable identity collapsed or unsigned")
	}
	after, _ := w.bob.Responder()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("agent setup changed device default")
	}
	tx, err := w.bob.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stamp, err := w.bob.ResolveExecutorIn(tx, a.ID, after)
	tx.Rollback()
	if err != nil || stamp.AgentID != a.ID || stamp.Responder.Harness != "stub" {
		t.Fatal("first executor", err)
	}
	beforeConflict, _ := w.bob.store.config(agentCatalogConfig)
	if err := w.bob.SetLocalAgentResponder(a.ID, &Responder{Harness: "stub2", Dir: stub.dir}); !errors.Is(err, ErrAgentHarnessConflict) {
		t.Fatal("mapping change did not report the other enabled harness", err)
	}
	if afterConflict, _ := w.bob.store.config(agentCatalogConfig); afterConflict != beforeConflict {
		t.Fatal("refused mapping change rewrote the catalog")
	}
	if err := w.bob.SetLocalAgentResponder(b.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SetLocalAgentResponder(a.ID, &Responder{Harness: "stub2", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	if stamp.Responder.Harness != "stub" || stamp.Record.Harness != "stub" || stamp.Record.ID != a.ID {
		t.Fatal("running selection changed with catalog")
	}
	next, err := w.bob.ResolveExecutorIn(w.bob.store.db, a.ID, after)
	if err != nil || next.Responder.Harness != "stub2" || next.Record.Harness != "stub2" || next.Record.Verify(w.bob.Self()) != nil {
		t.Fatal("next selection not updated", err)
	}
	if err := w.bob.SetLocalAgentResponder(a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SetLocalAgentResponder(b.ID, &Responder{Harness: "stub2", Dir: stub.dir}); err != nil {
		t.Fatal("freed harness identity could not be re-enabled", err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, a.ID, after); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("removed identity fell back", err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, protocol.NewID(), after); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("unknown identity fell back", err)
	}
	foreign, err := w.alice.CreateLocalAgent("Builder", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.bob.ResolveExecutorIn(w.bob.store.db, foreign.ID, after); !errors.Is(err, ErrUnknownAgent) {
		t.Fatal("foreign identity mapped locally", err)
	}
	public, err := w.bob.PublicAgentCatalog()
	if err != nil || len(public) != 2 || public[1].ID != b.ID {
		t.Fatal("removed record public", err)
	}
	entries, err := w.bob.LocalAgents()
	if err != nil || len(entries) != 3 || entries[1].Record.ID != a.ID {
		t.Fatal("historical identity lost", err)
	}
	legacy, err := w.bob.ResolveExecutorIn(w.bob.store.db, "", after)
	if err != nil || legacy.AgentID != "" || !reflect.DeepEqual(legacy.Responder, *after) {
		t.Fatal("legacy changed", err)
	}
	if err = w.bob.SetResponder(nil); err != nil {
		t.Fatal(err)
	}
	manual, err := w.bob.ResolveExecutorIn(w.bob.store.db, "", nil)
	if err != nil || manual != nil {
		t.Fatal("manual default changed", err)
	}
	selected, err := w.bob.ResolveExecutorIn(w.bob.store.db, b.ID, nil)
	if err != nil || selected == nil {
		t.Fatal("named selection depended on default", err)
	}
	raw, _ := w.bob.store.config(agentCatalogConfig)
	if strings.Contains(raw, "session_id") || strings.Contains(raw, "native_session") {
		t.Fatal("catalog contains native session mapping")
	}
}

// A full catalog read before an edit must not arrive after the explicit
// post-edit publication and replace it. Cover separate Open handles as used
// by a daemon and CLI, as well as two callers of the same Agent.
func TestAgentCatalogPublicationKeepsLatestAcrossHandles(t *testing.T) {
	for _, separate := range []bool{false, true} {
		name := "same-handle"
		if separate {
			name = "separate-open-handles"
		}
		t.Run(name, func(t *testing.T) {
			stub := installStub(t, "answer")
			w, conv, _, stopBob := agentWorld(t)
			stopBob()
			w.bob.stopBackgroundPosts()
			newer := w.bob
			if separate {
				var err error
				newer, err = Open(w.bob.home)
				if err != nil {
					t.Fatal(err)
				}
				defer newer.Close()
			}
			old, err := w.bob.CreateLocalAgent("Retired builder", Responder{Harness: "stub", Dir: stub.dir})
			if err != nil {
				t.Fatal(err)
			}
			entered, releaseOld := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseOld) }) }
			defer release()
			base := w.bob.hub.http.Transport
			var first atomic.Bool
			w.bob.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == "PUT" && r.URL.Path == "/v1/agent-catalog" && first.CompareAndSwap(false, true) {
					close(entered) // old snapshot is signed and serialized, before the relay stores it
					select {
					case <-releaseOld:
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
				}
				return base.RoundTrip(r)
			})
			defer func() { w.bob.hub.http.Transport = base }()
			oldDone := make(chan error, 1)
			go func() { oldDone <- w.bob.PublishAgentCatalog(tctx(t)) }()
			select {
			case <-entered:
			case <-tctx(t).Done():
				t.Fatal("old catalog snapshot did not reach the relay barrier")
			}
			if err := newer.SetLocalAgentResponder(old.ID, nil); err != nil {
				t.Fatal(err)
			}
			current, err := newer.CreateLocalAgent("Current builder", Responder{Harness: "stub", Dir: stub.dir})
			if err != nil {
				t.Fatal(err)
			}
			newDone := make(chan error, 1)
			// The existing OS-lock abstraction lets the fixture distinguish a
			// serialized publication from an unfenced one without sleeps. In
			// the unfenced RED case, force the newer PUT to finish first. When
			// fenced, release the older holder so the waiting publisher can
			// build its snapshot after that holder finishes.
			probe, fenceErr := lockfile.Acquire(filepath.Join(w.bob.home, "agent-catalog.lock"))
			if fenceErr == nil {
				probe()
				newDone <- newer.PublishAgentCatalog(tctx(t))
				release()
			} else if errors.Is(fenceErr, lockfile.ErrLocked) {
				go func() { newDone <- newer.PublishAgentCatalog(tctx(t)) }()
				release()
			} else {
				t.Fatal(fenceErr)
			}
			if err := <-oldDone; err != nil {
				t.Fatal(err)
			}
			if err := <-newDone; err != nil {
				t.Fatal(err)
			}
			label, device, _ := protocol.SplitAddress(w.bob.Address)
			var actual []protocol.AgentRecord
			if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/agent-catalog", nil, &actual); err != nil {
				t.Fatal(err)
			}
			if len(actual) != 1 || actual[0].ID != current.ID || actual[0].Verify(w.bob.Self()) != nil {
				t.Fatalf("stale completed snapshot replaced current catalog: got %+v; want only %s (disabled %s absent)", actual, current.ID, old.ID)
			}
			// Restore only the host's capability session after proving the
			// relay already has the correct catalog. Invitation is attempted
			// once against the actual signed reader, never retried or guessed.
			var previous protocol.Profile
			if err := w.bob.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &previous); err != nil {
				t.Fatal(err)
			}
			stopRestarted := runAgent(t, w.bob)
			defer stopRestarted()
			waitFreshNamedAgentSession(t, w.bob, previous)
			// A fresh CLI handle reads the restarted session directly, rather
			// than racing the old reader's next availability-cache push.
			reader, err := Open(w.alice.home)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			catalog, err := reader.AgentCatalog(tctx(t), w.bob.Address)
			if err != nil || len(catalog) != 1 || catalog[0].ID != current.ID {
				t.Fatalf("current verified catalog %+v %v", catalog, err)
			}
			invited, err := reader.InviteNamedAgent(tctx(t), conv, w.bob.Address, current.ID, nil, nil, "one exact current identity")
			if err != nil || invited.AgentID != current.ID {
				t.Fatalf("single exact named invitation: %+v %v", invited, err)
			}
			t.Log("new enabled identity retained, disabled identity absent; one signed-catalog invitation succeeded")
		})
	}
}
