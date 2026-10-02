package hub

import (
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"golang.org/x/time/rate"
)

var signalRateEpoch = time.Unix(1900000000, 0)

func signalRateEvent(from, to string, serial int, now time.Time) protocol.Signal {
	return protocol.Signal{From: from, To: to, ID: fmt.Sprintf("%032x", serial), TS: now.UnixMilli()}
}

func pushSignalRate(t *testing.T, s *signalRuntime, from, to string, serial int, now time.Time, want bool) {
	t.Helper()
	if got := s.push(signalRateEvent(from, to, serial, now), now); got != want {
		t.Fatalf("push %s -> %s serial=%d at %s: got %v want %v", from, to, serial, now.Sub(signalRateEpoch), got, want)
	}
}

func TestSignalRateInitialBursts(t *testing.T) {
	t.Run("pair", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 8; i++ {
			pushSignalRate(t, &s, "sender/x", "target/x", i, signalRateEpoch, true)
		}
		pushSignalRate(t, &s, "sender/x", "target/x", 8, signalRateEpoch, false)
		if len(s.replay) != 8 || len(s.rates) != 2 {
			t.Fatal("rejected request mutated maps")
		}
	})
	t.Run("sender", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 128; i++ {
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("target%d/x", i/8), i, signalRateEpoch, true)
		}
		pushSignalRate(t, &s, "sender/x", "fresh/x", 128, signalRateEpoch, false)
		if len(s.replay) != 128 || len(s.rates) != 17 {
			t.Fatal("sender rejection inserted fresh pair")
		}
	})
}

func TestSignalRateContinuousRefillAndSustainedLoad(t *testing.T) {
	t.Run("pair", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 8; i++ {
			v := signalRateEvent("sender/x", "target/x", i, signalRateEpoch)
			if !s.push(v, signalRateEpoch) {
				t.Fatal("initial burst rejected")
			}
		}
		pushSignalRate(t, &s, "sender/x", "target/x", 9, signalRateEpoch.Add(125*time.Millisecond-time.Microsecond), false)
		for i := 1; i <= 80; i++ {
			now := signalRateEpoch.Add(time.Duration(i) * 125 * time.Millisecond)
			pushSignalRate(t, &s, "sender/x", "target/x", 100+i, now, true)
			pushSignalRate(t, &s, "sender/x", "target/x", 200+i, now, false)
		}

	})
	t.Run("sender", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 128; i++ {
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("target%d/x", i/8), i, signalRateEpoch, true)
		}
		period := time.Second / 128
		pushSignalRate(t, &s, "sender/x", "fresh/x", 128, signalRateEpoch.Add(period-time.Microsecond), false)
		for i := 1; i <= 32; i++ {
			now := signalRateEpoch.Add(time.Duration(i) * period)
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("fresh%d/x", i/8), 200+i, now, true)
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("fresh%d/x", i/8), 300+i, now, false)
		}
	})
}

