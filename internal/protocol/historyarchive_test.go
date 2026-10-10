package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHistoryArchiveManifestStrictBounds(t *testing.T) {
	r := HistoryArchive{V: 1, Person: NewID(), Roster: strings.Repeat("a", 64), Count: 1, Format: HistoryArchiveFormat}
	for _, count := range []int{1, MaxHistoryArchiveEntries} {
		r.Count = count
		data, _ := json.Marshal(r)
		if _, err := ParseHistoryArchive(data); err != nil {
			t.Fatal(err)
		}
	}
	for name, change := range map[string]func(*HistoryArchive){
		"version": func(r *HistoryArchive) { r.V = 2 },
		"owner":   func(r *HistoryArchive) { r.Person = "wrong" },
		"roster":  func(r *HistoryArchive) { r.Roster = "wrong" },
		"empty":   func(r *HistoryArchive) { r.Count = 0 },
		"count":   func(r *HistoryArchive) { r.Count = MaxHistoryArchiveEntries + 1 },
		"format":  func(r *HistoryArchive) { r.Format = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			n := r
			change(&n)
			data, _ := json.Marshal(n)
			if _, err := ParseHistoryArchive(data); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	data, _ := json.Marshal(r)
	for _, invalid := range [][]byte{append(data, []byte(` {}`)...), append(data[:len(data)-1], []byte(`,"extra":true}`)...), []byte(strings.Repeat(" ", 4097)), {0xff}} {
		if _, err := ParseHistoryArchive(invalid); err == nil {
			t.Fatal("non-strict manifest accepted")
		}
	}
}
