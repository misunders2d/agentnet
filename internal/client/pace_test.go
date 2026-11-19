package client

import (
	"context"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A background pass runs at once after a quiet moment; wakes within
// passEvery of the last pass run once, at its end; a stop ends the wait.
func TestPaceRunsAtOnceThenOncePerWindow(t *testing.T) {
	var last time.Time
	at := time.Now()
	if !pace(context.Background(), &last, nil) || time.Since(at) > passEvery/2 {
		t.Fatal("the first pass waited")
	}
	at = time.Now()
	if !pace(context.Background(), &last, nil) || time.Since(at) < passEvery*9/10 {
		t.Fatalf("a second pass ran %s after the first", time.Since(at))
	}
	time.Sleep(passEvery)
	at = time.Now()
	if !pace(context.Background(), &last, nil) || time.Since(at) > passEvery/2 {
		t.Fatal("a pass after a quiet moment waited")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pace(ctx, &last, nil) {
		t.Fatal("a stopped loop went on")
	}
}

// A wake someone waits on (wakeWorkerNow: a job stored or finished, a
// received request, another process's command) ends the pace window at once;
// plain churn (wakeWorker) still waits for its end.
func TestPaceUrgentWakeRunsAtOnce(t *testing.T) {
	a := &Agent{workerUrgent: make(chan struct{}, 1), wakeWorker: func() {}}
	var last time.Time
	if !pace(context.Background(), &last, a.workerUrgent) {
		t.Fatal("the first pass did not run")
	}
	a.wakeWorker()
	select {
	case <-a.workerUrgent:
		t.Fatal("a plain wake was marked urgent")
	default:
	}
	a.wakeWorkerNow()
	at := time.Now()
	if !pace(context.Background(), &last, a.workerUrgent) || time.Since(at) > passEvery/2 {
		t.Fatalf("an urgent wake inside the window waited %s", time.Since(at))
	}
	go func() { time.Sleep(passEvery / 5); a.wakeWorkerNow() }()
	at = time.Now()
	if !pace(context.Background(), &last, a.workerUrgent) || time.Since(at) > passEvery*7/10 {
		t.Fatalf("an urgent wake during the wait did not end it: %s", time.Since(at))
	}
}

// A received request, answer or result is waited on (its run or its
// follow-up); a plain message, a burst's bulk, is not.
func TestWaitedOnKinds(t *testing.T) {
	for kind, want := range map[string]bool{
		envelope.KindQuestion: true, envelope.KindTask: true, envelope.KindAnswer: true, envelope.KindResult: true,
		envelope.KindMessage: false, "": false,
	} {
		if waitedOnKind(kind) != want {
			t.Errorf("waitedOnKind(%q) = %v", kind, !want)
		}
	}
}