func TestSignalRateBudgetRollback(t *testing.T) {
	t.Run("exhausted pair preserves sender", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 8; i++ {
			pushSignalRate(t, &s, "sender/x", "hot/x", i, signalRateEpoch, true)
		}
		for i := 0; i < 1024; i++ {
			pushSignalRate(t, &s, "sender/x", "hot/x", 1000+i, signalRateEpoch, false)
		}
		for i := 0; i < 120; i++ {
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("other%d/x", i/8), 3000+i, signalRateEpoch, true)
		}
		pushSignalRate(t, &s, "sender/x", "fresh/x", 4000, signalRateEpoch, false)
	})
	t.Run("exhausted sender preserves new pair and other sender", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 128; i++ {
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("target%d/x", i/8), i, signalRateEpoch, true)
		}
		for i := 0; i < 1024; i++ {
			pushSignalRate(t, &s, "sender/x", "fresh/x", 1000+i, signalRateEpoch, false)
		}
		if _, ok := s.rates["sender/x\x00fresh/x"]; ok {
			t.Fatal("failed sender created pair")
		}
		for i := 0; i < 8; i++ {
			pushSignalRate(t, &s, "other/x", "fresh/x", 3000+i, signalRateEpoch, true)
		}
		pushSignalRate(t, &s, "other/x", "fresh/x", 4000, signalRateEpoch, false)
	})
	t.Run("replayed request preserves both budgets", func(t *testing.T) {
		var s signalRuntime
		pushSignalRate(t, &s, "sender/x", "target/x", 1, signalRateEpoch, true)
		for i := 0; i < 1024; i++ {
			pushSignalRate(t, &s, "sender/x", "target/x", 1, signalRateEpoch, false)
		}
		for i := 0; i < 7; i++ {
			pushSignalRate(t, &s, "sender/x", "target/x", 10+i, signalRateEpoch, true)
		}
		pushSignalRate(t, &s, "sender/x", "target/x", 20, signalRateEpoch, false)
		for i := 0; i < 120; i++ {
			pushSignalRate(t, &s, "sender/x", fmt.Sprintf("other%d/x", i/8), 100+i, signalRateEpoch, true)
		}
		pushSignalRate(t, &s, "sender/x", "fresh/x", 1000, signalRateEpoch, false)
	})
	t.Run("future reservation rollback retains partial refill", func(t *testing.T) {
		var s signalRuntime
		for i := 0; i < 8; i++ {
			pushSignalRate(t, &s, "sender/x", "target/x", i, signalRateEpoch, true)
		}
		now := signalRateEpoch.Add(62 * time.Millisecond)
		before := s.rates["sender/x\x00target/x"].limiter.TokensAt(now)
		for i := 0; i < 1024; i++ {
			pushSignalRate(t, &s, "sender/x", "target/x", 100+i, now, false)
		}
		after := s.rates["sender/x\x00target/x"].limiter.TokensAt(now)
		if math.Abs(before-after) > 1e-10 {
			t.Fatalf("rollback changed partial balance: %g -> %g", before, after)
		}
		pushSignalRate(t, &s, "sender/x", "target/x", 2000, signalRateEpoch.Add(125*time.Millisecond), true)
	})
}

func TestSignalRateReplaySaturationNoEarlyEviction(t *testing.T) {
	var s signalRuntime
	pushSignalRate(t, &s, "sender/x", "target/x", 1, signalRateEpoch, true)
	for i := 0; i < 8191; i++ {
		s.replay[fmt.Sprintf("seed%d", i)] = signalRateEpoch.Add(protocol.SignalTTL)
	}
	beforeSender := s.rates["sender/x"].limiter.TokensAt(signalRateEpoch)
	beforePair := s.rates["sender/x\x00target/x"].limiter.TokensAt(signalRateEpoch)
	for i := 0; i < 16; i++ {
		pushSignalRate(t, &s, "sender/x", "target/x", 100+i, signalRateEpoch, false)
	}
	pushSignalRate(t, &s, "new/x", "newtarget/x", 1000, signalRateEpoch, false)
	if s.rates["sender/x"].limiter.TokensAt(signalRateEpoch) != beforeSender || s.rates["sender/x\x00target/x"].limiter.TokensAt(signalRateEpoch) != beforePair || len(s.rates) != 2 {
		t.Fatal("replay saturation drained or inserted budgets")
	}
	pushSignalRate(t, &s, "new/x", "newtarget/x", 1001, signalRateEpoch.Add(time.Second), false)
	if len(s.replay) != 8192 {
		t.Fatal("unexpired replay entries evicted")
	}
	if _, ok := s.replay["sender/x\x00"+fmt.Sprintf("%032x", 1)]; !ok {
		t.Fatal("original replay proof evicted")
	}
	pushSignalRate(t, &s, "new/x", "newtarget/x", 1002, signalRateEpoch.Add(protocol.SignalTTL), true)
	if len(s.replay) != 1 {
		t.Fatal("expired replay evidence did not recover capacity")
	}
}

func seedSignalRates(n int) *signalRuntime {
	s := &signalRuntime{replay: protocol.ReplayWindow{}, rates: map[string]signalRate{}}
	for i := 0; i < n; i++ {
		s.rates[fmt.Sprintf("seed%d", i)] = signalRate{at: signalRateEpoch, limiter: rate.NewLimiter(128, 128)}
	}
	return s
}

