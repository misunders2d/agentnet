package client

import (
	"context"
	"testing"
	"time"
)

// A background pass runs at once after a quiet moment; wakes within
// passEvery of the last pass run once, at its end; a stop ends the wait.
func TestPaceRunsAtOnceThenOncePerWindow(t *testing.T) {
	var last time.Time
	at := time.Now()
	if !pace(context.Background(), &last) || time.Since(at) > passEvery/2 {
		t.Fatal("the first pass waited")
	}
	at = time.Now()
	if !pace(context.Background(), &last) || time.Since(at) < passEvery*9/10 {
		t.Fatalf("a second pass ran %s after the first", time.Since(at))
	}
	time.Sleep(passEvery)
	at = time.Now()
	if !pace(context.Background(), &last) || time.Since(at) > passEvery/2 {
		t.Fatal("a pass after a quiet moment waited")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pace(ctx, &last) {
		t.Fatal("a stopped loop went on")
	}
}
