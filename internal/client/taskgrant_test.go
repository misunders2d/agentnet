package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func taskFrom(from, to string) envelope.Inner {
	return envelope.Inner{ID: protocol.NewID(), From: from, To: to, TS: time.Now().Unix(), Kind: envelope.KindTask, Body: "do it"}
}

func setRow(t *testing.T, a *Agent, id, state string) {
	t.Helper()
	if _, err := a.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, state, id); err != nil {
		t.Fatal(err)
	}
}

func grantStatus(t *testing.T, a *Agent, address string) string {
	t.Helper()
	gs, err := a.TaskGrants()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Address == address {
			return g.Status
		}
	}
	return "none"
}

// Once, always and the real receive path: a task waits until accepted; a
// grant lets later tasks from the same key run, but not ones already
// waiting; accept --always accepts and grants in one step.
func TestTaskGrantOnceAndAlways(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	fakeNotify(w.bob)
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	send := func() string {
		r, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "task", Kind: envelope.KindTask})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	t1 := send()
	waitState(t, w.bob, t1, stateAwaiting)

	// accept --always on a question is refused.
	q, _ := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	waitState(t, w.bob, q.ID, stateHeld)
	if _, _, err := w.bob.AcceptAlways(q.ID); err == nil {
		t.Fatal("--always accepted a question")
	}

	sender, fp, err := w.bob.AcceptAlways(t1)
	if err != nil || sender != w.alice.Address || fp != w.alice.Self().Fingerprint() {
		t.Fatalf("accept --always: %s %s %v", sender, fp, err)
	}
	waitState(t, w.bob, t1, stateAnswered)
	t2 := send()
	waitState(t, w.bob, t2, stateAnswered)
	if st.count() != 2 {
		t.Fatalf("runs = %d", st.count())
	}
	// Questions are not affected by a task grant.
	if s, _ := w.bob.store.jobState(q.ID); s != stateHeld {
		t.Fatalf("question after task grant: %s", s)
	}

	// Revoke: later tasks wait again; a new grant does not run what waits.
	if _, err := w.bob.RevokeTasks(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	t3 := send()
	waitState(t, w.bob, t3, stateAwaiting)
	if fp, err := w.bob.GrantTasks(w.alice.Address); err != nil || fp != w.alice.Self().Fingerprint() {
		t.Fatalf("grant: %s %v", fp, err)
	}
	time.Sleep(300 * time.Millisecond)
	if s, _ := w.bob.store.jobState(t3); s != stateAwaiting || st.count() != 2 {
		t.Fatalf("grant ran a waiting task: %s, runs %d", s, st.count())
	}
	t4 := send()
	waitState(t, w.bob, t4, stateAnswered)
}

