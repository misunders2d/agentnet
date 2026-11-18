package ui

import (
	"fmt"
	"testing"
)

// The kept projections stay at keptChats: the least recently read go first,
// and the open chat stays however long ago it was read.
func TestKeptProjectionsBounded(t *testing.T) {
	l := &Live{msgs: map[string]keptMessages{}, lastOpen: "open"}
	l.msgs["open"] = keptMessages{used: 0}
	for i := 1; i <= keptChats+3; i++ {
		l.msgs[fmt.Sprint(i)] = keptMessages{used: uint64(i)}
	}
	l.evictKept(fmt.Sprint(keptChats + 3))
	if len(l.msgs) != keptChats {
		t.Fatalf("%d kept, want %d", len(l.msgs), keptChats)
	}
	if _, ok := l.msgs["open"]; !ok {
		t.Fatal("the open chat was dropped")
	}
	for i := 1; i <= 4; i++ {
		if _, ok := l.msgs[fmt.Sprint(i)]; ok {
			t.Fatalf("chat %d, among the least recently read, was kept", i)
		}
	}
}
