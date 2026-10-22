package ui

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAttentionChangesDoNotWaitForOverview(t *testing.T) {
	runNodeCheck(t, "testdata/attention_prompt_check.mjs")
}

type attentionResponseFixture struct {
	*Fixture
	seq          uint64
	reminder     *ReminderView
	snapshotErr  error
	thread       *Thread
	threadChange bool
}

func (f *attentionResponseFixture) Changed() (uint64, <-chan struct{}) { return f.seq, nil }
func (f *attentionResponseFixture) Overview() (Overview, error) {
	return Overview{}, errors.New("attention actions must not build a workspace overview")
}
func (f *attentionResponseFixture) SetReminder(id string, due time.Time) error {
	f.seq++
	f.reminder = &ReminderView{Message: id, Due: due, Title: "Exact original"}
	return nil
}
func (f *attentionResponseFixture) DoneReminder(string) error {
	f.seq++
	f.reminder = nil
	return nil
}
func (f *attentionResponseFixture) CancelReminder(id string) error { return f.DoneReminder(id) }
func (f *attentionResponseFixture) reminderSnapshot(string) (*ReminderView, error) {
	f.seq++ // a concurrent change must not relabel the earlier snapshot
	return f.reminder, f.snapshotErr
}
func (f *attentionResponseFixture) Thread(id string) (Thread, error) {
	if f.thread != nil {
		if f.threadChange {
			f.seq++
		}
		return *f.thread, nil
	}
	f.seq++
	return Thread{ID: id}, nil
}

func TestThreadAttentionSnapshotRequiresStableRevision(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "arrival_during_assembly"}[changed], func(t *testing.T) {
			// The changed case includes a later archived arrival in its metadata.
			// Labeling that count with 10 would let an older overview at 10
			// retire it and erase the arrival. Omit only this optional fast path.
			f := &attentionResponseFixture{Fixture: NewFixture(time.Now), seq: 10, threadChange: changed,
				thread: &Thread{ID: "exact", PeerTopics: &PeerTopics{Peer: "peer/device", ArchivedUnread: 7}}}
			ts := httptest.NewUnstartedServer(nil)
			ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
			ts.Start()
			t.Cleanup(ts.Close)
			response := do(t, ts, "GET", "/api/thread?id=exact", "", authed(ts, nil))
			var view Thread
			if err := json.NewDecoder(response.Body).Decode(&view); err != nil || response.StatusCode != 200 || view.Seq != 10 {
				t.Fatalf("start-of-view revision: %+v %v", view, err)
			}
			if changed {
				if view.PeerTopics != nil || f.seq != 11 {
					t.Fatalf("mixed-revision aggregate was published: %+v", view)
				}
			} else if view.PeerTopics == nil || view.PeerTopics.ArchivedUnread != 7 {
				t.Fatalf("stable exact metadata absent: %+v", view)
			}
		})
	}
}
func (f *attentionResponseFixture) DM(id string) (DMThread, error) {
	f.seq++
	return DMThread{ID: id}, nil
}

func TestAttentionResponseSnapshotsUseExistingRoutes(t *testing.T) {
	f := &attentionResponseFixture{Fixture: NewFixture(time.Now), seq: 10}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	for _, action := range []string{"", "/done", "/cancel"} {
		before := f.seq
		response := do(t, ts, "POST", "/api/remind"+action, `{"id":"exact","due":2000000000}`, post(ts))
		var result struct {
			Note     string          `json:"note"`
			Seq      uint64          `json:"seq"`
			Message  string          `json:"message"`
			Reminder json.RawMessage `json:"reminder"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || result.Note == "" || result.Seq != before+1 || result.Message != "exact" {
			t.Fatalf("%s: status=%d result=%+v", action, response.StatusCode, result)
		}
		if action == "" {
			var reminder ReminderView
			if err := json.Unmarshal(result.Reminder, &reminder); err != nil || reminder.Message != "exact" || reminder.Due.Unix() != 2000000000 {
				t.Fatalf("pending snapshot: %+v %v", reminder, err)
			}
		} else if string(result.Reminder) != "null" {
			t.Fatalf("ended snapshot: %s", result.Reminder)
		}
	}
	f.snapshotErr = errors.New("snapshot unavailable after successful action")
	response := do(t, ts, "POST", "/api/remind/done", `{"id":"exact"}`, post(ts))
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != 200 || len(result) != 1 || result["note"] == "" {
		t.Fatalf("saved action keeps note-only fallback: %+v %v", result, err)
	}
	for _, route := range []string{"/api/dm?id=exact", "/api/thread?id=exact"} {
		before := f.seq
		response := do(t, ts, "GET", route, "", authed(ts, nil))
		var view struct{ Seq uint64 }
		if err := json.NewDecoder(response.Body).Decode(&view); err != nil || response.StatusCode != 200 || view.Seq != before {
			t.Fatalf("start-of-view revision %s: %+v %v", route, view, err)
		}
	}
}