// A row whose verifying key is unknown (stored before key tracking) or not
// the pinned one never gains authority, and accept --always on it grants
// nothing.
func TestAcceptAlwaysNeedsTaskProvenance(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.store.pin(w.alice.Self()); err != nil {
		t.Fatal(err)
	}
	old := taskFrom(w.alice.Address, w.bob.Address)
	if err := w.bob.store.addInbox(old, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.bob.AcceptAlways(old.ID); !errors.Is(err, ErrTaskKeyDiffer) {
		t.Fatalf("unknown provenance: %v", err)
	}
	if s := grantStatus(t, w.bob, w.alice.Address); s != "none" {
		t.Fatalf("grant left behind: %s", s)
	}
	if s, _ := w.bob.store.jobState(old.ID); s != stateAwaiting {
		t.Fatalf("task state: %s", s)
	}
	other, _ := identity.Generate()
	stranger := taskFrom(w.alice.Address, w.bob.Address)
	w.bob.store.addInbox(stranger, other.Public(w.alice.Address).Fingerprint())
	if _, _, err := w.bob.AcceptAlways(stranger.ID); !errors.Is(err, ErrTaskKeyDiffer) {
		t.Fatalf("other key: %v", err)
	}
	if _, err := w.bob.GrantTasks("nobody/none"); !errors.Is(err, ErrNoPinnedKey) {
		t.Fatalf("grant without a pinned key: %v", err)
	}
	// With the grant in place, only the pinned key's tasks run.
	fp, _ := w.bob.GrantTasks(w.alice.Address)
	a, b := taskFrom(w.alice.Address, w.bob.Address), taskFrom(w.alice.Address, w.bob.Address)
	w.bob.store.addInbox(a, fp)
	w.bob.store.addInbox(b, other.Public(w.alice.Address).Fingerprint())
	if s, _ := w.bob.store.jobState(a.ID); s != statePending {
		t.Fatalf("granted key: %s", s)
	}
	if s, _ := w.bob.store.jobState(b.ID); s != stateAwaiting {
		t.Fatalf("other key: %s", s)
	}
	// Duplicate delivery stores it once.
	w.bob.store.addInbox(a, fp)
	var n int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id = ?`, a.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("%d rows", n)
	}
}

// The worker's claim rechecks the grant, the verifying key and the
// current pin, so a change that lands after the task was stored stops it
// even without the usual demotion.
func TestTaskGrantClaimRechecks(t *testing.T) {
	w := newWorld(t, "")
	w.bob.store.pin(w.alice.Self())
	fp, err := w.bob.GrantTasks(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := identity.Generate()
	newKey := other.Public(w.alice.Address)
	for _, change := range []struct {
		name       string
		do, undo   string
		args, uarg []any
	}{
		{"grant deleted", `DELETE FROM task_grants`, `INSERT INTO task_grants SELECT address, ?, public, 0 FROM peers`, nil, []any{fp}},
		{"key change pending", `UPDATE peers SET pending = 'x'`, `UPDATE peers SET pending = NULL`, nil, nil},
		{"pin replaced", `UPDATE peers SET public = ?`, `UPDATE peers SET public = (SELECT public FROM task_grants)`, []any{mustJSON(newKey)}, nil},
	} {
		task := taskFrom(w.alice.Address, w.bob.Address)
		if err := w.bob.store.addInbox(task, fp); err != nil {
			t.Fatal(err)
		}
		if s, _ := w.bob.store.jobState(task.ID); s != statePending {
			t.Fatalf("%s: stored as %s", change.name, s)
		}
		if _, err := w.bob.store.db.Exec(change.do, change.args...); err != nil {
			t.Fatal(err)
		}
		if j, ok, err := w.bob.store.claimJob("stub"); err != nil || ok {
			t.Fatalf("%s: claimed %s (%v)", change.name, j.ID, err)
		}
		if _, err := w.bob.store.db.Exec(change.undo, change.uarg...); err != nil {
			t.Fatal(err)
		}
		if j, ok, _ := w.bob.store.claimJob("stub"); !ok || j.ID != task.ID {
			t.Fatalf("%s: not claimable once restored", change.name)
		}
		setRow(t, w.bob, task.ID, stateAnswered)
	}
}

// Revocation and key changes move not-yet-started tasks back into view,
// leave accepted and running ones alone, and a trusted new key never
// inherits the grant. Regranting never resurrects what was moved back.
func TestTaskGrantRevokeAndKeyChange(t *testing.T) {
	w := newWorld(t, "")
	w.bob.store.pin(w.alice.Self())
	fp, _ := w.bob.GrantTasks(w.alice.Address)
	add := func(state string) string {
		task := taskFrom(w.alice.Address, w.bob.Address)
		w.bob.store.addInbox(task, fp)
		if state != "" {
			setRow(t, w.bob, task.ID, state)
		}
		return task.ID
	}
	queued, once, running, failed := add(""), add(stateAccepted), add(stateRunning), add(stateJobFailed)
	got, err := w.bob.RevokeTasks(w.alice.Address)
	if err != nil || len(got) != 1 || got[0] != running {
		t.Fatalf("revoke: %v %v", got, err)
	}
	for id, want := range map[string]string{queued: stateAwaiting, once: stateAccepted, running: stateRunning, failed: stateJobFailed} {
		if s, _ := w.bob.store.jobState(id); s != want {
			t.Fatalf("%s: %s, want %s", id, s, want)
		}
	}
	var detail string
	w.bob.store.db.QueryRow(`SELECT detail FROM inbox WHERE id = ?`, queued).Scan(&detail)
	if !strings.Contains(detail, "revoked") {
		t.Fatalf("detail: %q", detail)
	}
	// The revoked task waits for review; the one still running stays visible
	// there too (P4: running work is never hidden), nothing else does.
	items, _ := w.bob.Review()
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
	}
	if !seen[queued] || len(items) > 2 || len(items) == 2 && !seen[running] {
		t.Fatalf("revoked task not in review: %v", items)
	}

	// Regrant: what was moved back stays waiting; failed is not rerun.
	fp, _ = w.bob.GrantTasks(w.alice.Address)
	for _, id := range []string{queued, failed} {
		if s, _ := w.bob.store.jobState(id); s == statePending || s == stateAccepted {
			t.Fatalf("regrant resurrected %s: %s", id, s)
		}
	}

	// A key change becomes pending: queued work waits, the grant is inactive.
	waiting := add("")
	other, _ := identity.Generate()
	newKey := other.Public(w.alice.Address)
	if err := w.bob.store.setPending(newKey); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.bob.store.jobState(waiting); s != stateAwaiting {
		t.Fatalf("pending key change left %s", s)
	}
	if s := grantStatus(t, w.bob, w.alice.Address); !strings.HasPrefix(s, "inactive: key change pending") {
		t.Fatalf("status: %s", s)
	}
	if _, err := w.bob.GrantTasks(w.alice.Address); !errors.Is(err, ErrKeyPending) {
		t.Fatalf("grant during a pending change: %v", err)
	}
	// Trusting the new key does not move the grant to it.
	if err := w.bob.store.pin(newKey); err != nil {
		t.Fatal(err)
	}
	if s := grantStatus(t, w.bob, w.alice.Address); !strings.HasPrefix(s, "inactive: key changed") {
		t.Fatalf("status after trust: %s", s)
	}
	fresh := taskFrom(w.alice.Address, w.bob.Address)
	w.bob.store.addInbox(fresh, newKey.Fingerprint())
	if s, _ := w.bob.store.jobState(fresh.ID); s != stateAwaiting {
		t.Fatalf("new key inherited the grant: %s", s)
	}
	// A task verified with the old key but stored after the trust waits.
	late := taskFrom(w.alice.Address, w.bob.Address)
	w.bob.store.addInbox(late, fp)
	if s, _ := w.bob.store.jobState(late.ID); s != stateAwaiting {
		t.Fatalf("old-key task after trust: %s", s)
	}
}

// Pending granted tasks survive a restart and are rechecked when claimed.
func TestTaskGrantAcrossRestart(t *testing.T) {
	w := newWorld(t, "")
	w.bob.store.pin(w.alice.Self())
	fp, _ := w.bob.GrantTasks(w.alice.Address)
	task := taskFrom(w.alice.Address, w.bob.Address)
	w.bob.store.addInbox(task, fp)
	w.bob.Close()
	bob, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	if j, ok, err := bob.store.claimJob("stub"); err != nil || !ok || j.ID != task.ID {
		t.Fatalf("after restart: %v %v", ok, err)
	}
}

func mustJSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// Trusting a different key directly (no pending step first) also moves
// queued granted tasks back into view.
func TestTaskGrantPinChangeDemotes(t *testing.T) {
	w := newWorld(t, "")
	w.bob.store.pin(w.alice.Self())
	fp, _ := w.bob.GrantTasks(w.alice.Address)
	task := taskFrom(w.alice.Address, w.bob.Address)
	w.bob.store.addInbox(task, fp)
	w.bob.store.pin(w.alice.Self()) // same key: nothing changes
	if s, _ := w.bob.store.jobState(task.ID); s != statePending {
		t.Fatalf("same key re-pinned: %s", s)
	}
	other, _ := identity.Generate()
	w.bob.store.pin(other.Public(w.alice.Address))
	if s, _ := w.bob.store.jobState(task.ID); s != stateAwaiting {
		t.Fatalf("new key pinned: %s", s)
	}
}

// Revoking wakes a running daemon, so tasks moved back to awaiting are
// announced at once rather than at the next Hub ping.
func TestRevokeTasksWakesDaemon(t *testing.T) {
	w := newWorld(t, "")
	notes := fakeNotify(w.bob)
	w.bob.store.pin(w.alice.Self())
	fp, _ := w.bob.GrantTasks(w.alice.Address)
	runWith(t, w, w.bob, RunOptions{}) // no responder: pending tasks stay put
	task := taskFrom(w.alice.Address, w.bob.Address)
	if err := w.bob.store.addInbox(task, fp); err != nil { // no wake-up
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	before := notes.count()
	if _, err := w.bob.RevokeTasks(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "review notification after revoke", func() bool { return notes.count() > before })
}
