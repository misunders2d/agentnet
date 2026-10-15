package client

import "testing"

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
