package ui

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Explicit synthetic native sender, not worker artifact collection. The real
// worker report remains separate; this existing SendConv API supplies files
// solely for the late-linked browser's recovery acceptance fixture.
func TestGroupLateBrowserSignedFileOutputFixture(t *testing.T) {
	input := os.Getenv("AGENTNET_GROUP_OUTPUT_FIXTURE")
	if input == "" {
		t.Skip("isolated rendered fixture only")
	}
	var request struct {
		Home, Conv, PID, AgentID, RequestID, Kind string
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	world := os.Getenv("AGENTNET_GROUP_WORLD")
	if world == "" || !strings.HasPrefix(request.Home, world+string(os.PathSeparator)) || !strings.HasPrefix(input, world+string(os.PathSeparator)) {
		t.Fatal("fixture requires its exact private world")
	}
	if request.Kind != envelope.KindAnswer && request.Kind != envelope.KindResult {
		t.Fatal("fixture only signs named output")
	}
	a, err := client.Open(request.Home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	var files []client.OutgoingFile
	for _, name := range []string{"z.txt", "a.txt"} {
		p := filepath.Join(world, "signed-output-"+request.Kind+"-"+name)
		if err = os.WriteFile(p, []byte("PID_SYNTHETIC_"+request.Kind+"_"+name+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		files = append(files, client.OutgoingFile{Path: p, Name: name})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := a.SendConv(ctx, request.Conv, client.ConvOutgoing{Kind: request.Kind, Body: "Synthetic signed native " + request.Kind + " with files", PID: request.PID, AgentID: request.AgentID, ReplyTo: request.RequestID, Files: files, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(result)
	if err == nil {
		err = os.WriteFile(input+".result.json", raw, 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func groupOutsideBrowserVectors(t *testing.T, old protocol.ConvRoot, alice, bob *identity.Identity, ar, br protocol.PersonRoster, browser identity.Public, browserRoster protocol.PersonRoster) map[string]any {
	t.Helper()
	root := old
	root.Nonce, root.Members = protocol.NewID(), []protocol.ConvMember{{Person: ar.Person, Roster: ar.Hash()}, {Person: br.Person, Roster: br.Hash()}}
	root.Sign(alice.Sign)
	state := protocol.GroupState{V: 1, Conv: root.ID(), Realm: root.Realm, Title: root.Title, Actor: ar.Person, ActorRoster: ar.Hash(), By: ar.Devices[0].Fingerprint()}
	for _, r := range []protocol.PersonRoster{ar, br} {
		adm := protocol.GroupAdmission{Conv: root.ID(), Realm: root.Realm, Person: r.Person, Roster: r.Hash(), By: r.Devices[0].Fingerprint()}
		if r.Person == ar.Person {
			adm.Sign(alice.Sign)
		} else {
			adm.Sign(bob.Sign)
		}
		state.Members = append(state.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: r.Person, Roster: r.Hash()}, Admin: r.Person == ar.Person, Admission: adm})
	}
	state.Sign(alice.Sign)
	packet := client.GroupContext{Root: root, State: state}
	encrypt := func(raw []byte, public identity.Public) []byte {
		key, _ := public.Recipient()
		var b bytes.Buffer
		w, e := age.Encrypt(&b, key)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write(raw); e != nil {
			t.Fatal(e)
		}
		if e = w.Close(); e != nil {
			t.Fatal(e)
		}
		return b.Bytes()
	}
	commit := protocol.GroupCommit{V: 1, Conv: root.ID(), Realm: root.Realm, Bootstrap: root.Creator.Fingerprint, Seq: 0, Hash: state.Hash(), Admins: state.Admins(), Writer: ar.Devices[0].Address, Actor: state.Actor, ActorRoster: state.ActorRoster, Ciphertext: encrypt([]byte(marshal(t, packet)), ar.Devices[0])}
	commit.Sign(alice.Sign)
	am, _ := state.Member(ar.Person)
	ev := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: protocol.NewID(), Type: protocol.EventInvite, TS: 1700000100, Author: protocol.EventAuthor{Person: ar.Person, Roster: ar.Hash(), Address: ar.Devices[0].Address, Fingerprint: ar.Devices[0].Fingerprint(), GroupAdmission: am.Admission.Hash()}, Host: &protocol.ParticipationHost{Person: browserRoster.Person, Address: browser.Address, Fingerprint: browser.Fingerprint(), AgentID: protocol.NewID()}, Audience: protocol.AudienceConversation, Group: &protocol.ParticipationGroup{Seq: 0, Hash: state.Hash(), HostRole: "visitor"}}
	ev.Sign(alice.Sign)
	out := map[string]any{"root": root, "state": state, "pid": ev.PID, "commit": commit}
	seal := func(name string, in envelope.Inner) {
		in.V, in.ID, in.LID, in.Conv, in.Root, in.From, in.To, in.TS = 2, protocol.NewID(), protocol.NewID(), root.ID(), json.RawMessage(marshal(t, root)), ar.Devices[0].Address, browser.Address, 1700000100
		key, _ := browser.Recipient()
		env, err := envelope.Seal(in, alice.Sign, key)
		if err != nil {
			t.Fatal(name, err)
		}
		out[name] = map[string]any{"envelope": marshal(t, env), "inner": in}
	}
	seal("invite", envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: ev.PID, Body: marshal(t, ev)})
	// Native PID copies to another device of the inviter retain event Replica=false;
	// only a request addressed to a different host is a display-only Replica.
	ownEvent := ev
	ownEvent.PID = protocol.NewID()
	ownEvent.Host = &protocol.ParticipationHost{Person: ar.Person, Address: ar.Devices[0].Address, Fingerprint: ar.Devices[0].Fingerprint(), AgentID: protocol.NewID()}
	ownEvent.Group = &protocol.ParticipationGroup{Seq: 0, Hash: state.Hash(), HostRole: "member", HostAdmission: am.Admission.Hash()}
	ownEvent.Sign(alice.Sign)
	seal("own-invite", envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: ownEvent.PID, Body: marshal(t, ownEvent)})
	ownDecision := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: ownEvent.PID, Type: protocol.EventAccept, Prev: ownEvent.Hash(), TS: 1700000100, Author: ownEvent.Author}
	ownDecision.Sign(alice.Sign)
	seal("own-accept", envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: ownEvent.PID, Body: marshal(t, ownDecision)})
	seal("own-question", envelope.Inner{Kind: envelope.KindQuestion, PID: ownEvent.PID, Body: "Exact own linked request replica", Origin: "ui", Replica: true, Target: &envelope.Target{Address: ownEvent.Host.Address, Fingerprint: ownEvent.Host.Fingerprint, AgentID: ownEvent.Host.AgentID, GroupAdmission: am.Admission.Hash()}})
	out["own-pid"] = ownEvent.PID
	for _, sub := range []string{envelope.SubGroupProof, envelope.SubGroupContext} {
		var payload any = packet
		if sub == envelope.SubGroupProof {
			payload = protocol.GroupJournalPage{Records: []protocol.GroupCommit{commit}}
		}
		raw := []byte(marshal(t, payload))
		ct := encrypt(raw, browser)
		sum, cs := sha256.Sum256(raw), sha256.Sum256(ct)
		att := envelope.Attachment{Name: sub + ".json", Size: int64(len(raw)), SHA256: hex.EncodeToString(sum[:]), Blob: envelope.Blob{ID: protocol.NewID(), Size: int64(len(ct)), SHA256: hex.EncodeToString(cs[:])}}
		desc := protocol.GroupCarrier{V: 1, Seq: 0, Hash: state.Hash(), ToKey: browser.Fingerprint()}
		seal(sub, envelope.Inner{Kind: envelope.KindMessage, Sub: sub, PID: ev.PID, Body: marshal(t, desc), Attachments: []envelope.Attachment{att}})
		seal("own-"+sub, envelope.Inner{Kind: envelope.KindMessage, Sub: sub, Body: marshal(t, desc), Attachments: []envelope.Attachment{att}})
		out[sub+"-file"] = map[string]any{"blob": att.Blob.ID, "ct": base64.StdEncoding.EncodeToString(ct)}
	}
	selectedBytes := []byte("OWN_LINKED_SELECTED_BYTES")
	selectedCipher := encrypt(selectedBytes, browser)
	selectedSum, selectedCipherSum := sha256.Sum256(selectedBytes), sha256.Sum256(selectedCipher)
	selectedAttachment := envelope.Attachment{Name: "own-selected.txt", Size: int64(len(selectedBytes)), SHA256: hex.EncodeToString(selectedSum[:]), Blob: envelope.Blob{ID: protocol.NewID(), Size: int64(len(selectedCipher)), SHA256: hex.EncodeToString(selectedCipherSum[:])}}
	seal("own-ordinary", envelope.Inner{Kind: envelope.KindMessage, Body: "Original own linked selected context", Origin: "ui", Replica: true, Attachments: []envelope.Attachment{selectedAttachment}})
	out["own-ordinary-file"] = map[string]any{"blob": selectedAttachment.Blob.ID, "ct": base64.StdEncoding.EncodeToString(selectedCipher), "bytes": string(selectedBytes)}
	seal("ordinary", envelope.Inner{Kind: envelope.KindMessage, Body: "Foreign ordinary room plaintext", Origin: "ui"})
	return out
}

// Real native signed envelopes consumed by the actual Engine. Deterministic
// fixture keys/transport are private synthetic setup, never a running model.
func groupParticipationEngineVectors(t *testing.T, root protocol.ConvRoot, state protocol.GroupState, alice, dana *identity.Identity, ar, dr protocol.PersonRoster, browser identity.Public) map[string]any {
	t.Helper()
	result := map[string]any{}
	fileVectors := []map[string]any{}
	ap, dp := ar.Devices[0], dr.Devices[0]
	am, _ := state.Member(ar.Person)
	seal := func(name string, in envelope.Inner, who *identity.Identity, sender identity.Public) {
		in.ID, in.From, in.To, in.TS = protocol.NewID(), sender.Address, browser.Address, 1700000100
		if in.V == 0 {
			in.V = envelope.Version2
		}
		if in.LID == "" {
			in.LID = protocol.NewID()
		}
		in.Conv = root.ID()
		if in.V == envelope.Version2 {
			in.Root = []byte(marshal(t, root))
		}
		key, _ := browser.Recipient()
		if name == "member-question" {
			for _, filename := range []string{"z.txt", "a.txt"} {
				data := []byte("GROUP_PID_EXACT_" + filename)
				var encrypted bytes.Buffer
				writer, e := age.Encrypt(&encrypted, key)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = writer.Write(data); e != nil {
					t.Fatal(e)
				}
				if e = writer.Close(); e != nil {
					t.Fatal(e)
				}
				digest, cipherDigest := sha256.Sum256(data), sha256.Sum256(encrypted.Bytes())
				att := envelope.Attachment{Name: filename, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:]), Blob: envelope.Blob{ID: protocol.NewID(), Size: int64(encrypted.Len()), SHA256: hex.EncodeToString(cipherDigest[:])}}
				in.Attachments = append(in.Attachments, att)
				fileVectors = append(fileVectors, map[string]any{"blob": att.Blob.ID, "ct": base64.StdEncoding.EncodeToString(encrypted.Bytes()), "bytes": string(data), "name": filename})
			}
		}
		ev, err := envelope.Seal(in, who.Sign, key)
		if err != nil {
			t.Fatal(name, err)
		}
		result[name] = map[string]any{"envelope": marshal(t, ev), "inner": in}
		for _, member := range state.Members {
			if member.Admission.By == browser.Fingerprint() {
				var manifests []envelope.Attachment
				for _, f := range in.Attachments {
					manifests = append(manifests, envelope.Attachment{Name: f.Name, Size: f.Size, SHA256: f.SHA256})
				}
				result["history-"+name] = marshal(t, client.HistoryItem{V: 1, From: in.From, FromKey: sender.Fingerprint(), ID: in.ID, LID: in.LID, TS: in.TS, Kind: in.Kind, Body: in.Body, ReplyTo: in.ReplyTo, Status: in.Status, Sub: in.Sub, Origin: in.Origin, Target: in.Target, PID: in.PID, AgentID: in.AgentID, Ref: in.Ref, At: in.TS * 1000, GroupAdmission: member.Admission.Hash(), Attachments: manifests})
			}
		}
	}
	for _, role := range []string{"member", "visitor"} {
		host, signer, person := ap, alice, ar
		if role == "visitor" {
			host, signer, person = dp, dana, dr
		}
		pid, agent := protocol.NewID(), protocol.NewID()
		ev := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: pid, Type: protocol.EventInvite, TS: 1700000100,
			Author: protocol.EventAuthor{Person: ar.Person, Roster: ar.Hash(), Address: ap.Address, Fingerprint: ap.Fingerprint(), GroupAdmission: am.Admission.Hash()},
			Host:   &protocol.ParticipationHost{Person: person.Person, Address: host.Address, Fingerprint: host.Fingerprint(), AgentID: agent}, Audience: protocol.AudienceConversation,
			TaskKeys: []string{ap.Fingerprint()}, Group: &protocol.ParticipationGroup{Seq: state.Seq, Hash: state.Hash(), HostRole: role, TaskAdmissions: []string{am.Admission.Hash()}}}
		if role == "member" {
			ev.Group.HostAdmission = am.Admission.Hash()
		}
		ev.Sign(alice.Sign)
		seal(role+"-invite", envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: pid, Body: marshal(t, ev)}, alice, ap)
		decision := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: pid, Type: protocol.EventAccept, Prev: ev.Hash(), TS: 1700000100, Author: protocol.EventAuthor{Person: person.Person, Roster: person.Hash(), Address: host.Address, Fingerprint: host.Fingerprint()}}
		if role == "member" {
			decision.Author.GroupAdmission = am.Admission.Hash()
		}
		decision.Sign(signer.Sign)
		seal(role+"-accept", envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: pid, Body: marshal(t, decision)}, signer, host)
		requestLID := protocol.NewID()
		target := &envelope.Target{Address: host.Address, Fingerprint: host.Fingerprint(), AgentID: agent, GroupAdmission: am.Admission.Hash()}
		seal(role+"-question", envelope.Inner{Kind: envelope.KindQuestion, PID: pid, LID: requestLID, Body: "Exact " + role + " original question", Origin: "ui", Target: target}, alice, ap)
		seal(role+"-answer", envelope.Inner{Kind: envelope.KindAnswer, PID: pid, ReplyTo: requestLID, Body: "Exact " + role + " answer", AgentID: agent}, signer, host)
		seal(role+"-wrong-agent", envelope.Inner{Kind: envelope.KindAnswer, PID: pid, ReplyTo: requestLID, Body: "Forged named output", AgentID: protocol.NewID()}, signer, host)
		seal(role+"-status", envelope.Inner{V: envelope.Version3, Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: `{"state":"running","n":1,"at":1700000100,"detail":"Synthetic executor status"}`, Ref: &envelope.Ref{ID: requestLID, Fingerprint: ap.Fingerprint()}}, signer, host)
		dismiss := protocol.ParticipationEvent{V: 1, Conv: root.ID(), PID: pid, Type: protocol.EventDismiss, Prev: decision.Hash(), TS: 1700000100, Author: ev.Author}
		dismiss.Sign(alice.Sign)
		seal(role+"-dismiss", envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: pid, Body: marshal(t, dismiss)}, alice, ap)
		result[role+"-pid"], result[role+"-agent"] = pid, agent
	}
	ordinaryLID := protocol.NewID()
	seal("ordinary", envelope.Inner{Kind: envelope.KindMessage, LID: ordinaryLID, Body: "Exact ordinary original", Origin: "ui"}, alice, ap)
	for _, sub := range []string{envelope.SubReaction, envelope.SubRevision, envelope.SubRetraction} {
		body := `{}`
		if sub == envelope.SubReaction {
			body = `{"emoji":"👍","op":"add","n":1}`
		} else if sub == envelope.SubRevision {
			body = `{"rev":1,"text":"Exact ordinary revised"}`
		}
		seal("ordinary-"+sub, envelope.Inner{V: envelope.Version3, Kind: envelope.KindMessage, Sub: sub, Body: body, Ref: &envelope.Ref{ID: ordinaryLID, Fingerprint: ap.Fingerprint()}}, alice, ap)
		seal("visitor-"+sub, envelope.Inner{V: envelope.Version3, Kind: envelope.KindMessage, Sub: sub, Body: body, Ref: &envelope.Ref{ID: ordinaryLID, Fingerprint: ap.Fingerprint()}}, dana, dp)
	}
	result["files"] = fileVectors
	return result
}

