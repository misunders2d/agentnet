package client

import (
	"bytes"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Time-only editing uses the same durable ID and revision through a second
// local API process and a restart. Real wall-clock delivery is a separate smoke.
func TestScheduledSendTimeOnlyEditRestart(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	path, data := writeFile(t, t.TempDir(), "reschedule.bin", 400)
	in := futureSchedule(conv)
	in.ID = protocol.NewID()
	in.Files = []OutgoingFile{{Path: path, Name: "retained.bin"}}
	original, err := a.ScheduleSend(in)
	if err != nil {
		t.Fatal(err)
	}
	api, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	edit := in
	edit.Due = original.Due.Add(time.Hour)
	edit.Files = nil
	saved, err := api.EditScheduledSend(original.ID, original.Rev, edit)
	api.Close()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != original.ID || saved.Rev != original.Rev+1 || !saved.Due.Equal(edit.Due) || saved.Body != original.Body || !slices.Equal(saved.Files, original.Files) || !slices.Equal(saved.Audience.Persons, original.Audience.Persons) || !slices.Equal(saved.Audience.PIDs, original.Audience.PIDs) || saved.Audience.AuthorPID != original.Audience.AuthorPID {
		t.Fatal("time-only save changed content, identity or audience", saved)
	}
	if _, err = a.ScheduleSend(in); !errors.Is(err, ErrScheduledIDConflict) {
		t.Fatal("old create retry overwrote reschedule", err)
	}
	for _, action := range []func() error{
		func() error { _, e := a.EditScheduledSend(original.ID, original.Rev, in); return e },
		func() error { _, e := a.CancelScheduledSend(original.ID, original.Rev); return e },
		func() error { _, e := a.SendScheduledNow(tctx(t), original.ID, original.Rev); return e },
	} {
		if err = action(); !errors.Is(err, ErrScheduledStale) {
			t.Fatal("stale revision changed reschedule", err)
		}
	}
	a.stopBackgroundPosts()
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	loaded := scheduleOf(t, a, saved.ID)
	if !loaded.Due.Equal(saved.Due) || loaded.Rev != saved.Rev || loaded.State != ScheduledPending || loaded.Body != saved.Body || !slices.Equal(loaded.Files, saved.Files) || !slices.Equal(loaded.Audience.Persons, saved.Audience.Persons) || !slices.Equal(loaded.Audience.PIDs, saved.Audience.PIDs) || loaded.Audience.AuthorPID != saved.Audience.AuthorPID {
		t.Fatal("restart lost the time-only edit", loaded)
	}
	next, err := a.scheduledDue(tctx(t), original.Due)
	if err != nil || !next.Equal(saved.Due) || scheduleCopies(t, a, loaded) != 0 {
		t.Fatal("old deadline query did not retain the new deadline", next, err)
	}
	plain, err := a.openKept(t.TempDir(), loaded.Files[0].SHA256, loaded.Files[0].Size)
	if err != nil {
		t.Fatal("restart lost attachment", err)
	}
	got, err := os.ReadFile(plain)
	os.Remove(plain)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("time-only edit changed attachment bytes", err)
	}
}
