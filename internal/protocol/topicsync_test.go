package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTopicSyncStrictPrivateTitles(t *testing.T) {
	base := TopicSync{V: 1, Person: NewID(), Roster: strings.Repeat("a", 64), Titles: []TopicTitle{{Scope: strings.Repeat("b", 64), Topic: NewID(), Title: "Привіт 🌍", Rev: 1, Writer: "aaaaaaaa-bbbbbbbb-cccccccc-dddddddd"}}}
	check := func(r TopicSync, want bool) {
		t.Helper()
		raw, _ := json.Marshal(r)
		_, err := ParseTopicSync(raw)
		if (err == nil) != want {
			t.Fatalf("valid=%v want=%v: %v", err == nil, want, err)
		}
	}
	check(base, true)
	for _, title := range []string{"", strings.Repeat("🌍", 120)} {
		r := base
		r.Titles = append([]TopicTitle(nil), base.Titles...)
		r.Titles[0].Title = title
		check(r, true)
	}
	for _, mutate := range []func(*TopicSync){
		func(r *TopicSync) { r.Titles[0].Title = strings.Repeat("🌍", 121) },
		func(r *TopicSync) { r.Titles[0].Title = "not\ncanonical" },
		func(r *TopicSync) { r.Titles[0].Title = "not\u0085canonical" },
		func(r *TopicSync) { r.Titles[0].Rev = 0 },
		func(r *TopicSync) { r.Titles[0].Rev = MaxTopicTitleRevision + 1 },
		func(r *TopicSync) { r.Titles[0].Scope = "not a scope" },
		func(r *TopicSync) { r.Titles = append(r.Titles, r.Titles[0]) },
		func(r *TopicSync) { r.Titles = nil },
	} {
		r := base
		r.Titles = append([]TopicTitle(nil), base.Titles...)
		mutate(&r)
		check(r, false)
	}
	r := base
	r.Titles = append([]TopicTitle(nil), base.Titles...)
	r.Titles[0].Scope = "admin/laptop"
	check(r, true)
	raw, _ := json.Marshal(base)
	for _, bad := range []string{
		strings.Replace(string(raw), `"title":"Привіт 🌍",`, "", 1),
		strings.Replace(string(raw), `"title":"Привіт 🌍"`, `"title":null`, 1),
		strings.Replace(string(raw), `"rev":1`, `"rev":1.5`, 1),
		strings.Replace(string(raw), `"v":1`, `"v":1,"grant":true`, 1),
	} {
		if _, err := ParseTopicSync([]byte(bad)); err == nil {
			t.Fatal("malformed title accepted")
		}
	}
}
