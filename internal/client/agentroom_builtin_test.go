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
	other, err := a.CreateGroup(tctx(t), MyAgentsRoomTitle)
	if err != nil {
		t.Fatal(err)
	}
	if other.State.Conv == room {
		t.Fatal("look-alike is the room")
	}
	if _, err = a.RenameGroup(tctx(t), other.State.Conv, "Spare"); errors.Is(err, ErrBuiltInGroup) {
		t.Fatalf("a look-alike is not built in: %v", err)
	}
}
