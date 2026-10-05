//go:build linux

package ui

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	staticfiles "github.com/misunders2d/agentnet/internal/ui/static"
)

// Original signed native ciphertext is delivered to the actual Engine. Fetch
// fixtures are synthetic; neither this test nor the browser runs a harness.
func groupEngineVectors(t *testing.T, setup map[string]any) (map[string]any, func(string) map[string]any) {
	t.Helper()
	var browser identity.Public
	var br protocol.PersonRoster
	if json.Unmarshal([]byte(setup["public"].(string)), &browser) != nil || browser.Verify() != nil {
		t.Fatal("browser public")
	}
	if json.Unmarshal([]byte(setup["roster"].(string)), &br) != nil || br.VerifyFirst() != nil {
		t.Fatal("browser roster")
	}
	alice, _ := identity.Generate()
	bob, _ := identity.Generate()
	ap, bp := alice.Public("alice/desk"), bob.Public("bob/desk")
	ar := protocol.PersonRoster{Person: strings.Repeat("a", 32), Label: "Alice", Devices: []identity.Public{ap}}
	ar.Sign(alice.Sign)
	rr := protocol.PersonRoster{Person: strings.Repeat("b", 32), Label: "Bob", Devices: []identity.Public{bp}}
	rr.Sign(bob.Sign)
	rosters := []protocol.PersonRoster{ar, rr, br}
	resolve := func(p, h string) (protocol.PersonRoster, bool) {
		for _, r := range rosters {
			if r.Person == p && r.Hash() == h {
				return r, true
			}
		}
		return protocol.PersonRoster{}, false
	}
	root := protocol.ConvRoot{V: 3, Kind: "group", Creator: protocol.ConvCreator{Person: ar.Person, Roster: ar.Hash(), Address: ap.Address, Fingerprint: ap.Fingerprint()}, Nonce: protocol.NewID(), Created: 1700000000, Realm: protocol.NewID(), Title: "Private group", Admins: []string{ar.Person}}
	for _, r := range rosters {
		root.Members = append(root.Members, protocol.ConvMember{Person: r.Person, Roster: r.Hash()})
	}
	slices.SortFunc(root.Members, func(a, b protocol.ConvMember) int { return strings.Compare(a.Person, b.Person) })
	root.Sign(alice.Sign)
	key, _ := x509.MarshalPKCS8PrivateKey(alice.Sign)
	challenge := map[string]any{"root": root, "rosters": rosters, "alice_private": base64.StdEncoding.EncodeToString(key)}
	return challenge, func(consent string) map[string]any {
		s0 := protocol.GroupState{V: 1, Conv: root.ID(), Realm: root.Realm, Title: root.Title, Actor: ar.Person, ActorRoster: ar.Hash(), By: ap.Fingerprint()}
		var ba protocol.GroupAdmission
		if json.Unmarshal([]byte(consent), &ba) != nil {
			t.Fatal("browser admission")
		}
		for _, m := range root.Members {
			r, _ := resolve(m.Person, m.Roster)
			a := protocol.GroupAdmission{Conv: s0.Conv, Realm: s0.Realm, Person: m.Person, Roster: m.Roster, By: r.Devices[0].Fingerprint()}
			if m.Person == ar.Person {
				a.Sign(alice.Sign)
			} else if m.Person == rr.Person {
				a.Sign(bob.Sign)
			} else {
				a = ba
			}
			s0.Members = append(s0.Members, protocol.GroupMember{ConvMember: m, Admin: m.Person == ar.Person, Admission: a})
		}
		s0.Sign(alice.Sign)
		if err := s0.Verify(root, nil, resolve, nil); err != nil {
			t.Fatal(err)
		}
		states := []protocol.GroupState{s0}
		commits := []protocol.GroupCommit{}
		encrypt := func(raw []byte) []byte {
			recipient, _ := browser.Recipient()
			var b bytes.Buffer
			w, err := age.Encrypt(&b, recipient)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			return b.Bytes()
		}
		for i := 1; i <= 5; i++ {
			s := states[i-1]
			s.Members = slices.Clone(s.Members)
			s.Seq = int64(i)
			s.Prev = states[i-1].Hash()
			s.Title = "Snapshot " + strings.Repeat("x", i)
			if i == 3 {
				for j := range s.Members {
					if s.Members[j].Person == rr.Person {
						s.Members[j].Admin = true
					}
				}
			}
			if i == 4 {
				for j := range s.Members {
					if s.Members[j].Person == rr.Person {
						s.Members[j].Admin = false
					}
				}
			}
			if i == 5 {
				s.Members = slices.DeleteFunc(s.Members, func(m protocol.GroupMember) bool { return m.Person == br.Person })
			}
			s.Sign(alice.Sign)
			if err := s.Verify(root, &states[i-1], resolve, nil); err != nil {
				t.Fatal(err)
			}
			states = append(states, s)
		}
		for _, s := range states {
			c := protocol.GroupCommit{Bootstrap: root.Creator.Fingerprint, V: 1, Conv: s.Conv, Realm: s.Realm, Seq: s.Seq, Prev: s.Prev, Hash: s.Hash(), Admins: s.Admins(), Writer: ap.Address, Actor: s.Actor, ActorRoster: s.ActorRoster, Ciphertext: encrypt([]byte(marshal(t, client.GroupContext{Root: root, State: s})))}
			c.Sign(alice.Sign)
			var prev *protocol.GroupCommit
			if len(commits) > 0 {
				prev = &commits[len(commits)-1]
			}
			if err := c.VerifyChain(root, prev, resolve); err != nil {
				t.Fatal(err)
			}
			commits = append(commits, c)
		}
		for i, s := range states {
			if err := s.VerifyCurrent(root, commits[i], resolve, func(seq int64) (protocol.GroupCommit, bool) {
				if seq < 0 || seq >= int64(len(commits)) {
					return protocol.GroupCommit{}, false
				}
				return commits[seq], true
			}, nil); err != nil {
				t.Fatal("native current authority", err)
			}
		}
		bm, _ := s0.Member(rr.Person)
		withdrawal := protocol.GroupWithdrawal{Conv: s0.Conv, Realm: s0.Realm, Person: rr.Person, Admission: bm.Admission.Hash(), Roster: rr.Hash(), By: bp.Fingerprint()}
		withdrawal.Sign(bob.Sign)
		if err := withdrawal.Verify(s0, resolve); err != nil {
			t.Fatal("native ordinary withdrawal", err)
		}
		vectors := map[string]any{"challenge": challenge, "commits": commits, "states": states, "withdrawal": withdrawal, "carriers": map[string]any{}}
		// Public signed sessions for the receiver-only group transition fixture.
		// Existing proof/context/participation vectors retain their exact bytes.
		profiles := map[string]any{}
		for _, source := range []struct {
			id     *identity.Identity
			public identity.Public
		}{{alice, ap}, {bob, bp}} {
			session := protocol.NewID()
			caps := protocol.CapsRecord{Address: source.public.Address, Session: session, TS: 1700000000, Caps: []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapGroup, protocol.CapAgentIdentity, protocol.CapExternalParticipation, protocol.CapReplyReceiver}}
			slices.Sort(caps.Caps)
			caps.Sign(source.id.Sign)
			profiles[source.public.Address] = map[string]any{"live": true, "sessions": []string{session}, "caps": []protocol.CapsRecord{caps}}
		}
		vectors["receiver_profiles"] = profiles
		proposal := protocol.GroupInvitation{V: 1, Root: root, State: s0, Target: br.Person, Roster: br.Hash(), Seq: 1, Prev: s0.Hash()}
		if err := proposal.Validate(); err != nil {
			t.Fatal(err)
		}
		vectors["invitation_json"], vectors["invitation_id"] = marshal(t, proposal), proposal.ID()
		vectors["decline_json"] = marshal(t, protocol.GroupConsent{V: 1, Invitation: proposal.ID(), Decision: "declined"})
		accepted := protocol.GroupAdmission{Conv: s0.Conv, Realm: root.Realm, Person: rr.Person, Roster: rr.Hash(), Seq: proposal.Seq, Prev: proposal.Prev, History: []protocol.GroupHistoryRef{{LID: protocol.NewID(), Author: bp.Fingerprint(), Hash: strings.Repeat("c", 64)}}, By: bp.Fingerprint()}
		accepted.Sign(bob.Sign)
		vectors["accept_json"] = marshal(t, protocol.GroupConsent{V: 1, Invitation: proposal.ID(), Decision: "accepted", Admission: &accepted})
		item := client.HistoryItem{V: 1, ID: strings.Repeat("4", 32), LID: strings.Repeat("2", 32), From: "alice/desk", FromKey: ap.Fingerprint(), TS: 10, Kind: "message", Body: "selected <text> & file", ReplyTo: strings.Repeat("3", 32), Attachments: []envelope.Attachment{{Name: "first.bin", Size: 7, SHA256: strings.Repeat("a", 64)}, {Name: "second.bin", Size: 9, SHA256: strings.Repeat("b", 64)}}, GroupAdmission: strings.Repeat("d", 64)}
		vectors["live_history_json"] = marshal(t, item)
		vectors["history_hash"] = "884f5cee25a5695a6e50c942c896790e8a0f328de405ac67cd075ce589480ab0" // native TestGroupHistoryCanonicalSelectionVector
		carriers := vectors["carriers"].(map[string]any)
		carrier := func(sub string, payload any, seq int64, hash string, signer ...*identity.Identity) map[string]any {
			who, from := alice, ap
			if len(signer) > 0 {
				who = signer[0]
				from = who.Public("dana/desk")
				if who == bob {
					from = bp
				}
			}
			raw := []byte(marshal(t, payload))
			ct := encrypt(raw)
			sum := sha256.Sum256(raw)
			cs := sha256.Sum256(ct)
			id := protocol.NewID()
			att := envelope.Attachment{Name: sub + ".json", Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:]), Blob: envelope.Blob{ID: protocol.NewID(), Size: int64(len(ct)), SHA256: hex.EncodeToString(cs[:])}}
			desc := protocol.GroupCarrier{V: 1, Seq: seq, Hash: hash, ToKey: browser.Fingerprint()}
			in := envelope.Inner{V: 2, ID: id, From: from.Address, To: browser.Address, TS: 1700000000, Kind: envelope.KindMessage, Conv: root.ID(), LID: protocol.NewID(), Root: json.RawMessage(marshal(t, root)), Sub: sub, Body: marshal(t, desc), Attachments: []envelope.Attachment{att}}
			recipient, _ := browser.Recipient()
			env, err := envelope.Seal(in, who.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
			return map[string]any{"envelope": marshal(t, env), "ct": base64.StdEncoding.EncodeToString(ct), "blob": att.Blob.ID}
		}
		dana, _ := identity.Generate()
		dr := protocol.PersonRoster{Person: strings.Repeat("d", 32), Label: "Dana", Devices: []identity.Public{dana.Public("dana/desk")}}
		dr.Sign(dana.Sign)
		newProposal := proposal
		newProposal.Target, newProposal.Roster = dr.Person, dr.Hash()
		newProposal.History = accepted.History
		newAdmission := protocol.GroupAdmission{Conv: s0.Conv, Realm: root.Realm, Person: dr.Person, Roster: dr.Hash(), Seq: newProposal.Seq, Prev: newProposal.Prev, History: newProposal.History, By: dr.Devices[0].Fingerprint()}
		newAdmission.Sign(dana.Sign)
		newConsent := protocol.GroupConsent{V: 1, Invitation: newProposal.ID(), Decision: "accepted", Admission: &newAdmission}
		vectors["invited_roster"], vectors["new_proposal"], vectors["new_consent"] = dr, newProposal, newConsent
		danaSession := protocol.NewID()
		danaCaps := protocol.CapsRecord{Address: dr.Devices[0].Address, Session: danaSession, TS: 1700000000, Caps: []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapGroup, protocol.CapAgentIdentity, protocol.CapExternalParticipation, protocol.CapReplyReceiver}}
		slices.Sort(danaCaps.Caps)
		danaCaps.Sign(dana.Sign)
		profiles[dr.Devices[0].Address] = map[string]any{"live": true, "sessions": []string{danaSession}, "caps": []protocol.CapsRecord{danaCaps}}

		carriers["new-consent"] = carrier(envelope.SubGroupConsent, newConsent, newProposal.Seq, newProposal.Prev, dana)
		badAdmission := newAdmission
		badAdmission.Sig = slices.Clone(newAdmission.Sig)
		badAdmission.Sig[0] ^= 1
		badConsent := newConsent
		badConsent.Admission = &badAdmission
		carriers["bad-consent-signature"] = carrier(envelope.SubGroupConsent, badConsent, newProposal.Seq, newProposal.Prev, dana)
		for i, s := range states {
			carriers["proof"+strings.Repeat("x", i)] = carrier(envelope.SubGroupProof, protocol.GroupJournalPage{Records: []protocol.GroupCommit{commits[i]}}, s.Seq, s.Hash())
			carriers["context"+strings.Repeat("x", i)] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: s}, s.Seq, s.Hash())
		}
		carriers["withdrawn"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: s0, Withdrawals: []protocol.GroupWithdrawal{withdrawal}}, 0, s0.Hash())
		carriers["promoted-leave"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: states[3], Withdrawals: []protocol.GroupWithdrawal{withdrawal}}, 3, states[3].Hash())
		participations := groupParticipationEngineVectors(t, root, s0, alice, dana, ar, dr, browser)
		vectors["participations"] = participations
		var membershipRecords []protocol.ParticipationEvent
		for _, prefix := range []string{"p6-0", "p6-1"} {
			for _, typ := range []string{"invite", "scope", "accept"} {
				in := participations[prefix+"-"+typ].(map[string]any)["inner"].(envelope.Inner)
				ev, err := protocol.ParseParticipationEvent([]byte(in.Body))
				if err != nil {
					t.Fatal(err)
				}
				membershipRecords = append(membershipRecords, ev)
			}
		}
		carriers["p6-memberships"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: s0, Memberships: membershipRecords}, s0.Seq, s0.Hash())
		carriers["p6-nonadmin-memberships"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: s0, Memberships: membershipRecords}, s0.Seq, s0.Hash(), bob)
		carriers["p6-outsider-memberships"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: s0, Memberships: membershipRecords}, s0.Seq, s0.Hash(), dana)
		// Bob held admin at slot 3, then lost it at slot 4 without a new admission.
		backdated := membershipRecords[3]
		currentBob, _ := states[4].Member(rr.Person)
		backdated.PID = protocol.NewID()
		backdated.Author = protocol.EventAuthor{Person: rr.Person, Roster: rr.Hash(), Address: bp.Address, Fingerprint: bp.Fingerprint(), GroupAdmission: currentBob.Admission.Hash()}
		backdated.Group = &protocol.ParticipationGroup{Seq: 3, Hash: states[3].Hash(), HostRole: "visitor"}
		backdated.Sign(bob.Sign)
		backdatedScope := protocol.ScopeOf(backdated, backdated.TS)
		backdatedScope.Sign(bob.Sign)
		backdatedAccept := membershipRecords[5]
		backdatedAccept.PID, backdatedAccept.Prev = backdated.PID, backdated.Hash()
		backdatedAccept.Sign(dana.Sign)
		vectors["p6_backdated"] = []protocol.ParticipationEvent{backdated, backdatedScope, backdatedAccept}
		carriers["p6-backdated-memberships"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: states[4], Memberships: []protocol.ParticipationEvent{backdated, backdatedScope, backdatedAccept}}, 4, states[4].Hash())
		bad := slices.Clone(membershipRecords)
		bad[0].Sig = slices.Clone(bad[0].Sig)
		bad[0].Sig[0] ^= 1
		carriers["p6-bad-memberships"] = carrier(envelope.SubGroupContext, client.GroupContext{Root: root, State: s0, Memberships: bad}, s0.Seq, s0.Hash())
		vectors["outside_browser"] = groupOutsideBrowserVectors(t, root, alice, bob, ar, rr, browser, br)
		return vectors
	}
}

