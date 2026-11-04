package client

import (
	"testing"
	"time"
)

// Other tests count exact pages on machines of any speed; the pacing itself is
// tested below and in the separate-process journeys.
func init() {
	historyPageBudget, historyPageStart = time.Hour, historyPage
}

func TestHistoryPageFollowsItsCost(t *testing.T) {
	defer func(d time.Duration) { historyPageBudget = d }(historyPageBudget)
	historyPageBudget = 250 * time.Millisecond
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
