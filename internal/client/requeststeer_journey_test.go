package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Join the durable delivery gates to the actual owned harness adapter: one
// strict correction is consumed by one existing run, ordinary messages behind
// it arrive, and neither restart nor retry starts another native process.
// The child is a protocol fixture, never a provider or an open user session.
func TestGroupStrictSteeringDeliveryConsumptionAndRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable wrapper fixture")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	processLog := filepath.Join(t.TempDir(), "processes")
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho started >> \"$AGENTNET_STEERING_JOURNEY_LOG\"\nexec \"$AGENTNET_CODEX_FIXTURE_EXE\" -test.run '^TestCodexRoomFixtureProcess$'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("AGENTNET_STEERING_JOURNEY_LOG", processLog)
	t.Setenv("AGENTNET_CODEX_FIXTURE_EXE", exe)
	t.Setenv("AGENTNET_CODEX_ROOM_FIXTURE", "steer")
	w, carol, packet, stops := groupTurnsFixture(t)
	a := w.alice
	p := p6Member(t, a, a, packet.State.Conv)
	stops[a]()
	a.stopBackgroundPosts()
	original, err := a.AskAgent(WithQueuedSend(tctx(t), protocol.NewID()), p.PID, envelope.KindQuestion, "Prepare the comparison")
	if err != nil {
		t.Fatal(err)
	}
	// Establish the fixture's exact owned attempt before enqueueing its
	// correction. runJob below uses the real subprocess/adapter/result path.
	if _, err = a.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,last_attempt_at=unixepoch() WHERE id=?`, stateRunning, original.LID); err != nil {
		t.Fatal(err)
	}
	parent := job{ID: original.LID, From: a.Address, Key: a.Self().Fingerprint(), Kind: envelope.KindQuestion, Body: "Prepare the comparison", Conv: packet.State.Conv, PID: p.PID, Local: true, nativeAttempt: 1}
	var target string
	if err = a.store.db.QueryRow(`SELECT target FROM inbox WHERE id=?`, parent.ID).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(target), &parent.Target); err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	correction := RequestFollowup{Conv: packet.State.Conv, Ref: envelope.Ref{ID: original.LID, Fingerprint: parent.Key}, Attempt: 1, ID: id, Body: "Use English instead"}
	if _, err = a.QueueRequestFollowup(tctx(t), correction); err != nil {
		t.Fatal(err)
	}
	ids := []string{original.LID, id}
	for _, body := range []string{"The later human message", "One more ordinary message"} {
		sent, err := a.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), packet.State.Conv, ConvOutgoing{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sent.LID)
	}
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND lid IN (?,?,?,?) AND state IN ('queued','waiting','receiver_waiting')`, packet.State.Conv, ids[0], ids[1], ids[2], ids[3]).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("normal flush retained steering/following messages: queued=%d err=%v", queued, err)
	}
	for _, reader := range []*Agent{w.bob, carol} {
		eventually(t, "ordinary turns behind strict intent reach each reader", func() bool {
			var n int
			err := reader.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND lid IN (?,?,?,?)`, packet.State.Conv, ids[0], ids[1], ids[2], ids[3]).Scan(&n)
			return err == nil && n == len(ids)
		})
	}
	a.runJob(tctx(t), parent, &Responder{Harness: "codex", Dir: t.TempDir(), Timeout: 15 * time.Second}, nil)
	waitState(t, a, id, stateAnswered)
	waitState(t, a, parent.ID, stateAnswered)
	var linked bool
	if err = a.store.db.QueryRow(`SELECT c.result_id=p.result_id AND c.result_id IS NOT NULL AND c.steering_parent=p.id AND c.steering_attempt=p.attempts FROM inbox c JOIN inbox p ON p.id=? WHERE c.id=?`, parent.ID, id).Scan(&linked); err != nil || !linked {
		t.Fatal("accepted correction lost exact parent/result linkage", linked, err)
	}
	var results, attempts int
	if err = a.store.db.QueryRow(`SELECT count(DISTINCT lid) FROM outbox WHERE conv=? AND reply_to=? AND kind=? AND body=?`, packet.State.Conv, parent.ID, envelope.KindAnswer, taggedAnswer(t, a, "OWNED_CORRECTION_ACCEPTED")).Scan(&results); err != nil || results != 1 {
		t.Fatalf("exact corrected result: count=%d err=%v", results, err)
	}
	if err = a.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, parent.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("original run repeated: attempts=%d err=%v", attempts, err)
	}
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if err = a.store.interruptRunning(); err != nil {
		t.Fatal(err)
	}
	if _, err = a.QueueRequestFollowup(tctx(t), correction); err != nil {
		t.Fatal("exact retry after result and restart", err)
	}
	for range 2 {
		if err = a.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	if state := jobState(t, a, id); state != stateAnswered {
		t.Fatalf("settled correction changed after restart: %s", state)
	}
	if _, found, err := a.claimNativeFollowup(parent); err != nil || found {
		t.Fatalf("finished native correction replayed: %t %v", found, err)
	}
	for _, reader := range []*Agent{w.bob, carol} {
		eventually(t, "one signed corrected answer reaches each reader", func() bool {
			var n int
			err := reader.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND reply_to=? AND kind=? AND body=? AND verified_by=?`, packet.State.Conv, parent.ID, envelope.KindAnswer, taggedAnswer(t, a, "OWNED_CORRECTION_ACCEPTED"), a.Self().Fingerprint()).Scan(&n)
			return err == nil && n == 1
		})
	}
	data, err := os.ReadFile(processLog)
	if err != nil || strings.Count(string(data), "started\n") != 1 {
		t.Fatalf("one original owns one native process: %q %v", data, err)
	}
}
