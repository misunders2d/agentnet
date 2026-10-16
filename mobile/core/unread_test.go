package core

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui"
)

type unreadValue struct {
	total   int64
	partial bool
}
type unreadFunc func(int64, bool)

func (f unreadFunc) Unread(n int64, p bool) { f(n, p) }

// A non-comparable interface implementation must also be safe to replace.
type unreadSlice []func(int64, bool)

func (f unreadSlice) Unread(n int64, p bool) { f[0](n, p) }
func takeUnread(t *testing.T, ch <-chan unreadValue, want unreadValue) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got := <-ch:
			if got == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("missing unread value %+v", want)
		}
	}
}
func unreadRead(n int) func() (ui.Overview, error) {
	return func() (ui.Overview, error) { return ui.Overview{AlertTotal: &n}, nil }
}
func refreshForTest(a *App, id string, s *Session, read func() (ui.Overview, error)) {
	s.mu.Lock()
	gen := s.contentGeneration
	rev, _ := s.a.ViewChanges()
	s.mu.Unlock()
	a.refreshUnread(id, s, rev.Content, gen, read)
}
func unreadApp(sessions map[string]*Session) *App {
	a := &App{sessions: sessions, active: map[string]bool{}, unread: map[string]workspaceUnread{}}
	for id := range sessions {
		a.active[id] = true
		a.bindUnreadLocked(id)
	}
	return a
}
func TestAppUnreadSinkContract(t *testing.T) {
	var _ interface{ SetUnreadSink(UnreadSink) } = (*App)(nil)
	s, _ := fixture(t)
	other, _ := fixture(t)
	a := unreadApp(map[string]*Session{"one": s, "two": other})
	ch := make(chan unreadValue, 64)
	var deliveries atomic.Int32
	a.SetUnreadSink(unreadSlice{func(n int64, p bool) { a.mu.Lock(); a.mu.Unlock(); deliveries.Add(1); ch <- unreadValue{n, p} }})
	takeUnread(t, ch, unreadValue{0, true}) // Neither workspace has ever been read.
	refreshForTest(a, "one", s, unreadRead(3))
	takeUnread(t, ch, unreadValue{3, true})
	refreshForTest(a, "two", other, unreadRead(2))
	takeUnread(t, ch, unreadValue{5, false})
	idle := func() bool { a.mu.Lock(); defer a.mu.Unlock(); return !a.unreadFlushing }
	waitMobile(t, "unread delivery drained", idle)
	delivered := deliveries.Load()
	refreshForTest(a, "one", s, unreadRead(3))
	waitMobile(t, "duplicate delivery drained", idle)
	if deliveries.Load() != delivered {
		t.Fatal("identical aggregate was redelivered")
	}
	refreshForTest(a, "two", other, func() (ui.Overview, error) { return ui.Overview{}, errors.New("read failed") })
	takeUnread(t, ch, unreadValue{5, true}) // Last known contribution retained, flagged unknown.
	a.mu.Lock()
	a.active["one"] = false
	delete(a.unread, "one")
	a.markUnreadDirtyLocked()
	a.mu.Unlock()
	takeUnread(t, ch, unreadValue{2, true})
	refreshForTest(a, "two", other, unreadRead(0))
	takeUnread(t, ch, unreadValue{0, false})
	refreshForTest(a, "one", s, unreadRead(99)) // Removed workspace cannot revive.
	a.mu.Lock()
	got, p := a.unreadTotalLocked()
	a.mu.Unlock()
	if got != 0 || p {
		t.Fatal(got, p)
	}
	a.mu.Lock()
	a.active["one"] = true
	a.bindUnreadLocked("one")
	a.unread["one"] = workspaceUnread{math.MaxInt64, true}
	a.unread["two"] = workspaceUnread{2, true}
	got, p = a.unreadTotalLocked()
	a.mu.Unlock()
	if got != math.MaxInt64 || p {
		t.Fatal("sum did not saturate", got, p)
	}
	a.SetUnreadSink(nil)
}
func TestAppUnreadDelayedReadFences(t *testing.T) {
	for _, change := range []string{"new-content", "remove", "rebind", "same-session-rebind", "close"} {
		t.Run(change, func(t *testing.T) {
			s, _ := fixture(t)
			next, _ := fixture(t)
			a := unreadApp(map[string]*Session{"w": s})
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				refreshForTest(a, "w", s, func() (ui.Overview, error) { close(entered); <-release; return unreadRead(8)() })
			}()
			<-entered
			switch change {
			case "new-content":
				s.a.NoteChange()
				refreshForTest(a, "w", s, unreadRead(2))
			case "remove":
				a.mu.Lock()
				a.active["w"] = false
				delete(a.unread, "w")
				a.mu.Unlock()
			case "rebind":
				a.mu.Lock()
				a.sessions["w"] = next
				a.bindUnreadLocked("w")
				a.mu.Unlock()
				refreshForTest(a, "w", next, unreadRead(2))
			case "same-session-rebind":
				s.mu.Lock()
				s.contentGeneration++
				s.mu.Unlock()
				a.mu.Lock()
				a.bindUnreadLocked("w")
				a.mu.Unlock()
				refreshForTest(a, "w", s, unreadRead(2))
			case "close":
				a.mu.Lock()
				a.closed = true
				clear(a.unread)
				a.mu.Unlock()
			}
			close(release)
			<-done
			a.mu.Lock()
			total, partial := a.unreadTotalLocked()
			a.mu.Unlock()
			if change == "remove" || change == "close" {
				if total != 0 || !partial {
					t.Fatal(total, partial)
				}
			} else if total != 2 || partial {
				t.Fatal("stale completion replaced current result", total, partial)
			}
		})
	}
}
func TestAppUnreadCoalescingReplacementReentryAndClose(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	a, err := OpenApp(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	var oldCalls atomic.Int32
	a.SetUnreadSink(unreadFunc(func(int64, bool) {
		if oldCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}))
	<-entered
	ch := make(chan unreadValue, 100)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a.mu.Lock()
			a.unread["default"] = workspaceUnread{int64(i), true}
			a.markUnreadDirtyLocked()
			a.mu.Unlock()
		}(i)
	}
	wg.Wait()
	a.mu.Lock()
	a.unread["default"] = workspaceUnread{17, true}
	a.markUnreadDirtyLocked()
	a.mu.Unlock()
	a.SetUnreadSink(unreadFunc(func(n int64, p bool) { ch <- unreadValue{n, p} }))
	close(release)
	takeUnread(t, ch, unreadValue{17, false})
	if oldCalls.Load() != 1 {
		t.Fatal("old sink resurrected", oldCalls.Load())
	}
	a.mu.Lock()
	a.markUnreadDirtyLocked()
	a.mu.Unlock()
	// Reentry replaces an interface containing a slice (no interface equality).
	swapped := make(chan struct{})
	a.SetUnreadSink(unreadSlice{func(int64, bool) {
		a.SetUnreadSink(unreadFunc(func(n int64, p bool) { ch <- unreadValue{n, p} }))
		close(swapped)
	}})
	select {
	case <-swapped:
	case <-time.After(3 * time.Second):
		t.Fatal("setter reentry deadlocked")
	}
	takeUnread(t, ch, unreadValue{17, false})
	a.Close()
	takeUnread(t, ch, unreadValue{0, true})
	var late atomic.Int32
	a.SetUnreadSink(unreadFunc(func(int64, bool) { late.Add(1) }))
	refreshForTest(a, "default", a.session, unreadRead(99))
	a.mu.Lock()
	total, partial := a.unreadTotalLocked()
	a.mu.Unlock()
	if total != 0 || !partial || late.Load() != 0 {
		t.Fatal("late hook or setter after close", total, partial, late.Load())
	}
}
func TestAppUnreadSinkMayClose(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	a, err := OpenApp(home, 0)
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan unreadValue, 8)
	var once sync.Once
	a.SetUnreadSink(unreadFunc(func(n int64, p bool) { once.Do(a.Close); ch <- unreadValue{n, p} }))
	takeUnread(t, ch, unreadValue{0, true})
	a.Close()
}
func TestSessionContentHookSerialAndEphemeralCost(t *testing.T) {
	s, _ := fixture(t)
	var calls atomic.Int32
	var active atomic.Int32
	var maximum atomic.Int32
	first, release, second := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.mu.Lock()
	s.contentGeneration++
	s.content = func(uint64, uint64) {
		n := active.Add(1)
		if n > maximum.Load() {
			maximum.Store(n)
		}
		defer active.Add(-1)
		switch calls.Add(1) {
		case 1:
			close(first)
			<-release
		case 2:
			close(second)
		}
	}
	s.mu.Unlock()
	s.signal()
	<-first
	s.a.NoteChange()
	s.a.NoteChange()
	close(release)
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("content during slow hook was lost")
	}
	// A wake without a content change is exactly the gate used for typing/presence.
	listener := make(chan struct{}, 1)
	s.SetListener(callback(func() {
		select {
		case listener <- struct{}{}:
		default:
		}
	}))
	for range 100 {
		s.signal()
	}
	select {
	case <-listener:
	case <-time.After(3 * time.Second):
		t.Fatal("user listener was replaced")
	}
	s.mu.Lock()
	s.content = nil
	s.mu.Unlock()
	if maximum.Load() != 1 || calls.Load() != 2 {
		t.Fatal("content recomputed on ephemeral wake", maximum.Load(), calls.Load())
	}
}

