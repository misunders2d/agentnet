package client

import (
	"context"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
)

func TestHistoryPageFollowsItsCost(t *testing.T) {
	for _, c := range []struct {
		limit int
		took  time.Duration
		want  int
	}{
		{50, 32 * time.Second, 1},
		{50, 500 * time.Millisecond, 25},
		{4, 10 * time.Millisecond, 8},
		{40, 10 * time.Millisecond, historyPage},
		{historyPage, 10 * time.Millisecond, historyPage},
		{10, 200 * time.Millisecond, 10},
		{1, time.Minute, 1},
	} {
		if got := nextHistoryLimit(c.limit, c.took); got != c.want {
			t.Fatalf("limit %d took %v: got %d, want %d", c.limit, c.took, got, c.want)
		}
	}
}

func TestHistoryRestFollowsTheTurnsCost(t *testing.T) {
	var w convWork
	if rest := w.historyRest(3*time.Second, false); rest != 0 || w.historyNotBefore.Load() != 0 {
		t.Fatalf("without a stream nothing would wake a rest: %v", rest)
	}
	if rest := w.historyRest(time.Millisecond, true); rest != 0 || w.historyNotBefore.Load() != 0 {
		t.Fatalf("a cheap turn rested: %v", rest)
	}
	if rest := w.historyRest(300*time.Millisecond, true); rest != 300*time.Millisecond || w.historyNotBefore.Load() == 0 {
		t.Fatalf("a real turn: %v", rest)
	}
	if rest := w.historyRest(time.Minute, true); rest != historyRestMax {
		t.Fatalf("rest not bounded: %v", rest)
	}
}

// Evidence during the wait stays noted: the restart is delayed, never lost.
func TestDeferredHistoryRestartWaitsForItsCost(t *testing.T) {
	a := &Agent{}
	now := time.Now()
	a.convWork.historyDeferred = map[string]historyDeferredScan{"d": {done: true}}
	a.convWork.historyCost.Store(int64(time.Second))
	if !a.restartDeferredHistory(now) || a.convWork.historyDeferred != nil {
		t.Fatal("without a stream every restart happens at once, as before")
	}
	a.kick = func() {}
	a.convWork.historyDeferred = map[string]historyDeferredScan{"d": {done: true}}
	if !a.restartDeferredHistory(now.Add(11 * time.Second)) {
		t.Fatal("the first restart after the wait did not run")
	}
	a.convWork.historyDeferred = map[string]historyDeferredScan{"d": {done: true}}
	if a.restartDeferredHistory(now.Add(12*time.Second)) || a.convWork.historyDeferred == nil || !a.convWork.sweepPending || a.convWork.bits.Load()&convHistory == 0 {
		t.Fatalf("a restart inside the wait: pending=%v bits=%d", a.convWork.sweepPending, a.convWork.bits.Load())
	}
	if !a.restartDeferredHistory(now.Add(22*time.Second)) || a.convWork.historyDeferred != nil || a.convWork.sweepPending {
		t.Fatal("the noted evidence was lost")
	}
}

func TestHistoryTurnWaitsOutItsRest(t *testing.T) {
	a := &Agent{}
	a.kick = func() {}
	a.convWork.historyNotBefore.Store(time.Now().Add(time.Hour).UnixNano())
	a.historyTurn(t.Context()) // would use the nil store if it ran
	if a.convWork.bits.Load()&convHistory == 0 {
		t.Fatal("the resting turn was dropped")
	}
}

// The worst measured machine sits at the smallest page: everything still
// arrives, exactly once, newest first, with no turn lost.
func TestHistoryCatchupCompletesAtTheSmallestPage(t *testing.T) {
	w, phone, _, ids := historyCatchupFixture(t, 12)
	a := w.alice
	for rounds := 0; ; rounds++ {
		a.convWork.historyLimit.Store(1)
		more, err := a.historyCatchupPage(tctx(t), phone.Self())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if rounds > 3*len(ids) {
			t.Fatalf("no end after %d single-row pages", rounds)
		}
	}
	seen := map[string]int{}
	for _, item := range historyCopiedItems(t, a, phone) {
		seen[item.ID]++
	}
	for _, id := range ids {
		if seen[id] != 1 {
			t.Fatalf("row %s copied %d times", id, seen[id])
		}
	}
}

// The ByHand forms are for a test that drives one sync or history turn itself and counts
// its rows. A turn's rest and its page size follow its cost, which have their own tests
// above, so under load or race a hand-driven turn may otherwise skip or take fewer rows.
func turnsByHand(a *Agent) {
	a.convWork.historyNotBefore.Store(0)
	a.convWork.historyLimit.Store(historyPage)
}

func (a *Agent) pageByHand(ctx context.Context, dev identity.Public) (bool, error) {
	turnsByHand(a)
	return a.historyCatchupPage(ctx, dev)
}

func (a *Agent) stepByHand(ctx context.Context) bool {
	turnsByHand(a)
	return a.historyStep(ctx)
}

func (a *Agent) convSyncByHand(ctx context.Context) {
	turnsByHand(a)
	a.convSync(ctx)
}

func (a *Agent) syncByHand(ctx context.Context) {
	turnsByHand(a)
	a.sync(ctx)
}
