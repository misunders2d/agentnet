package itest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestCLIGroupGovernanceOfflineLeave(t *testing.T) {
	c := buildCLI(t)
	addr, stopHub := c.setup(t, "governance-hub")
	invitation, err := protocol.DecodeInvite(c.run("--home", "alice", "admin", "invite", "--raw", "governance-caps"))
	if err != nil {
		t.Fatal(err)
	}
	c.run("--home", "carol", "join", "--agent", "desk", c.run("--home", "alice", "admin", "invite", "--raw", "carol"))
	stops := map[string]func(){}
	for _, home := range []string{"alice", "bob", "carol"} {
		stops[home] = c.start(home+"-governance.log", "--home", home, "daemon")
		c.run("--home", home, "person", "create", home)
	}
	var link string
	for _, line := range strings.Split(c.run("--home", "alice", "person", "link"), "\n") {
		if strings.HasPrefix(line, "agentnet-link-v2:") {
			link = line
		}
	}
	if link == "" {
		t.Fatal("missing disposable linked-phone offer")
	}
	c.run("--home", "phone", "join", "--agent", "phone", link)
	stops["phone"] = c.start("phone-governance.log", "--home", "phone", "daemon")
	var request string
	waitFor(t, "actual linked admin phone request", func() bool {
		for _, line := range strings.Split(c.run("--home", "alice", "person", "links"), "\n") {
			f := strings.Fields(line)
			if len(f) > 2 && f[1] == "pending" && f[2] == "admin/phone" {
				request = f[0]
			}
		}
		return request != ""
	})
	c.run("--home", "alice", "person", "approve", request)
	addresses := map[string]string{"alice": "admin/laptop", "bob": "bob/desk", "carol": "carol/desk", "phone": "admin/phone"}
	oldSessions := map[string][]string{}
	for home, address := range addresses {
		oldSessions[home] = groupCLIFixtureCaps(t, c, addr, home, address, invitation.CertPEM)
	}
	personIDs := map[string]string{}
	for _, home := range []string{"alice", "bob", "carol"} {
		a, e := client.Open(filepath.Join(c.dir, home))
		if e != nil {
			t.Fatal(e)
		}
		p, ok, e := a.Person()
		a.Close()
		if e != nil || !ok {
			t.Fatal(e)
		}
		personIDs[home] = p.Person
	}
	decodePacket := func(home string, args ...string) client.GroupContext {
		t.Helper()
		var p client.GroupContext
		cmd := append([]string{"--home", home, "group"}, args...)
		if e := json.Unmarshal([]byte(c.run(cmd...)), &p); e != nil {
			t.Fatal(e)
		}
		return p
	}
	packet := decodePacket("alice", "create", "Native governance")
	conv := packet.Root.ID()
	defer func() {
		if t.Failed() {
			groupGovernanceFailureDetails(t, c, addr, invitation.CertPEM, conv)
		}
	}()
	waitCurrent := func(p client.GroupContext, homes ...string) {
		t.Helper()
		for _, home := range homes {
			waitFor(t, "exact current group "+home, func() bool {
				a, e := client.Open(filepath.Join(c.dir, home))
				if e != nil {
					return false
				}
				defer a.Close()
				got, e := a.GroupContext(conv)
				return e == nil && got.State.Hash() == p.State.Hash()
			})
		}
	}
	inviteMember := func(targetHome, decideHome string) {
		t.Helper()
		var inv client.GroupInvitationInfo
		if e := json.Unmarshal([]byte(c.run("--home", "alice", "group", "invite", conv, personIDs[targetHome])), &inv); e != nil {
			t.Fatal(e)
		}
		waitFor(t, "explicit CLI pending admission "+targetHome, func() bool { return strings.Contains(c.run("--home", decideHome, "group", "invitations"), inv.ID) })
		c.run("--home", decideHome, "group", "accept", inv.ID)
		waitFor(t, "automatic publication exact CLI consent", func() bool {
			a, e := client.Open(filepath.Join(c.dir, "alice"))
			if e != nil {
				return false
			}
			defer a.Close()
			p, e := a.GroupContext(conv)
			if e != nil {
				return false
			}
			_, ok := p.State.Member(personIDs[targetHome])
			if ok {
				packet = p
			}
			return ok
		})
		waitCurrent(packet, "alice", "bob", "phone")
	}
	inviteMember("bob", "bob")
	inviteMember("carol", "carol")
	waitCurrent(packet, "carol")
	if out, e := c.try("--home", "bob", "group", "rename", conv, "unauthorized"); e == nil || !strings.Contains(out, "administrator") {
		t.Fatal("ordinary CLI governance not refused")
	}
	if out, e := c.try("--home", "phone", "group", "leave", conv); e == nil || !strings.Contains(out, "successor") {
		t.Fatal("last administrator CLI leave not refused")
	}
	packet = decodePacket("phone", "rename", conv, "Linked phone governs")
	waitCurrent(packet, "alice", "bob", "carol")
	packet = decodePacket("phone", "promote", conv, personIDs["bob"])
	waitCurrent(packet, "bob", "alice")
	packet = decodePacket("bob", "demote", conv, personIDs["alice"])
	waitCurrent(packet, "phone", "alice", "carol")
	if _, e := c.try("--home", "phone", "group", "rename", conv, "stale admin"); e == nil {
		t.Fatal("demoted linked admin retained authority")
	}
	if out, e := c.try("--home", "bob", "group", "demote", conv, personIDs["bob"]); e == nil || !strings.Contains(out, "successor") {
		t.Fatal("last administrator CLI demotion not refused")
	}
	packet = decodePacket("bob", "promote", conv, personIDs["alice"])
	waitCurrent(packet, "phone", "alice", "carol")
	var adminLeft client.GroupLeaveResult
	if e := json.Unmarshal([]byte(c.run("--home", "phone", "group", "leave", conv)), &adminLeft); e != nil || adminLeft.Context == nil || adminLeft.Queued {
		t.Fatalf("admin leave not CAS: %v", e)
	}
	packet = *adminLeft.Context
	waitCurrent(packet, "bob", "carol")
	// Rejoin is a fresh human decision; the old role/admission cannot revive.
	var rejoin client.GroupInvitationInfo
	if e := json.Unmarshal([]byte(c.run("--home", "bob", "group", "invite", conv, personIDs["alice"])), &rejoin); e != nil {
		t.Fatal(e)
	}
	waitFor(t, "fresh Alice rejoin proposal", func() bool { return strings.Contains(c.run("--home", "phone", "group", "invitations"), rejoin.ID) })
	if _, ok := packet.State.Member(personIDs["alice"]); ok {
		t.Fatal("rejoin invited without explicit consent")
	}
	c.run("--home", "phone", "group", "accept", rejoin.ID)
	waitFor(t, "fresh rejoin publication", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "bob"))
		if e != nil {
			return false
		}
		defer a.Close()
		p, e := a.GroupContext(conv)
		if e != nil {
			return false
		}
		m, ok := p.State.Member(personIDs["alice"])
		if ok && m.Admission.Seq == rejoin.Proposal.State.Seq+1 {
			packet = p
			return true
		}
		return false
	})
	waitCurrent(packet, "alice", "phone", "carol")
	for _, stop := range stops {
		stop()
	}
	stopHub()
	var left client.GroupLeaveResult
	if e := json.Unmarshal([]byte(c.run("--home", "carol", "group", "leave", conv)), &left); e != nil || !left.Queued || left.Withdrawal == nil {
		t.Fatalf("CLI offline leave not locally queued: %v", e)
	}
	exact, _ := json.Marshal(left.Withdrawal)
	var again client.GroupLeaveResult
	if e := json.Unmarshal([]byte(c.run("--home", "carol", "group", "leave", conv)), &again); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(again.Withdrawal)
	if !bytes.Equal(exact, raw) {
		t.Fatal("offline restart re-signed departure")
	}
	c.start("governance-hub-restart.log", "hub", "serve", "--data", "governance-hub", "--listen", addr)
	waitFor(t, "restarted Hub accepts signed native request", func() bool {
		_, e := c.try("--home", "bob", "members")
		return e == nil
	})
	for home := range addresses {
		c.start(home+"-governance-restart.log", "--home", home, "daemon")
	}
	for home, address := range addresses {
		groupCLIFixtureCaps(t, c, addr, home, address, invitation.CertPEM, oldSessions[home])
	}
	waitFor(t, "offline departure reaches current peers after restart", func() bool {
		for _, home := range []string{"bob", "alice", "phone"} {
			a, e := client.Open(filepath.Join(c.dir, home))
			if e != nil {
				return false
			}
			members, e := a.GroupMembers(conv)
			a.Close()
			if e != nil {
				return false
			}
			for _, m := range members {
				if m.Person == personIDs["carol"] {
					return false
				}
			}
		}
		return true
	})
	data := c.writeRandom("after-leave.bin", 16001)
	c.run("--home", "bob", "dm", "send", "--file", "after-leave.bin", conv, "ordinary file after explicit departure")
	var message string
	waitFor(t, "remaining member receives direct future file", func() bool {
		a, e := client.Open(filepath.Join(c.dir, "phone"))
		if e != nil {
			return false
		}
		defer a.Close()
		rows, e := a.ConversationMessages(conv)
		if e != nil {
			return false
		}
		for _, row := range rows {
			// Own-device catch-up may reach the phone before Bob's direct
			// copy. That history row is replaced when direct delivery arrives;
			// this journey checks direct future delivery and its attached bytes.
			if row.Body == "ordinary file after explicit departure" && !row.History && row.Key != "" && len(row.Attachments) == 1 {
				message = row.ID
				return true
			}
		}
		return false
	})
	os.MkdirAll(filepath.Join(c.dir, "phone-download"), 0700)
	saved := c.run("--home", "phone", "download", "--dir", "phone-download", message)
	got, e := os.ReadFile(filepath.Join(c.dir, saved))
	if e != nil || !bytes.Equal(got, data) {
		t.Fatalf("future encrypted file bytes: %v", e)
	}
	if strings.Contains(dmShow(c, "carol", conv), "ordinary file after explicit departure") {
		t.Fatal("departed person received future plaintext")
	}
	packet = decodePacket("bob", "rename", conv, "Withdrawal folded into current membership")
	if _, ok := packet.State.Member(personIDs["carol"]); ok {
		t.Fatal("next CLI CAS did not fold departure")
	}
	waitCurrent(packet, "phone", "alice")
	packet = decodePacket("bob", "remove", conv, personIDs["alice"])
	if _, ok := packet.State.Member(personIDs["alice"]); ok {
		t.Fatal("exact CLI remove kept member")
	}
}