func TestBrowserGroupCarrierEngineMatchesGo(t *testing.T) { testBrowserGroupCarrierEngine(t, "checks") }
func TestBrowserGroupWarmRecovery(t *testing.T)           { testBrowserGroupCarrierEngine(t, "warm-regression") }
func testBrowserGroupCarrierEngine(t *testing.T, op string) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node missing")
	}
	cmd := exec.Command(node, "testdata/group_engine_check.mjs")
	w := &wireNode{t: t, stderr: &bytes.Buffer{}}
	cmd.Stderr = w.stderr
	w.in, err = cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.in.Close(); cmd.Wait() })
	w.out = bufio.NewScanner(out)
	w.out.Buffer(make([]byte, 4096), 16<<20)
	challenge, finish := groupEngineVectors(t, w.ok(map[string]any{"op": "setup"}))
	consent := w.ok(map[string]any{"op": "consent", "challenge": challenge})["consent"].(string)
	result := w.ok(map[string]any{"op": op, "vectors": finish(consent)})
	if result["ok"] != true {
		t.Fatal(result)
	}
	t.Logf("%v checks (%v)", result["checks"], result["storage"])
}

func TestBrowserGroupCarrierRealIndexedDB(t *testing.T) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME not set")
	}
	var finish func(string) map[string]any
	done := make(chan map[string]any, 1)
	assets := http.FileServerFS(staticfiles.Files)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<script type="module">import {setup,consent,checks} from '/testdata/group_engine_check.mjs';let out;try {const post=async(path,body)=>{const r=await fetch(path,{method:'POST',body:JSON.stringify(body)});return r.json()};const challenge=await post('/setup',await setup());const v=await post('/consent',{consent:await consent(challenge)});out=await checks(v,true);}catch(e){out={error:e.stack}}await fetch('/result',{method:'POST',body:JSON.stringify(out)});</script>`)
		case "/testdata/group_engine_check.mjs":
			w.Header().Set("Content-Type", "text/javascript")
			http.ServeFile(w, r, "testdata/group_engine_check.mjs")
		case "/setup":
			var setup map[string]any
			if json.NewDecoder(r.Body).Decode(&setup) != nil {
				t.Error("bad setup")
				return
			}
			challenge, f := groupEngineVectors(t, setup)
			finish = f
			json.NewEncoder(w).Encode(challenge)
		case "/consent":
			var x struct {
				Consent string `json:"consent"`
			}
			json.NewDecoder(r.Body).Decode(&x)
			json.NewEncoder(w).Encode(finish(x.Consent))
		case "/result":
			var result map[string]any
			json.NewDecoder(r.Body).Decode(&result)
			done <- result
		default:
			http.StripPrefix("/static", assets).ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	profile := t.TempDir()
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--disable-background-networking", "--no-first-run", "--no-default-browser-check", "--disable-sync", "--user-data-dir="+filepath.Join(profile, "chrome"), srv.URL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "HOME="+profile, "XDG_CONFIG_HOME="+filepath.Join(profile, "config"), "XDG_CACHE_HOME="+filepath.Join(profile, "cache"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		waited := make(chan struct{})
		go func() { cmd.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-time.After(5 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waited
		}
		for i := 0; i < 50 && syscall.Kill(-cmd.Process.Pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if err := syscall.Kill(-cmd.Process.Pid, 0); err == nil {
			t.Error("owned Chrome process group remains")
		}
	}()
	select {
	case result := <-done:
		if result["ok"] != true {
			t.Fatalf("real IDB: %v", result)
		}
		t.Logf("%v checks (%v)", result["checks"], result["storage"])
	case <-time.After(90 * time.Second):
		t.Fatalf("real IDB timeout: %s", stderr.String())
	}
}
