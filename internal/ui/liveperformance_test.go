package ui

import (
	"database/sql"
	"encoding/json"
	"fmt"
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

// Exercise the actual native HTTP provider with a representative retained
// history. The old rows are synthetic already-stored metadata, never sent;
// the new send and topic events use real signing, outbox and local relay paths.
// Timings are evidence, not machine-dependent pass/fail thresholds.
func TestLiveBusyConversationJourney(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx := t.Context()
	hubHome, aliceHome := t.TempDir(), t.TempDir()
	testhub.Start(t, hubHome, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, aliceHome, testhub.BootstrapCode(t, hubHome), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, t.TempDir(), code, "phone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	for _, a := range []*client.Agent{alice, bob} {
		if _, err = a.CreatePerson(ctx, a.Address); err != nil {
			t.Fatal(err)
		}
	}
	conv := liveTwoMemberGroup(t, alice, bob)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(aliceHome, "agent.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().Unix() - 1000
	for turn := 0; turn < 300; turn++ {
		lid := fmt.Sprintf("%032x", turn+1)
		for copy := 0; copy < 12; copy++ {
			id := fmt.Sprintf("%032x", 10000+turn*12+copy)
			var sub, ref, fp any
			body := "Synthetic retained message " + lid
			if turn >= 100 {
				sub, ref, fp = "status", fmt.Sprintf("%032x", turn+5000), alice.Self().Fingerprint()
				body = fmt.Sprintf(`{"state":"resolved","n":1,"at":%d}`, now+int64(turn))
			}
			_, err = tx.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,sub,ref_id,ref_fp,created_ms) VALUES(?,?,?,?,'delivered',?,?,?,'message',?,?,?,?)`, id, bob.Address, body, fmt.Sprintf(`{"ts":%d}`, now+int64(turn)), now+int64(turn), conv, lid, sub, ref, fp, (now+int64(turn))*1000)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	live := NewLive(alice)
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	read := func(label string) DMThread {
		t.Helper()
		start := time.Now()
		r := do(t, ts, "GET", "/api/dm?id="+conv, "", authed(ts, nil))
		defer r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s HTTP %d", label, r.StatusCode)
		}
		var view DMThread
		if err = json.NewDecoder(r.Body).Decode(&view); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s HTTP through decoded view: %s (%d visible rows)", label, time.Since(start), len(view.Messages))
		return view
	}
	before := read("initial history")
	if len(before.Messages) != 100 {
		t.Fatalf("logical history rows=%d", len(before.Messages))
	}
	start := time.Now()
	refs, err := alice.SelectGroupHistory(ctx, conv, client.GroupHistorySelection{Last: 64})
	if err != nil || len(refs) != 64 {
		t.Fatalf("history refs=%d err=%v", len(refs), err)
	}
	t.Logf("recent history selection: %s (64 refs)", time.Since(start))
	id := protocol.NewID()
	body, _ := json.Marshal(DMDraft{ID: id, Conv: conv, Topic: "new", Body: "Actual new topic message"})
	start = time.Now()
	r := do(t, ts, "POST", "/api/dm/send", string(body), post(ts))
	var sent Sent
	if r.StatusCode != 200 {
		t.Fatalf("send HTTP %d", r.StatusCode)
	}
	if err = json.NewDecoder(r.Body).Decode(&sent); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	t.Logf("send HTTP/local enqueue acknowledgement: %s state=%s", time.Since(start), sent.State)
	if sent.LID != id {
		t.Fatal("optimistic send lost its exact logical ID")
	}
	view := read("after send")
	var topic string
	for _, m := range view.Messages {
		if m.LID == id {
			topic = m.Topic
		}
	}
	if topic == "" {
		t.Fatal("new message topic missing")
	}
	for _, action := range []string{"done", "reopen"} {
		body, _ = json.Marshal(TopicChange{Conv: conv, ID: topic, Count: 1})
		start = time.Now()
		r = do(t, ts, "POST", "/api/topic/"+action, string(body), post(ts))
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s HTTP %d", action, r.StatusCode)
		}
		t.Logf("%s HTTP/local enqueue acknowledgement: %s", action, time.Since(start))
		view = read("after " + action)
	}
	if len(view.Topics) != 1 || view.Topics[0].State != TopicActive {
		t.Fatal("Reopen did not produce an active topic")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		msgs, e := bob.ConversationMessages(conv)
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, m := range msgs {
			found = found || m.LID == id
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new signed message never reached recipient storage")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Log("recipient separately confirmed the exact new message; enqueue timing was not a delivery claim")
}
