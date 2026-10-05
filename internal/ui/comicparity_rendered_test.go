package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// MEL-528 parity as the default messenger renders it over a real
// installation (testdata/comic_parity_check.cjs): a reminder already due
// leads the list at the top of Chats and opens its message; a reminder is
// set from a message's menu (desktop) and its action sheet (phone), moved,
// done and cancelled; "Remind me about this chat" is in the header menu;
// automatic answers are turned on before they ask and off again; the
// typing preferences survive a reload; and no screen shows an address or a
// terminal command. At 1440x900 light and 390x844 dark. Opt-in: needs an
// installed Playwright (AGENTNET_PLAYWRIGHT) and Chromium
// (AGENTNET_CHROMIUM or /usr/bin/chromium).
func TestComicParityRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	alice, bob, live := liveWorld(t)
	ctx := t.Context()
	send := func(body, replyTo string) string {
		r, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: body, ReplyTo: replyTo})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	// One conversation (topic) with alice's device: the second follows the first.
	first := send("Please check the Savannah order before Friday", "")
	due := send("The Denver pallets arrive at dock 2", first)
	waitFor(t, "bob has both messages", func() bool {
		th, err := live.Thread(due)
		return err == nil && len(th.Messages) >= 2
	})
	// A reminder that is already due when the page opens.
	if _, err := bob.SetReminder(due, time.Now().Add(1500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reminder is due", func() bool {
		o, err := live.Overview()
		return err == nil && len(o.Reminders) == 1 && o.Reminders[0].Overdue
	})
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(live, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command(node, "testdata/comic_parity_check.cjs")
	cmd.Env = append(os.Environ(), "PARITY_URL="+ts.URL+"/?t="+testToken, "PARITY_FIRST="+first, "PARITY_DUE="+due,
		"PARITY_ADDRESSES="+alice.Address+","+bob.Address)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "comic parity check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// The group parts of MEL-528 as Comic renders them over Alice's real
// installation, admin of a group with Bob (testdata/comic_group_parity_check.cjs):
// the header menu renames the group and offers Leave, which the last admin
// is refused in words; "In this chat" makes Bob an admin and takes it back;
// bringing in Bob's agent shares "since a date" and lets the people ticked
// give it tasks without asking: tasks_from is exactly Bob's member keys.
// At 1440x900 light, and the phone room sheet at 390x844 dark. Opt-in as
// TestComicParityRendered.
func TestComicGroupParityRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hubDir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, hubDir, "127.0.0.1:0", "")
	alice := personAgent(t, ctx, testhub.BootstrapCode(t, hubDir), "laptop", "Alice")
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob := personAgent(t, ctx, code, "desk", "Bob")
	bobPerson, ok, err := bob.Person()
	if err != nil || !ok {
		t.Fatalf("bob's person: %v %v", ok, err)
	}
	packet, err := alice.CreateGroup(ctx, "Dock crew")
	if err != nil {
		t.Fatal(err)
	}
	admission, err := bob.SignGroupAdmission(packet.Root, 1, packet.State.Hash(), nil)
	if err != nil {
		t.Fatal(err)
	}
	packet.Proof = nil
	packet.State.Seq, packet.State.Prev = 1, packet.State.Hash()
	packet.State.Members = append(slices.Clone(packet.State.Members), protocol.GroupMember{ConvMember: protocol.ConvMember{Person: bobPerson.Person, Roster: bobPerson.Roster}, Admission: admission})
	slices.SortFunc(packet.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	if packet, err = alice.SignGroupState(ctx, packet); err != nil {
		t.Fatal(err)
	}
	commit, err := alice.BuildGroupCommit(ctx, packet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = alice.PublishGroup(ctx, commit, packet); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	for _, body := range []string{"Truck 4 is at dock 2", "Pallets for Denver are wrapped", "Savannah order ships Friday"} {
		if _, err := alice.SendConv(ctx, conv, client.ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	for _, d := range bobPerson.Devices {
		keys = append(keys, d.Fingerprint)
	}
	if len(keys) == 0 {
		keys = []string{bobPerson.Fingerprint}
	}
	live := NewLive(alice)
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(live, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command(node, "testdata/comic_group_parity_check.cjs")
	cmd.Env = append(os.Environ(), "PARITY_URL="+ts.URL+"/?t="+testToken, "PARITY_BOB_KEYS="+strings.Join(keys, ","),
		"PARITY_ADDRESSES="+alice.Address+","+bob.Address)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "comic group parity check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
