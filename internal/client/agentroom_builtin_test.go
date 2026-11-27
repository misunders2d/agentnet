package client

import (
	"errors"
	"testing"
)

// MEL-842 (owner, 2026-11-27): the person's "My agents" room keeps its name
// and its person, because their agents find it by that name. Only that one
// room is built in: a newer group that only shares the title can be renamed.
func TestMyAgentsRoomCannotBeRenamedOrLeft(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice)
	runAgent(t, w.alice)
	publishGroupFixtureCaps(t, w.alice, true)
	a := w.alice
	room, err := a.MyAgentsRoom(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.RenameGroup(tctx(t), room, "Pi team"); !errors.Is(err, ErrBuiltInGroup) {
		t.Fatalf("rename of My agents: %v", err)
	}
	if _, err = a.LeaveGroup(tctx(t), room); !errors.Is(err, ErrBuiltInGroup) {
		t.Fatalf("leave of My agents: %v", err)
	}
	if again, err := a.MyAgentsRoom(tctx(t)); err != nil || again != room {
		t.Fatalf("My agents room changed: %s vs %s %v", again, room, err)
	}
	// A second "My agents" is not created (CreateGroup returns the room);
	// a look-alike comes from renaming another group.
	if again, err := a.CreateGroup(tctx(t), MyAgentsRoomTitle); err != nil || again.State.Conv != room {
		t.Fatalf("a new My agents: %v %v", again.State.Conv, err)
	}
	other, err := a.CreateGroup(tctx(t), "Other")
	if err != nil {
		t.Fatal(err)
	}
	if other, err = a.RenameGroup(tctx(t), other.State.Conv, MyAgentsRoomTitle); err != nil {
		t.Fatal(err)
	}
	// Both may carry the same creation second; the room is whichever
	// MyAgentsRoom picks (oldest, then lowest ID). The other is a look-alike.
	built, err := a.MyAgentsRoom(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	spare := other.State.Conv
	if built == spare {
		spare = room
	}
	if _, err = a.RenameGroup(tctx(t), built, "Pi team"); !errors.Is(err, ErrBuiltInGroup) {
		t.Fatalf("rename of the chosen My agents: %v", err)
	}
	if _, err = a.RenameGroup(tctx(t), spare, "Spare"); errors.Is(err, ErrBuiltInGroup) {
		t.Fatalf("a look-alike is not built in: %v", err)
	}
}