// Observe the disposable stores and signed profiles before process cleanup.
// This does not repair or retry the journey, and prints no message, identity,
// envelope, session identifier, or free-form error text.
func groupGovernanceFailureDetails(t *testing.T, c *cli, addr, cert, conv string) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cert)) {
		t.Log("governance diagnostic certificate unavailable")
		return
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}, Proxy: nil}
	defer tr.CloseIdleConnections()
	h := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	for _, home := range []string{"alice", "phone"} {
		path := filepath.Join(c.dir, home, "agent.db")
		for _, line := range groupHistoryStateSummary(path, conv) {
			t.Logf("governance %s %s", home, line)
		}
		func() {
			db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(100)")
			if err != nil {
				t.Logf("governance %s extra database metadata unavailable", home)
				return
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			for _, q := range []struct{ label, sql string }{
				{"carrier", `SELECT sub||':'||state||':cap='||coalesce(required_cap,'')||':error='||CASE WHEN coalesce(error,'')='' THEN 'none' WHEN error LIKE 'peer_update:%' THEN 'peer-update' WHEN error LIKE 'server_update:%' OR error LIKE 'server_unavailable:%' THEN 'server' ELSE 'present' END,count(*) FROM outbox WHERE conv=? AND sub IN ('group-proof','group-context') GROUP BY 1 ORDER BY 1 LIMIT 20`},
				{"quarantine-all", `SELECT reason||':code='||detail_code,count(*) FROM quarantine WHERE ?<>'' GROUP BY reason,detail_code ORDER BY reason,detail_code LIMIT 20`},
			} {
				rows, err := db.QueryContext(ctx, q.sql, conv)
				if err != nil {
					t.Logf("governance %s %s unavailable", home, q.label)
					continue
				}
				found := false
				for rows.Next() {
					var state string
					var count int
					if err = rows.Scan(&state, &count); err != nil {
						break
					}
					found = true
					t.Logf("governance %s %s %s count=%d", home, q.label, state, count)
				}
				if err != nil || rows.Err() != nil {
					t.Logf("governance %s %s unavailable", home, q.label)
				} else if !found {
					t.Logf("governance %s %s count=0", home, q.label)
				}
				rows.Close()
			}
		}()
		func() {
			id, err := identity.Load(filepath.Join(c.dir, home, "identity.json"))
			if err != nil {
				t.Logf("governance %s profile unavailable", home)
				return
			}
			address := "admin/phone"
			if home == "alice" {
				address = "admin/laptop"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "GET", "https://"+addr+"/v1/agents/"+address+"/profile", nil)
			if err != nil {
				t.Logf("governance %s profile unavailable", home)
				return
			}
			protocol.SignRequest(req, address, id.Sign, nil)
			res, err := h.Do(req)
			if err != nil {
				t.Logf("governance %s profile unavailable", home)
				return
			}
			defer res.Body.Close()
			var p protocol.Profile
			if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&p) != nil {
				t.Logf("governance %s profile unavailable status=%d", home, res.StatusCode)
				return
			}
			public := id.Public(address)
			t.Logf("governance %s profile live=%t sessions=%d grp1=%t", home, p.Live, len(p.Sessions), p.Supports(address, public.SignKey, protocol.CapGroup))
			for i, session := range p.Sessions {
				for _, raw := range p.Caps {
					record, err := protocol.ParseCapsRecord(raw)
					if err == nil && record.Address == address && record.Session == session && record.Verify(public.SignKey) == nil {
						t.Logf("governance %s profile session-index=%d signed-grp1=%t caps-ts=%d", home, i, record.Reads(protocol.CapGroup), record.TS)
					}
				}
			}
		}()
	}
}
