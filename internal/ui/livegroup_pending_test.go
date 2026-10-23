package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// The real rename/promotion is published before the recipient ingests its
// original signed proof. Withhold only AcceptGroupCommit: no daemon, sleeps,
// fabricated signatures or corrupt rows can race the pending-context barrier.
func TestLiveGroupMutationPendingHTTP(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	hub, aliceHome := t.TempDir(), t.TempDir()
	testhub.Start(t, hub, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, aliceHome, testhub.BootstrapCode(t, hub), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	for _, a := range []*client.Agent{alice, bob} {
		if _, err = a.CreatePerson(ctx, a.Address); err != nil {
			t.Fatal(err)
		}
	}
	conv := liveTwoMemberGroup(t, alice, bob)
	initial, err := alice.GroupContext(conv)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(aliceHome, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Ordinary members receive the original signed proof through encrypted
	// fanout; only admins may read the Hub's group journal directly.
	var firstPage protocol.GroupJournalPage
	for seq := int64(0); seq <= initial.State.Seq; seq++ {
		var raw []byte
		var commit protocol.GroupCommit
		if err = db.QueryRow("SELECT record FROM group_proof_records WHERE conv=? AND seq=?", conv, seq).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &commit); err != nil {
			t.Fatal(err)
		}
		firstPage.Records = append(firstPage.Records, commit)
	}
	if err = bob.IngestGroupProofPage(ctx, initial.Root, firstPage); err != nil {
		t.Fatal(err)
	}
	if err = bob.AcceptGroupCommit(ctx, firstPage.Records[len(firstPage.Records)-1]); err != nil {
		t.Fatal(err)
	}
	// The failing later projection resolves a real signed participation. A
	// room with no invited agent instead returns a fully frozen cached view.
	// No daemon, harness configuration or request exists, so it cannot execute.
	if _, err = bob.InviteAgent(ctx, conv, bob.Address, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	live := NewLive(bob)
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	read := func(t *testing.T) (*http.Response, []byte) {
		t.Helper()
		resp := do(t, ts, "GET", "/api/dm?id="+conv, "", authed(ts, nil))
		body, e := io.ReadAll(resp.Body)
		resp.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		return resp, body
	}
	if resp, body := read(t); resp.StatusCode != http.StatusOK {
		t.Fatalf("verified baseline: %d %s", resp.StatusCode, body)
	}
	person, _, err := bob.Person()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"rename", "promote"} {
		t.Run(action, func(t *testing.T) {
			var next client.GroupContext
			var e error
			if action == "rename" {
				next, e = alice.RenameGroup(ctx, conv, "Renamed while verification catches up")
			} else {
				next, e = alice.PromoteGroupMember(ctx, conv, person.Person)
			}
			if e != nil {
				t.Fatal(e)
			}
			var raw []byte
			if e = db.QueryRow("SELECT record FROM group_proof_records WHERE conv=? AND seq=?", conv, next.State.Seq).Scan(&raw); e != nil {
				t.Fatal(e)
			}
			var commit protocol.GroupCommit
			if e = json.Unmarshal(raw, &commit); e != nil {
				t.Fatal(e)
			}
			if e = bob.IngestGroupProofPage(ctx, next.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{commit}}); e != nil {
				t.Fatal(e)
			}
			if _, e = bob.GroupMembers(conv); !errors.Is(e, client.ErrGroupContextPending) {
				t.Fatalf("proof barrier did not pause membership: %v", e)
			}
			views, e := bob.Conversations()
			if e != nil {
				t.Fatal(e)
			}
			for _, view := range views {
				if view.ID == conv && (view.Frozen != "" || view.Role == "former_member") {
					t.Fatalf("pending head fence invented a closed conversation: frozen=%q role=%q", view.Frozen, view.Role)
				}
			}
			if _, e = bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "Must not send with pending authority"}); !errors.Is(e, client.ErrGroupContextPending) {
				t.Fatalf("proof barrier did not refuse send: %v", e)
			}
			resp, body := read(t)
			var pending map[string]json.RawMessage
			if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Content-Type") != "application/json" || json.Unmarshal(body, &pending) != nil || string(pending["code"]) != `"group_context_pending"` || len(pending) != 2 || len(pending["message"]) == 0 {
				t.Fatalf("pending contract: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
			}
			if e = bob.AcceptGroupCommit(ctx, commit); e != nil {
				t.Fatal(e)
			}
			resp, body = read(t)
			var thread DMThread
			if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &thread) != nil || thread.ID != conv || thread.Title != next.State.Title || len(thread.Members) != 2 || thread.Messages == nil {
				t.Fatalf("verified recovery: %d %s", resp.StatusCode, body)
			}
			if action == "promote" {
				for _, m := range thread.Members {
					if m.Person == person.Person && !m.Admin {
						t.Fatal("recovered member did not reflect verified promotion")
					}
				}
			}
			t.Logf("%s: original signed proof held at seq=%d; membership/send refused; HTTP pending then complete verified thread", action, commit.Seq)
		})
		if t.Failed() {
			break
		}
	}
}

func TestGroupContextPendingErrorMapping(t *testing.T) {
	for _, err := range []error{client.ErrGroupContextPending, fmt.Errorf("private verification detail: %w", client.ErrGroupContextPending)} {
		w := httptest.NewRecorder()
		writeErr(w, err)
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "private verification detail") || strings.Contains(w.Body.String(), "internal error") {
			t.Fatalf("expected safe pending response: %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct {
		err  error
		code int
	}{{NotFound("missing"), http.StatusNotFound}, {Refuse("not allowed"), http.StatusConflict}, {errors.New("unexpected"), http.StatusInternalServerError}} {
		w := httptest.NewRecorder()
		writeErr(w, tc.err)
		if w.Code != tc.code {
			t.Fatalf("unrelated mapping: %d want %d", w.Code, tc.code)
		}
	}
}
