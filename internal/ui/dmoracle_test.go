package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/misunders2d/agentnet/internal/client"
)

// dmOracle, set by every test of this package (overviewcache_test.go), hears
// of a chat read from its kept messages that differs from a fresh read, as
// overviewOracle does for the list; an empty string says one equalled it.
var dmOracle func(string)

func init() { dmChecked = checkDM }

// checkDM compares a DM read with a fresh one and proves the page's skip rule.
func checkDM(l *Live, id string, page DMPage, t DMThread, mark int64, rev client.ViewChange) {
	if dmOracle == nil {
		return
	}
	if page.Limit == 0 && l.a.ViewKeysScoped() {
		l.checkSkip(id, t, mark, rev.Unscoped)
	}
	fresh, e := l.dmPage(id, page, true)
	now, _ := l.a.ViewChanges()
	if after, _ := l.a.ViewMark(); now.Content == rev.Content && after == mark { // nothing changed under the comparison, announced or not
		x, _ := json.Marshal(t)
		y, _ := json.Marshal(fresh)
		if e != nil || !bytes.Equal(x, y) {
			dmOracle(fmt.Sprintf("chat %s differs from a fresh read (%v):\nkept  %s\nfresh %s", id, e, x, y))
		} else {
			dmOracle("")
		}
	}
}

// checkSkip (dmOracle only) proves the page's skip rule (store.ts
// scopedRefetch): when every key moved since this chat's last view is "h" or
// another chat's, and nothing unscoped happened, the view is as it was. Seq
// and exec staleness move without a write and are left out.
func (l *Live) checkSkip(id string, t DMThread, mark int64, unscoped uint64) {
	t.Seq = 0
	t.Messages = slices.Clone(t.Messages)
	for i := range t.Messages {
		if e := t.Messages[i].Exec; e != nil {
			c := *e
			c.Stale = false
			t.Messages[i].Exec = &c
		}
	}
	now, _ := json.Marshal(t)
	seenMu.Lock()
	prev, ok := seen[seenChat{l, id}]
	seen[seenChat{l, id}] = seenView{mark, unscoped, now}
	seenMu.Unlock()
	if !ok || prev.unscoped != unscoped {
		return
	}
	keys, _, err := l.a.ViewDirtySince(prev.mark)
	if err != nil || len(keys) == 0 {
		return
	}
	for _, k := range keys {
		if k = strings.TrimPrefix(k, "v"); k != "h" && (!strings.HasPrefix(k, "c:") || slices.Contains(t.Scope, k)) { // as ViewScope says it
			return // the page reads it again
		}
	}
	if !bytes.Equal(prev.view, now) {
		dmOracle(fmt.Sprintf("chat %s changed under keys %v that leave it as it was:\nbefore %s\nnow    %s", id, keys, prev.view, now))
		return
	}
	skipsChecked.Add(1)
}

var skipsChecked atomic.Int64 // checkSkip's proven skips, said by the tests

// seenView is checkSkip's last view of a chat.
type seenView struct {
	mark     int64
	unscoped uint64
	view     []byte
}

// seen is checkSkip's last view of each chat of each Live.
var (
	seenMu sync.Mutex
	seen   = map[seenChat]seenView{}
)

type seenChat struct {
	l  *Live
	id string
}
