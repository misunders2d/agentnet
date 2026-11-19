package client

import (
	"testing"
	"time"
)

// A kept chat's request rows are settled again on each read: a host that is
// not connected (or a run past its age) is old news, as decorateConv says.
func TestResettledSettlesKeptRowsAgain(t *testing.T) {
	a := &Agent{}
	kept := []ConvMessage{{ID: "q", Exec: &ExecView{State: stateRunning, Host: "gone/host", At: time.Now().Unix()}}, {ID: "m"}}
	got := a.Resettled(kept)
	if !got[0].Exec.Stale || kept[0].Exec.Stale {
		t.Fatalf("settled %v, kept changed %v", got[0].Exec.Stale, kept[0].Exec.Stale)
	}
	kept[0].Exec = &ExecView{State: "unavailable", Stale: true}
	if !a.Resettled(kept)[0].Exec.Stale {
		t.Fatal("a removed host's request lost its stored old-news mark")
	}
}
