package client

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// An admin's device sees whether it may invite and the unused
// invitations, and withdraws one; another member learns neither. A Hub
// whose page browsers cannot trust (a certificate pin) refuses an
// invitation link before creating anything, in plain words.
func TestCreateInviteAndList(t *testing.T) {
	w := newWorld(t, "")
	ctx := tctx(t)
	if _, err := w.alice.CreateInvite(ctx, InviteOptions{TTL: time.Hour}); err == nil {
		t.Fatal("an invitation without a name or label")
	}
	if _, err := w.alice.CreateInvite(ctx, InviteOptions{Name: "Bob\nEve", TTL: time.Hour}); err == nil {
		t.Fatal("an unreadable name")
	}
	_, err := w.alice.CreateInvite(ctx, InviteOptions{Name: "Carol", TTL: time.Hour})
	var he *HubError
	if !errors.As(err, &he) || he.Status != http.StatusConflict || !strings.Contains(err.Error(), "browsers trust") {
		t.Fatalf("link from a pinned Hub: %v", err)
	}
	code := w.aliceInvites("dave") // a CLI invitation is listed too
	got, err := w.alice.Invites(ctx)
	if err != nil || !got.CanInvite || len(got.Invites) != 1 || got.Invites[0].Label != "dave" {
		t.Fatalf("admin's list: %+v %v", got, err)
	}
	if theirs, err := w.bob.Invites(ctx); err != nil || theirs.CanInvite || len(theirs.Invites) != 0 {
		t.Fatalf("member's list: %+v %v", theirs, err)
	}
	if err := w.bob.RevokeInvite(ctx, got.Invites[0].ID); err == nil {
		t.Fatal("a member withdrew an invitation")
	}
	if err := w.alice.RevokeInvite(ctx, got.Invites[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Join(ctx, filepath.Join(t.TempDir(), "dave"), code, "laptop"); err == nil {
		t.Fatal("a withdrawn invitation enrolled")
	}
}
