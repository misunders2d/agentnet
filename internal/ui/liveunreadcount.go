package ui

import (
	"context"
	"strings"
	"sync"
)

// unreadCount is the app badge's running unread total (MEL-731), kept the way
// Telegram keeps its chat lists' counters (TDLib MessagesManager,
// tdesktop Dialogs::MainList): each chat's number is recomputed only when a
// write touched that chat (client unread_dirty), the total moves by that
// chat's change, and watchers hear of it only when the total moved. Each
// chat's number comes from the same canonical projection as the overview's
// alert total; a complete overview build at the same revision cross-checks it.
type unreadCount struct {
	mu       sync.Mutex
	watchers int
	stop     context.CancelFunc
	done     chan struct{} // closed when the current worker returned
	poke     chan struct{} // a recount was asked for outside a content change
	changed  chan struct{} // closed when total or known changes
	total    int
	known    bool
	content  uint64 // the revision the last completed pass started at
	exact    bool   // no content change during that pass: total is content's
	idle     bool   // no pass is running
	recount  bool   // the next pass recomputes every chat
	chats    map[string]int
	mark     int64 // the last unread_dirty seq read
	reported bool
}

// WatchUnread keeps the running count current while the caller holds it; the
// first watcher starts it and the last one's release stops it. Writes in
// between keep their chats marked, so a later watcher resumes exactly.
func (l *Live) WatchUnread() (release func()) {
	c := &l.count
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.changed, c.poke = make(chan struct{}), make(chan struct{}, 1)
	}
	c.watchers++
	if c.watchers == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		previous, done := c.done, make(chan struct{})
		c.stop, c.done = cancel, done
		go func() {
			defer close(done)
			if previous != nil {
				<-previous
			}
			l.countUnread(ctx)
		}()
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			c.watchers--
			var done chan struct{}
			if c.watchers == 0 {
				c.stop()
				done = c.done
			}
			c.mu.Unlock()
			if done != nil {
				<-done
			}
		})
	}
}

// UnreadTotal is the running count: the alert total, whether it is known yet,
// and a channel closed at its next change.
func (l *Live) UnreadTotal() (total int, known bool, changed <-chan struct{}) {
	c := &l.count
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.changed == nil {
		c.changed, c.poke = make(chan struct{}), make(chan struct{}, 1)
	}
	return c.total, c.known, c.changed
}

func (l *Live) countUnread(ctx context.Context) {
	c := &l.count
	for {
		view, next := l.a.ViewChanges()
		if err := l.unreadPass(view.Content); err != nil && ctx.Err() == nil && l.a.Logf != nil {
			l.a.Logf("unread count: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-next:
		case <-c.poke:
		}
	}
}

// unreadPass recomputes the chats marked since the last pass (every chat the
// first time, or after a failure) and publishes the total if it moved.
func (l *Live) unreadPass(content uint64) (err error) {
	c := &l.count
	c.mu.Lock()
	full := c.chats == nil || c.recount
	c.recount, c.idle = false, false
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.idle = true
		if err != nil {
			c.recount = true // the marks read are passed: recount everything next time
		}
		c.mu.Unlock()
	}()
	keys, mark, err := l.a.UnreadDirtySince(c.mark)
	if err != nil {
		return err
	}
	var convs, peers []string
	for _, key := range keys {
		switch {
		case key == "*":
			full = true
		case strings.HasPrefix(key, "c:"):
			convs = append(convs, key[2:])
		case strings.HasPrefix(key, "p:"):
			peers = append(peers, key[2:])
		}
	}
	chats := c.chats
	if full {
		chats = map[string]int{}
		convs, peers = nil, nil
	}
	if full || len(convs) > 0 {
		list, err := l.a.Conversations()
		if err != nil {
			return err
		}
		listed := make(map[string]bool, len(list))
		for _, cv := range list {
			if !cv.Deleted {
				listed[cv.ID] = true
				if full {
					convs = append(convs, cv.ID)
				}
			}
		}
		for _, conv := range convs {
			if !listed[conv] {
				delete(chats, "c:"+conv)
				continue
			}
			msgs, err := l.a.ConversationMessages(conv)
			if err != nil {
				return err
			}
			n := 0
			for _, m := range msgs {
				if m.Unread { // events never count: shownRows drops only events
					n++
				}
			}
			chats["c:"+conv] = n
		}
	}
	if full {
		if peers, err = l.a.UnreadPeers(); err != nil {
			return err
		}
	}
	for _, peer := range peers {
		n, err := l.a.PeerUnread(peer)
		if err != nil {
			return err
		}
		chats["p:"+peer] = n
	}
	prefs, err := l.a.AlertPrefs()
	if err != nil {
		return err
	}
	muted := make(map[string]bool, len(prefs.Mutes))
	for _, conv := range prefs.Mutes {
		muted["c:"+conv] = true
	}
	total := 0
	for key, n := range chats {
		if !muted[key] {
			total += n
		}
	}
	// A write during the pass may be counted only in part: no cross-check
	// at this revision then. The write's own change starts the next pass.
	view, _ := l.a.ViewChanges()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chats, c.content, c.exact, c.mark = chats, content, view.Content == content, mark
	if !c.known || c.total != total {
		c.total, c.known = total, true
		close(c.changed)
		c.changed = make(chan struct{})
	}
	return nil
}

// checkUnread compares a complete overview's alert total with the running
// count settled at the same revision. A difference means a write changed a
// chat's unread without marking it: recount every chat, and say so once.
func (l *Live) checkUnread(content uint64, alert int) {
	c := &l.count
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.watchers == 0 || !c.known || !c.idle || !c.exact || c.content != content || c.total == alert {
		return
	}
	if !c.reported && l.a.Logf != nil {
		l.a.Logf("unread count: running total %d, overview %d at the same revision; counting again", c.total, alert)
	}
	c.reported, c.recount = true, true
	select {
	case c.poke <- struct{}{}:
	default:
	}
}