func TestSignalRateMapCapacityAndRecovery(t *testing.T) {
	t.Run("exact bound and existing keys continue", func(t *testing.T) {
		s := seedSignalRates(8190)
		pushSignalRate(t, s, "sender/x", "target/x", 1, signalRateEpoch, true)
		if len(s.rates) != 8192 {
			t.Fatal("did not reach exact map capacity")
		}
		pushSignalRate(t, s, "sender/x", "fresh/x", 2, signalRateEpoch, false)
		pushSignalRate(t, s, "freshsender/x", "fresh/x", 3, signalRateEpoch, false)
		if len(s.rates) != 8192 {
			t.Fatal("map exceeded bound")
		}
		for i := 0; i < 7; i++ {
			pushSignalRate(t, s, "sender/x", "target/x", 10+i, signalRateEpoch, true)
		}
		pushSignalRate(t, s, "sender/x", "target/x", 20, signalRateEpoch, false)
		pushSignalRate(t, s, "freshsender/x", "fresh/x", 21, signalRateEpoch.Add(time.Second), true)
		if len(s.rates) != 2 {
			t.Fatal("fully refilled idle keys did not reclaim capacity")
		}
	})
	t.Run("8191 plus two refuses", func(t *testing.T) {
		s := seedSignalRates(8191)
		pushSignalRate(t, s, "freshsender/x", "fresh/x", 1, signalRateEpoch, false)
		if len(s.rates) != 8191 || len(s.replay) != 0 {
			t.Fatal("failed reservation mutated memory")
		}
	})
}

// An initial event at t=0 refills before t=1s. Crossing that old window
// boundary grants no fresh burst: eight accepted just before, zero just after.
// The prior fixed window admitted seven before and eight after this boundary.
func TestSignalRateBoundaryHasNoWindowReset(t *testing.T) {
	var s signalRuntime
	for i := 0; i < 17; i++ {
		now := signalRateEpoch
		if i > 0 && i < 9 {
			now = now.Add(time.Second - time.Nanosecond)
		}
		if i >= 9 {
			now = now.Add(time.Second)
		}
		v := signalRateEvent("sender/x", "target/x", i, now)
		wantNew := i < 9
		if got := s.push(v, now); got != wantNew {
			t.Fatalf("bucket boundary event %d: %v want %v", i, got, wantNew)
		}
	}
}

func TestSignalRateQueuesOfflineDropAndDisconnect(t *testing.T) {
	var s signalRuntime
	sub1, sub2 := &subscriber{}, &subscriber{}
	ch1, ch2 := s.subscribe("target/x", sub1), s.subscribe("target/x", sub2)
	for i := 0; i < 40; i++ {
		pushSignalRate(t, &s, fmt.Sprintf("sender%d/x", i), "target/x", i, signalRateEpoch, true)
	}
	if len(ch1) != 32 || len(ch2) != 32 {
		t.Fatal("queue bounds changed")
	}
	// A full queue still consumes one admitted signal, once per sender-target key.
	for i := 0; i < 8; i++ {
		pushSignalRate(t, &s, "full/x", "target/x", 100+i, signalRateEpoch, true)
	}
	pushSignalRate(t, &s, "full/x", "target/x", 108, signalRateEpoch, false)
	if s.rates["full/x"].limiter.TokensAt(signalRateEpoch) != 120 {
		t.Fatal("fanout charged more than once")
	}
	s.unsubscribe("target/x", sub1)
	s.unsubscribe("target/x", sub2)
	if _, ok := s.queues["target/x"]; ok {
		t.Fatal("disconnected queues retained in runtime")
	}
	for i := 0; i < 8; i++ {
		pushSignalRate(t, &s, "offline/x", "target/x", 200+i, signalRateEpoch, true)
	}
	pushSignalRate(t, &s, "offline/x", "target/x", 208, signalRateEpoch, false)
	fresh := s.subscribe("target/x", sub1)
	if len(fresh) != 0 || fresh == ch1 {
		t.Fatal("reconnect inherited discarded queue")
	}
	s.unsubscribe("target/x", sub1)
}

func TestSignalRateConcurrentBudgetAtomicity(t *testing.T) {
	var s signalRuntime
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 256; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.push(signalRateEvent("sender/x", "target/x", i, signalRateEpoch), signalRateEpoch) {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 8 || len(s.replay) != 8 || s.rates["sender/x"].limiter.TokensAt(signalRateEpoch) != 120 {
		t.Fatalf("non-atomic budgets: accepted=%d replay=%d", accepted.Load(), len(s.replay))
	}
}
