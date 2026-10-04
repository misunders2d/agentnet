package itest

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestCLIGroupSelectedHistoryFile(t *testing.T) {
	c := buildCLI(t)
	addr, _ := c.setup(t, "history-hub")
	code := c.run("--home", "alice", "admin", "invite", "--raw", "history-caps")
	invitation, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	for _, home := range []string{"alice", "bob", "carol"} {
		c.start(home+"-history.log", "--home", home, "daemon")
		c.run("--home", home, "person", "create", home)
	}
	for home, address := range map[string]string{"alice": "admin/laptop", "bob": "bob/desk", "carol": "carol/desk"} {
		groupCLIFixtureCaps(t, c, addr, home, address, invitation.CertPEM)
	}
	persons := map[string]string{}
	for _, home := range []string{"bob", "carol"} {
		a, e := client.Open(filepath.Join(c.dir, home))
		if e != nil {
			t.Fatal(e)
		}
		p, ok, e := a.Person()
		a.Close()
		if e != nil || !ok {
			t.Fatal(e)
		}
		persons[home] = p.Person
	}
	var packet client.GroupContext
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "create", "Selected native history")), &packet); err != nil {
		t.Fatal(err)
	}
	conv := packet.Root.ID()
	var bobInvite client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", conv, persons["bob"])), &bobInvite); err != nil || len(bobInvite.Proposal.History) != 0 {
		t.Fatal("default selected implicit history")
	}
	waitFor(t, "Bob pending native CLI invitation", func() bool { return strings.Contains(c.run("--home", "bob", "group", "invitations"), bobInvite.ID) })
	c.run("--home", "bob", "group", "accept", bobInvite.ID)
	waitFor(t, "Bob exact current group", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "bob"))
		if e != nil {
			return false
		}
		defer a.Close()
		p, e := a.GroupContext(conv)
		return e == nil && p.State.Seq == 1
	})
	unselected := c.writeRandom("unselected.bin", 8193)
	_ = unselected
	c.run("--home", "alice", "dm", "send", "--file", "unselected.bin", conv, "UNSELECTED earlier private turn")
	waitFor(t, "Bob receives parent", func() bool { return strings.Contains(dmShowArriving(c, "bob", conv), "UNSELECTED earlier private turn") })
	data := c.writeRandom("selected.bin", 45001)
	c.run("--home", "bob", "dm", "send", "--file", "selected.bin", conv, "SELECTED exact historical turn")
	waitFor(t, "Alice receives selected source", func() bool { return strings.Contains(dmShow(c, "alice", conv), "SELECTED exact historical turn") })
	var sinceInvite, selected client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", "--history-since", "2020-01-01T00:00:00Z", conv, persons["carol"])), &sinceInvite); err != nil || len(sinceInvite.Proposal.History) != 2 {
		t.Fatalf("since actual visible refs %v", err)
	}
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", "--history-last", "1", conv, persons["carol"])), &selected); err != nil || len(selected.Proposal.History) != 1 {
		t.Fatalf("last actual visible ref %v", err)
	}
	raw, _ := json.Marshal(selected.Proposal.History)
	if err = os.WriteFile(filepath.Join(c.dir, "selected-refs.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	var explicit client.GroupInvitationInfo
	if err = json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", "--history-refs", "selected-refs.json", conv, persons["carol"])), &explicit); err != nil || explicit.ID != selected.ID {
		t.Fatal("explicit refs did not resolve original selection")
	}
	if _, err = c.try("--home", "alice", "group", "invite", "--history-last", "65", conv, persons["carol"]); err == nil {
		t.Fatal("unbounded selector accepted")
	}
	waitFor(t, "Carol exact pending selection", func() bool { return strings.Contains(c.run("--home", "carol", "group", "invitations"), selected.ID) })
	// Before accepting, the group is not Carol's conversation: dm show says
	// so, or lists nothing, and never leaks history or file metadata.
	if text, err := c.try("--home", "carol", "dm", "show", conv); (err != nil && !strings.Contains(text, "no such conversation here")) ||
		strings.Contains(text, "earlier private turn") || strings.Contains(text, "historical turn") || strings.Contains(text, "selected.bin") {
		t.Fatalf("preaccept history/file metadata leaked or unexpected failure: %v\n%s", err, text)
	}
	c.run("--home", "carol", "group", "accept", selected.ID)
	var imported client.ConvMessage
	waitFor(t, "Carol only selected native history", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "carol"))
		if e != nil {
			return false
		}
		defer a.Close()
		rows, e := a.ConversationMessages(conv)
		if e != nil || len(rows) != 1 {
			return false
		}
		imported = rows[0]
		return imported.Body == "SELECTED exact historical turn" && imported.History && imported.Key == "" && len(imported.Attachments) == 1
	})
	if text := dmShow(c, "carol", conv); strings.Contains(text, "UNSELECTED") || strings.Contains(text, "unselected.bin") {
		t.Fatal("unselected history reached CLI")
	}
	if _, err = c.try("--home", "alice", "group", "request-file", conv, imported.ID, "0"); err == nil {
		t.Fatal("outbox/direct message became scoped history request")
	}
	if _, err = c.try("--home", "carol", "group", "request-file", strings.Repeat("a", 64), imported.ID, "0"); err == nil {
		t.Fatal("wrong group request accepted")
	}
	if _, err = c.try("--home", "carol", "group", "request-file", conv, imported.ID, "1"); err == nil {
		t.Fatal("wrong file index accepted")
	}
	if out := c.run("--home", "carol", "group", "request-file", conv, imported.ID, "0"); !strings.Contains(out, "requested") || strings.Contains(out, "downloaded") {
		t.Fatal("file request claimed download")
	}
	waitFor(t, "selected native file offer", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "carol"))
		if e != nil {
			return false
		}
		defer a.Close()
		rows, e := a.ConversationMessages(conv)
		return e == nil && len(rows) == 1 && len(rows[0].Attachments) == 1 && !strings.HasPrefix(rows[0].Attachments[0].BlobID, "history-")
	})
	got := c.downloadInto("carol", "selected-download", imported.ID)
	if !bytes.Equal(got, data) {
		t.Fatal("CLI selected file bytes differ")
	}
	c.run("--home", "carol", "dm", "send", conv, "new ordinary future turn")
	waitFor(t, "normal future group turns unchanged", func() bool { return strings.Contains(dmShow(c, "bob", conv), "new ordinary future turn") })
}