// Real signed homes and streams feed both cached Live overviews. Deliberate
// unread on ordinary sent messages gives exact local counts without faking rows.
func TestAppUnreadRealWorkspacesMutesReadAndTyping(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	relayHome := t.TempDir()
	relay := testhub.Start(t, relayHome, "127.0.0.1:0", "")
	sender, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, relayHome), "desktop")
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	if _, err = sender.CreatePerson(ctx, "Sender"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	id := strings.Repeat("a", 32)
	for i, dir := range []string{home, filepath.Join(home, "workspaces", id)} {
		invite, e := sender.Invite(ctx, []string{"phone-one", "phone-two"}[i], time.Hour, false)
		if e != nil {
			t.Fatal(e)
		}
		phone, e := client.Join(ctx, dir, invite, "phone")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = phone.CreatePerson(ctx, []string{"One", "Two"}[i]); e != nil {
			phone.Close()
			t.Fatal(e)
		}
		phone.Close()
	}
	registry, e := client.OpenWorkspaces(home)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := registry.List()
	if e != nil {
		t.Fatal(e)
	}
	entries = append(entries, client.Workspace{ID: id, Name: "Second", Endpoint: "https://" + relay.Addr, Address: "phone-two/phone", State: "enrolled"})
	raw, _ := json.Marshal(struct {
		Version int                `json:"version"`
		Items   []client.Workspace `json:"items"`
	}{1, entries})
	if e = secfile.Write(filepath.Join(home, "workspaces.json"), raw); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = sender.Run(ctx, client.RunOptions{HumanOnly: true}) }()
	defer func() { cancel(); <-done }()
	app, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer app.Close()
	if e = app.Start(); e != nil {
		t.Fatal(e)
	}
	one, two := app.sessions["default"], app.sessions[id]
	waitMobile(t, "three actual push streams", func() bool {
		return sender.MemberView().Current && one.a.MemberView().Current && two.a.MemberView().Current
	})
	convs := map[*Session]string{}
	for _, s := range []*Session{one, two} {
		conv, e := s.a.CreateDM(ctx, sender.Address)
		if e != nil {
			t.Fatal(e)
		}
		convs[s] = conv
	}
	for _, s := range []*Session{one, two} {
		n := 3
		if s == two {
			n = 2
		}
		var ids []string
		for range n {
			sent, e := s.a.SendConv(ctx, convs[s], client.ConvOutgoing{Body: "Ordinary deliberate unread"})
			if e != nil {
				t.Fatal(e)
			}
			ids = append(ids, sent.ID)
		}
		if e = s.a.MarkUnread(ids[0], ids); e != nil {
			t.Fatal(e)
		}
	}
	ch := make(chan unreadValue, 128)
	app.SetUnreadSink(unreadFunc(func(n int64, p bool) { ch <- unreadValue{n, p} }))
	takeUnread(t, ch, unreadValue{5, false})
	for s, want := range map[*Session]int{one: 3, two: 2} {
		v, e := s.live.Overview()
		if e != nil || v.AlertTotal == nil || *v.AlertTotal != want {
			t.Fatal("cached shared scalar", v.AlertTotal, e)
		}
	}
	prefs, e := one.a.AlertPrefs()
	if e != nil {
		t.Fatal(e)
	}
	prefs.Mutes = []string{convs[one]}
	if e = one.a.SetAlertPrefs(prefs); e != nil {
		t.Fatal(e)
	}
	takeUnread(t, ch, unreadValue{2, false})
	prefs.Mutes = nil
	if e = one.a.SetAlertPrefs(prefs); e != nil {
		t.Fatal(e)
	}
	takeUnread(t, ch, unreadValue{5, false})
	// Actual ephemeral typing updates the user listener, without another overview.
	waitMobile(t, "sender's verified conversation", func() bool {
		rows, e := sender.Conversations()
		if e != nil {
			return false
		}
		for _, row := range rows {
			if row.ID == convs[one] {
				return true
			}
		}
		return false
	})
	var reads atomic.Int32
	one.mu.Lock()
	hook := one.content
	one.content = func(c, g uint64) { reads.Add(1); hook(c, g) }
	one.mu.Unlock()
	before, _ := one.a.ViewChanges()
	count := reads.Load()
	if _, e = sender.SendTyping(ctx, protocol.TypingScope{Conv: convs[one]}, true); e != nil {
		t.Fatal(e)
	}
	waitMobile(t, "actual typing pulse", func() bool { v, _ := one.a.ViewChanges(); return v.Typing > before.Typing })
	listener := make(chan struct{}, 1)
	one.SetListener(callback(func() {
		select {
		case listener <- struct{}{}:
		default:
		}
	}))
	select {
	case <-listener:
	case <-time.After(3 * time.Second):
		t.Fatal("typing did not retain user listener")
	}
	after, _ := one.a.ViewChanges()
	if after.Content != before.Content || reads.Load() != count {
		t.Fatalf("typing recomputed overview: before=%+v after=%+v reads=%d", before, after, reads.Load()-count)
	}
	if e = app.providers.Disconnect(id); e != nil {
		t.Fatal(e)
	}
	takeUnread(t, ch, unreadValue{3, false})
	if e = app.providers.Reconnect(id); e != nil {
		t.Fatal(e)
	}
	takeUnread(t, ch, unreadValue{5, false})
	rows, e := one.a.ConversationMessages(convs[one])
	if e != nil || len(rows) != 3 {
		t.Fatal(len(rows), e)
	}
	markCurrent := func(ids []string) {
		t.Helper()
		view, err := one.live.DM(convs[one])
		if err != nil {
			t.Fatal(err)
		}
		selected := map[string]bool{}
		for _, id := range ids {
			selected[id] = true
		}
		attention := map[string]protocol.AttentionGeneration{}
		for _, message := range view.Messages {
			if selected[message.ID] && message.Attention != nil {
				attention[message.ID] = *message.Attention
			}
		}
		if err = one.a.MarkReadWithAttention(ids, attention); err != nil {
			t.Fatal(err)
		}
	}
	markCurrent([]string{rows[0].ID})
	takeUnread(t, ch, unreadValue{4, false})
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	markCurrent(ids)
	takeUnread(t, ch, unreadValue{2, false})
	app.Close()
	takeUnread(t, ch, unreadValue{0, true})
}

func TestSessionUnreadReusesComicOverview(t *testing.T) {
	s, _ := fixture(t)
	page, err := s.live.TopicOverview()
	if err != nil {
		t.Fatal(err)
	}
	badge, err := s.unreadOverview()
	if err != nil {
		t.Fatal(err)
	}
	// The cached DTO returns the same scalar pointer. This witnesses reuse without
	// private-field access, a production test hook, or Agent.Close (which itself
	// advances Content and would invalidate the cache being tested).
	if page.AlertTotal == nil || badge.AlertTotal != page.AlertTotal {
		t.Fatal("badge did not reuse Comic's cached alert scalar")
	}
	wrongBranch, err := s.live.Overview()
	if err != nil {
		t.Fatal(err)
	}
	if wrongBranch.AlertTotal == page.AlertTotal {
		t.Fatal("negative control did not distinguish the second cache branch")
	}
}