func TestBrowserGroupParticipationWireMatchesGo(t *testing.T) {
	w := startAgentWireNode(t)
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	const person = "0123456789abcdef0123456789abcdef"
	const fp = "01234567-89abcdef-01234567-89abcdef"
	for _, role := range []string{"member", "visitor"} {
		e := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("1", 64), PID: person, Type: protocol.EventInvite, TS: 1790000000,
			Author:   protocol.EventAuthor{Person: person, Roster: strings.Repeat("2", 64), Address: "alice/laptop", Fingerprint: fp, GroupAdmission: strings.Repeat("a", 64)},
			Host:     &protocol.ParticipationHost{Person: "fedcba9876543210fedcba9876543210", Address: "bob/desk", Fingerprint: fp, AgentID: person},
			Audience: protocol.AudienceConversation, TaskKeys: []string{fp}, Note: "Exact <&> group scope",
			Group: &protocol.ParticipationGroup{Seq: 7, Hash: strings.Repeat("b", 64), HostRole: role, TaskAdmissions: []string{strings.Repeat("d", 64)}}}
		if role == "member" {
			e.Group.HostAdmission = strings.Repeat("c", 64)
		}
		e.Sign(key)
		if err := e.Verify(key.Public().(ed25519.PublicKey)); err != nil {
			t.Fatal(err)
		}
		req := func(event protocol.ParticipationEvent) map[string]any {
			return map[string]any{"op": "event", "json": marshal(t, event), "key": base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}
		}
		v := w.ok(req(e))
		if v["hash"] != e.Hash() || v["json"] != marshal(t, e) {
			t.Fatal("group event canonical bytes differ from Go")
		}
		changed := e
		changed.Author.GroupAdmission = strings.Repeat("e", 64)
		w.refuses("changed signed author epoch", w.call(req(changed)), "signature")
		changed = e
		g := *e.Group
		g.TaskAdmissions = nil
		changed.Group = &g
		w.refuses("missing exact task admission", w.call(req(changed)), "group invitation")
		g = *e.Group
		g.HostRole = "unknown"
		changed.Group = &g
		w.refuses("unknown host role", w.call(req(changed)), "group invitation")
		changed = e
		changed.Type, changed.Prev, changed.Host, changed.Audience, changed.TaskKeys, changed.Note = protocol.EventAccept, e.Hash(), nil, "", nil, ""
		w.refuses("group scope on a decision", w.call(req(changed)), "only the event")
		changed.Group = nil
		changed.Author.GroupAdmission = ""
		changed.Sign(key)
		v = w.ok(req(changed))
		if v["hash"] != changed.Hash() || v["json"] != marshal(t, changed) {
			t.Fatal("outside host decision canonical bytes differ")
		}
	}
}
