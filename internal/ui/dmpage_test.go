package ui

import (
	"fmt"
	"net/http/httptest"
	"testing"
)

// The page shape is TDLib's getChatHistory (from, offset 0 to -99, limit up
// to 100), and an open with no anchor lands on the newest page or around the
// first unread (tdesktop loadAroundId, offset -limit/2).
func TestDMPageBounds(t *testing.T) {
	for _, c := range []struct {
		n, anchor, unread int
		p                 DMPage
		start, end        int
	}{
		{10, -1, -1, DMPage{}, 0, 10},                        // no limit: the whole chat
		{200, -1, -1, DMPage{Limit: 50}, 150, 200},           // newest page
		{200, -1, 180, DMPage{Limit: 50}, 150, 200},          // the newest page reaches the first unread
		{200, -1, 20, DMPage{Limit: 50}, 0, 46},              // around the first unread: 25 newer
		{200, 99, -1, DMPage{From: "x", Limit: 30}, 70, 100}, // from a message, older ones
		{200, 99, -1, DMPage{From: "x", Offset: -10, Limit: 30}, 80, 110},
		{200, 195, -1, DMPage{From: "x", Offset: -10, Limit: 30}, 170, 200},
		{20, -1, -1, DMPage{Limit: 50}, 0, 20},
	} {
		start, end := pageBounds(c.n, c.anchor, c.unread, c.p)
		if start != c.start || end != c.end {
			t.Errorf("%+v: [%d,%d), want [%d,%d)", c, start, end, c.start, c.end)
		}
	}
	var whole DMThread
	for i := range 120 {
		whole.Messages = append(whole.Messages, DMMessage{ID: fmt.Sprint(i), Unread: i >= 30})
	}
	page, err := pageMessages(whole, DMPage{Limit: 50})
	if err != nil || len(page.Messages) != 50 || page.Messages[0].ID != "6" || !page.Older || !page.Newer {
		t.Fatalf("around the first unread: %d rows from %s older=%v newer=%v err=%v", len(page.Messages), page.Messages[0].ID, page.Older, page.Newer, err)
	}
	if _, err := pageMessages(whole, DMPage{From: "nope", Limit: 10}); err == nil {
		t.Fatal("a message not in the chat paged")
	}
	for query, ok := range map[string]bool{"": true, "limit=50": true, "from=a&offset=-25&limit=50": true, "limit=101": false,
		"offset=1&limit=5": false, "offset=-10&limit=5": false, "from=a": false, "limit=x": false} {
		if _, got := dmPageOf(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/dm?id=c&"+query, nil)); got != ok {
			t.Errorf("%q accepted=%v, want %v", query, got, ok)
		}
	}
}
