package client

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func operatorPromotionSeed(t *testing.T, host, sender *Agent, state string) string {
	t.Helper()
	id := protocol.NewID()
	_, err := host.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, sender.Address, 1, envelope.KindTask, "original waiting task", 1, state, sender.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func operatorPromotionMark(t *testing.T, a *Agent, id, to string) {
	t.Helper()
	if _, err := a.store.db.Exec(`INSERT OR REPLACE INTO reported(item,recipient,sent_at) VALUES(?,?,1)`, id, to); err != nil {
		t.Fatal(err)
	}
}
func operatorPromotionMarked(t *testing.T, a *Agent, id, to string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item=? AND recipient=?`, id, to).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func operatorPromotionPin(t *testing.T, a, b *Agent) {
	t.Helper()
	if _, err := a.Send(tctx(t), b.Address, "synthetic key pin", ""); err != nil {
		t.Fatal(err)
	}
}

func TestOperatorPromotionCountThenGrantSeparateInstanceAndRestart(t *testing.T) {
	w := newWorld(t, "")
	st := installStub(t, "answer")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runAgent(t, w.alice)
	stop := runAgent(t, w.bob)
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	task, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "original waiting task"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	eventually(t, "same recipient received count only", func() bool { return len(mustNotices(t, w.alice)) == 1 })
	if _, ok := w.alice.NoticeReport(mustNotices(t, w.alice)[0]); ok {
		t.Fatal("count before grant was actionable")
	}
	before, _ := w.bob.store.config(reviewToGenKey)
	command, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := command.GrantOperator(w.alice.Address)
	command.Close()
	if err != nil || fp != w.alice.Self().Fingerprint() {
		t.Fatal(fp, err)
	}
	after, _ := w.bob.store.config(reviewToGenKey)
	if after == before {
		t.Fatal("grant did not change durable notifier generation")
	}
	eventually(t, "same original item becomes actionable", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok && len(r.Items) == 1 && r.Items[0].ID == task.ID && r.Items[0].Actionable {
				return true
			}
		}
		return false
	})
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	stable, _ := w.bob.store.config(reviewToGenKey)
	if stable != after {
		t.Fatal("unchanged grant reissued reports")
	}
	stop()
	runAgent(t, w.bob)
	time.Sleep(300 * time.Millisecond)
	if len(mustNotices(t, w.alice)) != 2 || st.count() != 0 {
		t.Fatal("grant/restart duplicated report or executed task")
	}
	if s, _ := w.bob.store.jobState(task.ID); s != stateAwaiting {
		t.Fatal("operator grant changed job state", s)
	}
	if count(t, w.bob, "approvals") != 0 || count(t, w.bob, "task_grants") != 0 {
		t.Fatal("operator grant created ordinary approvals")
	}
}

func TestOperatorPromotionMarksAndRollback(t *testing.T) {
	w := newWorld(t, "")
	operatorPromotionPin(t, w.bob, w.alice)
	waiting := operatorPromotionSeed(t, w.bob, w.alice, stateAwaiting)
	settled := operatorPromotionSeed(t, w.bob, w.alice, stateAnswered)
	operatorPromotionMark(t, w.bob, waiting, w.alice.Address)
	operatorPromotionMark(t, w.bob, waiting, "other/device")
	operatorPromotionMark(t, w.bob, settled, w.alice.Address)
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if operatorPromotionMarked(t, w.bob, waiting, w.alice.Address) != 0 || operatorPromotionMarked(t, w.bob, waiting, "other/device") != 1 || operatorPromotionMarked(t, w.bob, settled, w.alice.Address) != 1 {
		t.Fatal("promotion touched wrong recipient or settled item")
	}
	gen, _ := w.bob.store.config(reviewToGenKey)
	operatorPromotionMark(t, w.bob, waiting, w.alice.Address)
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	same, _ := w.bob.store.config(reviewToGenKey)
	if same != gen || operatorPromotionMarked(t, w.bob, waiting, w.alice.Address) != 1 {
		t.Fatal("active repeat must not spam")
	}
	if err := w.bob.RevokeOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if operatorPromotionMarked(t, w.bob, waiting, w.alice.Address) != 0 {
		t.Fatal("regrant left count suppression")
	}
	operatorPromotionMark(t, w.bob, waiting, w.alice.Address)
	key, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := key.Public(w.alice.Address)
	public, err := json.Marshal(pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE peers SET public=? WHERE address=?`, string(public), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if fp, err := w.bob.GrantOperator(w.alice.Address); err != nil || fp != pub.Fingerprint() {
		t.Fatal(fp, err)
	}
	if operatorPromotionMarked(t, w.bob, waiting, w.alice.Address) != 0 {
		t.Fatal("new verified key left old suppression")
	}
	if err := w.bob.RevokeOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	operatorPromotionMark(t, w.bob, waiting, w.alice.Address)
	before, _ := w.bob.store.config(reviewToGenKey)
	if _, err := w.bob.store.db.Exec(`CREATE TRIGGER fail_operator_generation BEFORE INSERT ON config WHEN NEW.k='review_to_gen' BEGIN SELECT RAISE(ABORT,'synthetic generation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.GrantOperator(w.alice.Address); err == nil || !strings.Contains(err.Error(), "synthetic generation failure") {
		t.Fatal("expected transaction rollback", err)
	}
	after, _ := w.bob.store.config(reviewToGenKey)
	if before != after || count(t, w.bob, "operators") != 0 || operatorPromotionMarked(t, w.bob, waiting, w.alice.Address) != 1 {
		t.Fatal("failed grant changed generation/marks/grant")
	}
}

type operatorPromotionTransport func(*http.Request) (*http.Response, error)

func (f operatorPromotionTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOperatorPromotionRefusesStalePregrantSnapshot(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	operatorPromotionPin(t, w.bob, w.alice)
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	task := operatorPromotionSeed(t, w.bob, w.alice, stateAwaiting)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	base := w.bob.hub.http.Transport
	first := true
	w.bob.hub.http.Transport = operatorPromotionTransport(func(r *http.Request) (*http.Response, error) {
		if first && r.URL.Path == "/v1/version" {
			first = false
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return base.RoundTrip(r)
	})
	go func() { w.bob.sendReviewNotice(tctx(t)); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not reach barrier")
	}
	command, err := Open(w.bobHome)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	_, err = command.GrantOperator(w.alice.Address)
	command.Close()
	close(release)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if operatorPromotionMarked(t, w.bob, task, w.alice.Address) != 0 {
		t.Fatal("stale count reclaimed promotion marks")
	}
	w.bob.sendReviewNotice(tctx(t))
	eventually(t, "fresh granted snapshot", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok && len(r.Items) == 1 && r.Items[0].ID == task && r.Items[0].Actionable {
				return true
			}
		}
		return false
	})
	if len(mustNotices(t, w.alice)) != 1 {
		t.Fatal("stale snapshot was also sent")
	}
}
