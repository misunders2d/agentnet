package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestViewChangesKeepContentAcrossEphemeralBursts(t *testing.T) {
	f := newChangeFeed()
	f.bump()
	content, _ := f.view()
	for range 1000 {
		f.bumpKind("presence")
		f.bumpKind("typing")
	}
	next, _ := f.view()
	if next.Content != content.Content || next.Seq != 2001 || next.Typing <= content.Seq || next.Presence <= content.Seq {
		t.Fatalf("lost independent revisions: %+v -> %+v", content, next)
	}
	f.bump() // a durable edit following the burst still wins
	final, _ := f.view()
	if final.Content != final.Seq || final.Content <= next.Content {
		t.Fatal("durable change was hidden")
	}
}

func TestMemberPresenceKeepsContentRevision(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}],"truncated":false}`))
	before, _ := a.ViewChanges()
	a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"reconnecting","joined":5}],"truncated":false}`))
	after, _ := a.ViewChanges()
	if after.Content != before.Content || after.Presence <= before.Presence {
		t.Fatalf("presence rebuilt content: %+v -> %+v", before, after)
	}
	a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5},{"address":"new/phone","presence":"connected","joined":6}],"truncated":false}`))
	joined, _ := a.ViewChanges()
	if joined.Content <= after.Content {
		t.Fatal("enrollment must invalidate discovery and content")
	}
	t.Run("listed-roster-completion", func(t *testing.T) {
		w := newWorld(t, "") // No daemon: each roster completion is synchronous.
		alice, err := w.alice.CreatePerson(tctx(t), "Alice")
		if err != nil {
			t.Fatal(err)
		}
		carol := mustJoin(t, t.TempDir(), w.aliceInvites("carol"), "desk")
		second, err := carol.CreatePerson(tctx(t), "Carol")
		if err != nil {
			t.Fatal(err)
		}
		a := w.bob
		members, err := a.Members(tctx(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []PersonInfo{alice, second} {
			found := false
			for _, m := range members.Members {
				if m.Address == want.Address && m.Person != nil && m.Person.ID == want.Person && m.Person.Hash == want.Roster {
					found = true
				}
			}
			if !found {
				t.Fatal("actual published person reference missing")
			}
		}
		raw, err := json.Marshal(members)
		if err != nil {
			t.Fatal(err)
		}
		a.onMembers(raw)
		missing, err := a.ListedPersons()
		if err != nil || len(missing) != 0 {
			t.Fatal("pre-completion listed cache was not empty")
		}
		trustBefore := []int{trustRows(t, a, alice.Address), trustRows(t, a, second.Address)}
		before, _ := a.ViewChanges()
		a.checkPersons(tctx(t), members)
		people, err := a.ListedPersons()
		if err != nil || len(people) != 2 {
			t.Fatal("batch did not load both actual signed rosters")
		}
		for i, want := range []PersonInfo{alice, second} {
			found := false
			for _, p := range people {
				if p.Person == want.Person && p.Address == want.Address && p.Label == want.Label && p.Roster == want.Roster && p.State == "listed" {
					found = true
				}
			}
			if _, pinned, err := a.store.personByID(want.Person); err != nil || pinned || !found || trustRows(t, a, want.Address) != trustBefore[i] {
				t.Fatal("listed completion changed trust or lost the exact claimed identity")
			}
		}
		loaded, _ := a.ViewChanges()
		t.Logf("listed completion metadata: before_content=%d after_content=%d seq_delta=%d listed=%d", before.Content, loaded.Content, loaded.Seq-before.Seq, len(people))
		if loaded.Seq != before.Seq+1 || loaded.Content != loaded.Seq || loaded.Typing != before.Typing || loaded.Presence != before.Presence {
			t.Fatal("listed roster completion did not publish exactly one content event")
		}
		a.checkPersons(tctx(t), members)
		unchanged, _ := a.ViewChanges()
		if unchanged != loaded {
			t.Fatal("identical roster batch emitted a change")
		}
		a.checkPersons(tctx(t), protocol.Members{}) // Actual keep removes both entries in one batch.
		removed, _ := a.ViewChanges()
		if removed.Seq != loaded.Seq+1 || removed.Content != removed.Seq || a.listed.has(alice.Roster) || a.listed.has(second.Roster) {
			t.Fatal("listed removal did not prune both entries with one content event")
		}
		a.checkPersons(tctx(t), protocol.Members{})
		empty, _ := a.ViewChanges()
		if empty != removed {
			t.Fatal("empty prune emitted a change")
		}
		// The exact real roster must still match the member's advertised hash.
		mismatch := protocol.Members{Members: []protocol.Member{{Address: alice.Address, Person: &protocol.PersonRef{ID: alice.Person, Seq: alice.Seq, Hash: second.Roster}}}}
		a.checkPersons(tctx(t), mismatch)
		refused, _ := a.ViewChanges()
		if refused != empty || a.listed.has(second.Roster) || a.listed.has(alice.Roster) {
			t.Fatal("mismatched roster changed the cache or content")
		}
		a.onMembers(raw) // a new list requires a fresh profile before completion
		empty, _ = a.ViewChanges()
		faults := injectFaults(a)
		label, name, err := protocol.SplitAddress(alice.Address)
		if err != nil {
			t.Fatal(err)
		}
		faults.add("GET", "/v1/agents/"+label+"/"+name+"/profile", 1, false)
		one := protocol.Members{Members: []protocol.Member{{Address: alice.Address, Person: &protocol.PersonRef{ID: alice.Person, Seq: alice.Seq, Hash: alice.Roster}}}}
		a.checkPersons(tctx(t), one)
		failed, _ := a.ViewChanges()
		if failed != empty || a.listed.has(alice.Roster) {
			t.Fatal("failed profile fetch changed the cache or content")
		}
		// Direct mutator outcomes cover duplicates and the existing capacity bound.
		self, ok, err := w.alice.store.selfPerson(w.alice.Address)
		if err != nil || !ok {
			t.Fatal("actual signed fixture roster missing")
		}
		var cache listedCache
		if !cache.put(self.roster) || cache.put(self.roster) || !cache.keep(nil) || cache.keep(nil) {
			t.Fatal("cache mutators did not report actual insert/remove outcomes")
		}
		cache.byHash = make(map[string]protocol.PersonRoster, protocol.MaxMembers)
		for i := range protocol.MaxMembers {
			cache.byHash[string(rune(i))] = self.roster // Local capacity sentinels, never consumed as identity evidence.
		}
		if cache.put(self.roster) || len(cache.byHash) != protocol.MaxMembers || cache.has(self.roster.Hash()) {
			t.Fatal("full cache accepted a new roster hash")
		}
		still, _ := a.ViewChanges()
		if still != empty {
			t.Fatal("refused/no-op mutations emitted a content event")
		}
	})
}

// Workspace names arrive on the list rather than an individual Member. Even
// with identical discovery, keeping a new name must invalidate cached content.
func TestMemberWorkspaceNameAdvancesContentRevision(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}],"workspace":"Before"}`))
	before, _ := a.ViewChanges()
	a.onMembers([]byte(`{"members":[{"address":"vitalii/desk","presence":"connected","joined":5}],"workspace":"After"}`))
	after, _ := a.ViewChanges()
	if a.WorkspaceName() != "After" || after.Content <= before.Content {
		t.Fatalf("workspace rename lost: %q revisions %+v -> %+v", a.WorkspaceName(), before, after)
	}
}

func TestConfigWritesPublishContentChanges(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	before, _ := a.ViewChanges()
	if err := a.store.setConfig(map[string]string{"person_published": "fixture"}); err != nil {
		t.Fatal(err)
	}
	saved, _ := a.ViewChanges()
	if saved.Content <= before.Content {
		t.Fatal("configuration write did not invalidate content")
	}
	if err := a.store.deleteConfig("person_published"); err != nil {
		t.Fatal(err)
	}
	removed, _ := a.ViewChanges()
	if removed.Content <= saved.Content {
		t.Fatal("configuration deletion did not invalidate content")
	}
}
