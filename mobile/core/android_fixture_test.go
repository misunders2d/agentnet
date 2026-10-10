package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
)

// TestExportAndroidOfflineFixture exports only synthetic test-owned storage,
// explicitly requested by an absolute path beneath the OS temporary directory.
// It never contacts a relay or reads a production home. SQLite rows model
// already-admitted cached messages; this is a presentation fixture, not proof
// of network enrollment, signature admission, or physical phone qualification.
func TestExportAndroidOfflineFixture(t *testing.T) {
	target := os.Getenv("AGENTNET_ANDROID_FIXTURE_DIR")
	if target == "" {
		t.Skip("set AGENTNET_ANDROID_FIXTURE_DIR to export a synthetic emulator fixture")
	}
	clean := filepath.Clean(target)
	rel, err := filepath.Rel(os.TempDir(), clean)
	if err != nil || !filepath.IsAbs(clean) || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatal("fixture export must be an absolute child of the OS temporary directory")
	}
	if entries, err := os.ReadDir(clean); err == nil && len(entries) != 0 {
		t.Fatal("fixture export directory must be empty")
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s, home := fixture(t)
	own, err := identity.Load(filepath.Join(home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	const peerAddress = "fixture-peer/laptop"
	public := peer.Public(peerAddress)
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO peers(address,public,pinned_at) VALUES(?,?,?)", peerAddress, string(raw), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	const rootID = "00000000000000000000000000000001"
	const count = 200
	base := time.Now().Add(-count * time.Minute).Unix()
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("%032x", i+1)
		reply := ""
		if i > 0 {
			reply = rootID
		}
		at := base + int64(i)*60
		if i%2 == 0 {
			body := fmt.Sprintf("Saved fixture message %03d received. Available offline.", i+1)
			_, err = tx.Exec("INSERT INTO inbox(id,sender,ts,kind,body,reply_to,received_at,verified_by) VALUES(?,?,?,?,?,nullif(?,''),?,?)", id, peerAddress, at, envelope.KindMessage, body, reply, at, public.Fingerprint())
		} else {
			body := fmt.Sprintf("Saved fixture message %03d sent. Delivery is recorded separately.", i+1)
			env, e := envelope.Seal(envelope.Inner{V: 1, ID: id, From: s.a.Address, To: peerAddress, TS: at, Kind: envelope.KindMessage, Body: body, ReplyTo: reply}, own.Sign, peer.Box.Recipient())
			if e != nil {
				t.Fatal(e)
			}
			encoded, e := json.Marshal(env)
			if e != nil {
				t.Fatal(e)
			}
			_, err = tx.Exec("INSERT INTO outbox(id,recipient,body,envelope,state,created_at,reply_to,recipient_fp) VALUES(?,?,?,?,?,?,?,?)", id, peerAddress, body, string(encoded), "delivered", at, reply, public.Fingerprint())
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	overview, err := s.OverviewJSON()
	if err != nil {
		t.Fatal(err)
	}
	var summary struct {
		Threads []struct {
			ID    string `json:"id"`
			Count int    `json:"count"`
		} `json:"threads"`
	}
	if err = json.Unmarshal([]byte(overview), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.Threads) != 1 || summary.Threads[0].Count != count {
		t.Fatalf("fixture overview does not contain one complete200-message thread: %s", overview)
	}
	threadID := summary.Threads[0].ID
	thread, err := s.ThreadJSON(threadID)
	if err != nil {
		t.Fatal(err)
	}
	var timeline struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err = json.Unmarshal([]byte(thread), &timeline); err != nil {
		t.Fatal(err)
	}
	if len(timeline.Messages) != count {
		t.Fatalf("fixture timeline has%d rows, want%d", len(timeline.Messages), count)
	}
	s.Close()
	if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(clean, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"identity.json", "agent.db"} {
		data, err := os.ReadFile(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(clean, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _ := json.MarshalIndent(struct {
		Synthetic bool   `json:"synthetic"`
		ThreadID  string `json:"thread_id"`
		Messages  int    `json:"messages"`
		Peer      string `json:"peer"`
	}{true, threadID, count, peerAddress}, "", "  ")
	if err = os.WriteFile(filepath.Join(clean, "fixture.json"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("exported synthetic offline fixture:200 messages, thread%s", threadID)
}
