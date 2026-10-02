package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryAttachmentManifestDirectionAndOrder(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	for _, dir := range []string{"in", "out"} {
		for index, name := range []string{"z.txt", "a.txt"} {
			blob := dir + name
			if dir == "in" {
				_, err = s.db.Exec(`INSERT INTO attachments(message_id,blob_id,name,size,sha256,ct_size,ct_sha256) VALUES(?,?,?,?,?,0,'')`, "same-physical-id", blob, name, index+1, dir+name)
			} else {
				_, err = s.db.Exec(`INSERT INTO sent_attachments(message_id,blob_id,name,size,sha256) VALUES(?,?,?,?,?)`, "same-physical-id", blob, name, index+1, dir+name)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, dir := range []string{"in", "out"} {
		files, err := s.attachmentManifest("same-physical-id", dir)
		if err != nil || len(files) != 2 || files[0].Name != "z.txt" || files[1].Name != "a.txt" || files[0].SHA256 != dir+"z.txt" || files[1].SHA256 != dir+"a.txt" {
			t.Fatalf("%s manifest %+v: %v", dir, files, err)
		}
	}
}

func TestGroupParticipationFilesLateLinkedRequestAndNamedOutput(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, stops := groupTurnsFixture(t)
	fakeNotify(w.alice)
	fakeNotify(w.bob)
	record, e := w.bob.CreateLocalAgent("named file reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.bob.PublishAgentCatalog(tctx(t)); e != nil {
		t.Fatal(e)
	}
	parts := []ParticipationInfo{}
	for range 2 {
		p, e := w.alice.InviteNamedAgent(tctx(t), packet.State.Conv, w.bob.Address, record.ID, nil, []string{w.alice.Self().Fingerprint()}, "exact PID file scope")
		if e != nil {
			t.Fatal(e)
		}
		eventually(t, "file PID invited", func() bool { v, e := w.bob.Participation(p.PID); return e == nil && v.State == PartInvited })
		if _, e = w.bob.AcceptParticipation(tctx(t), p.PID); e != nil {
			t.Fatal(e)
		}
		eventually(t, "file PID active", func() bool { v, e := w.alice.Participation(p.PID); return e == nil && v.Claimable() })
		parts = append(parts, p)
	}
	stops[w.bob]() // signed manual outputs below, never a model/task execution
	dir := t.TempDir()
	requestFiles, outputFiles := []OutgoingFile{}, []OutgoingFile{}
	for _, name := range []string{"z.txt", "a.txt"} {
		path := filepath.Join(dir, "request-"+name)
		if e = os.WriteFile(path, []byte("PID_REQUEST_"+name), 0600); e != nil {
			t.Fatal(e)
		}
		requestFiles = append(requestFiles, OutgoingFile{Path: path, Name: name})
		path = filepath.Join(dir, "output-"+name)
		if e = os.WriteFile(path, []byte("PID_OUTPUT_"+name), 0600); e != nil {
			t.Fatal(e)
		}
		outputFiles = append(outputFiles, OutgoingFile{Path: path, Name: name})
	}
	refs := map[string]string{}
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		request, e := w.alice.AskAgent(tctx(t), parts[0].PID, kind, "exact attached request", requestFiles...)
		if e != nil {
			t.Fatal(e)
		}
		groupGovernanceDeliver(t, w.alice, w.bob, groupTurnEnvelope(t, w.alice, request.ID))
		refs[kind] = request.LID
		outKind := envelope.KindAnswer
		if kind == envelope.KindTask {
			outKind = envelope.KindResult
		}
		output, e := w.bob.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Kind: outKind, Body: "exact named file output", PID: parts[0].PID, AgentID: record.ID, ReplyTo: request.ID, Files: outputFiles, Origin: envelope.OriginAgentPrefix + "agentstub", Emotion: "calm", status: envelope.StatusDone})
		if e != nil {
			t.Fatal(e)
		}
		refs[outKind] = output.LID
		replyAt(t, w.alice, packet.State.Conv, request.ID)
	}
	other, e := w.alice.AskAgent(tctx(t), parts[1].PID, envelope.KindQuestion, "exact attached request", requestFiles...)
	if e != nil {
		t.Fatal(e)
	}
	// Link after all request/output bytes existed. Existing own-history exports
	// carry manifests/proof only, and file recovery must use the same holder.
	phone, await, _ := linkPhone(t, w.alice, "pid-file-phone")
	link := pendingLink(t, w.alice)
	stops[w.alice]()
	if e = w.alice.DecideLink(tctx(t), link.ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	copies, e := w.alice.groupDeliveryCopies(tctx(t), packet)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); e != nil {
		t.Fatal(e)
	}
	for _, c := range copies {
		if c.env.To == phone.Address {
			groupGovernanceDeliver(t, w.alice, phone, c.env)
		}
	}
	if _, e = w.alice.historyPageFor(phone.Self(), historyPos{}); e != nil {
		t.Fatal(e)
	}
	rows, e := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='history' ORDER BY rowid`, phone.Address)
	if e != nil {
		t.Fatal(e)
	}
	var histories []envelope.Envelope
	for rows.Next() {
		var raw []byte
		var env envelope.Envelope
		if e = rows.Scan(&raw); e != nil {
			break
		}
		if e = json.Unmarshal(raw, &env); e != nil {
			break
		}
		histories = append(histories, env)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	for _, env := range histories {
		if e = phone.accept(tctx(t), env); e != nil {
			t.Fatal(e)
		}
		phone.retryProof(tctx(t))
	}
	phone.retryProof(tctx(t))
	oldSession := w.alice.session
	runAgent(t, w.alice)
	runAgent(t, phone)
	eventually(t, "fresh own holder session", func() bool {
		label, name, _ := protocol.SplitAddress(w.alice.Address)
		var profile protocol.Profile
		if w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile) != nil {
			return false
		}
		for _, session := range profile.Sessions {
			if session != oldSession {
				return true
			}
		}
		return false
	})
	publishGroupFixtureCaps(t, w.alice, true)
	publishGroupFixtureCaps(t, phone, true)
	var positive fileMsg
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask, envelope.KindAnswer, envelope.KindResult} {
		var id string
		if e = phone.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND pid=? AND kind=?`, packet.State.Conv, refs[kind], parts[0].PID, kind).Scan(&id); e != nil {
			t.Fatal(e)
		}
		for index, name := range []string{"z.txt", "a.txt"} {
			if e = phone.RequestFile(tctx(t), id, index); e != nil {
				t.Fatalf("%s index%d: %v", kind, index, e)
			}
			eventually(t, "late PID file offer", func() bool {
				files, e := phone.store.attachments(id)
				return e == nil && len(files) == 2 && !strings.HasPrefix(files[index].BlobID, historyBlob)
			})
			f, _, e := phone.OpenFileFrom(tctx(t), "in", id, index)
			if e != nil {
				t.Fatal(e)
			}
			data := make([]byte, 128)
			n, e := f.Read(data)
			f.Close()
			want := "PID_REQUEST_" + name
			if kind == envelope.KindAnswer || kind == envelope.KindResult {
				want = "PID_OUTPUT_" + name
			}
			if e != nil || string(data[:n]) != want {
				t.Fatalf("%s[%d] bytes%q %v", kind, index, data[:n], e)
			}
		}
		if kind == envelope.KindQuestion {
			sources, e := phone.groupParticipationFileSources(phone.store.db, packet.State.Conv, refs[kind], w.alice.Self().Fingerprint(), 3)
			if e != nil || len(sources) != 1 {
				t.Fatalf("source %+v %v", sources, e)
			}
			ref := historyRef(packet.State.Conv, sources[0].item)
			size := sources[0].item.Attachments[0].Size
			index := 0
			positive = fileMsg{V: 1, Type: "request", LID: ref.LID, Author: ref.Author, Hash: ref.Hash, Index: &index, Name: "z.txt", Size: &size, SHA256: sources[0].item.Attachments[0].SHA256, GroupAdmission: sources[0].stamp}
		}
	}
	if e = w.alice.groupFileAuthorized(w.alice.store.db, packet, phone.Address, phone.Self().Fingerprint(), positive); e != nil {
		t.Fatal(e)
	}
	for _, variant := range []string{"manifest", "cross_pid", "admission"} {
		wrong := positive
		switch variant {
		case "manifest":
			wrong.Name = "a.txt"
		case "cross_pid":
			wrong.LID = other.LID
		case "admission":
			wrong.GroupAdmission = strings.Repeat("a", 64)
		}
		if e = w.alice.groupFileAuthorized(w.alice.store.db, packet, phone.Address, phone.Self().Fingerprint(), wrong); e == nil {
			t.Fatalf("wrong %s got PID file authority", variant)
		}
	}
	outside := proofReader(t, w, "file-visitor")
	runAgent(t, outside)
	sibling, wait, _ := linkPhone(t, outside, "sibling")
	if e = outside.DecideLink(tctx(t), pendingLink(t, outside).ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-wait; out.err != nil {
		t.Fatal(out.err)
	}
	if e = w.alice.groupFileAuthorized(w.alice.store.db, packet, sibling.Address, sibling.Self().Fingerprint(), positive); e == nil {
		t.Fatal("visitor sibling got room PID file")
	}
	if e = w.alice.groupFileAuthorized(w.alice.store.db, packet, w.bob.Address, w.bob.Self().Fingerprint(), positive); e == nil {
		t.Fatal("another current person got own PID file")
	}
	if n := inboxCount(t, phone, `conv=? AND state IN ('agent-waiting','running','done','task-waiting')`, packet.State.Conv); n != 0 || stub.runs() != 0 {
		t.Fatalf("history recovery created work%d runs%d", n, stub.runs())
	}
	person, _, _ := w.bob.store.selfPerson(w.bob.Address)
	next, e := w.alice.RemoveGroupMember(tctx(t), packet.State.Conv, person.roster.Person)
	if e != nil {
		t.Fatal(e)
	}
	next = groupInteractionRejoin(t, w.alice, w.bob, next)
	if e = w.alice.groupFileAuthorized(w.alice.store.db, next, phone.Address, phone.Self().Fingerprint(), positive); e == nil {
		t.Fatal("rejoined host revived old PID file transfer")
	}
}
