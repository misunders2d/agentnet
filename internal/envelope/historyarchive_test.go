package envelope

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func archiveTestInner(t *testing.T) Inner {
	t.Helper()
	body, _ := json.Marshal(protocol.HistoryArchive{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Count: 1, Format: protocol.HistoryArchiveFormat})
	return Inner{V: Version2, ID: protocol.NewID(), From: "owner/host", To: "owner/phone", TS: 1, Kind: KindMessage, Sub: SubHistoryArchive, Replica: true, Body: string(body), Attachments: []Attachment{{Name: "history.json", Size: 100, SHA256: strings.Repeat("b", 64), Blob: Blob{ID: protocol.NewID(), Size: 200, SHA256: strings.Repeat("c", 64)}}}}
}

func TestHistoryArchiveQuietCarrier(t *testing.T) {
	in := archiveTestInner(t)
	if err := checkVersion2(in); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Inner){
		"missing attachment": func(n *Inner) { n.Attachments = nil },
		"two attachments":    func(n *Inner) { n.Attachments = append(n.Attachments, n.Attachments[0]) },
		"oversized attachment": func(n *Inner) {
			n.Attachments = append([]Attachment(nil), n.Attachments...)
			n.Attachments[0].Size = protocol.MaxHistoryArchivePlaintext + 1
		},
		"empty attachment": func(n *Inner) { n.Attachments = append([]Attachment(nil), n.Attachments...); n.Attachments[0].Size = 0 },
		"session":          func(n *Inner) { n.Session = protocol.NewID() },
		"fallback":         func(n *Inner) { n.Fallback = true },
		"group":            func(n *Inner) { n.SendGroup = protocol.NewID() },
		"kind":             func(n *Inner) { n.Kind = KindTask },
		"target":           func(n *Inner) { n.Target = &Target{Address: n.To} },
		"agent":            func(n *Inner) { n.AgentID = protocol.NewID() },
		"reply":            func(n *Inner) { n.ReplyTo = protocol.NewID() },
		"origin":           func(n *Inner) { n.Origin = "ui" },
		"status":           func(n *Inner) { n.Status = StatusDone },
		"replica":          func(n *Inner) { n.Replica = false },
		"topic":            func(n *Inner) { n.Topic = protocol.NewID() },
		"pid":              func(n *Inner) { n.PID = protocol.NewID() },
		"root":             func(n *Inner) { n.Root = json.RawMessage(`{}`) },
		"quote":            func(n *Inner) { n.Quote = protocol.NewID() },
	} {
		t.Run(name, func(t *testing.T) {
			n := in
			change(&n)
			if checkVersion2(n) == nil {
				t.Fatal("archive accepted another message's fields")
			}
		})
	}
	from, to := newParty(t, in.From), newParty(t, in.To)
	recipient, _ := to.pub.Recipient()
	e, err := Seal(in, from.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(e, to.id, in.To, from.pub); err != nil {
		t.Fatal(err)
	}
	if _, err := SealAttention(in, from.id.Sign, recipient, protocol.NewID()); err == nil {
		t.Fatal("archive requests attention")
	}
	e.Attn, e.Chan = true, protocol.NewID()
	e.Sig = ed25519.Sign(from.id.Sign, e.signed())
	if _, err := Open(e, to.id, in.To, from.pub); err == nil {
		t.Fatal("signed archive attention accepted")
	}
}

func archiveTestChunk() HistoryArchiveChunk {
	ct := []byte("structural ciphertext")
	sum := sha256.Sum256(ct)
	b := Blob{ID: protocol.NewID(), Size: int64(len(ct)), SHA256: hex.EncodeToString(sum[:])}
	e := Envelope{V: Version2, ID: protocol.NewID(), From: "owner/host", To: "owner/phone", TS: 1, Kind: KindMessage, CT: []byte("encrypted child"), Sig: make([]byte, ed25519.SignatureSize), Blobs: []Blob{b}}
	return HistoryArchiveChunk{V: 1, Entries: []HistoryArchiveEntry{{Envelope: e, Blobs: []HistoryArchiveBlob{{ID: b.ID, CT: ct}}}}}
}

func TestHistoryArchiveChunkBoundsAndBlobs(t *testing.T) {
	r := archiveTestChunk()
	data, err := MarshalHistoryArchiveChunk(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHistoryArchiveChunk(data); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*HistoryArchiveChunk){
		"version":         func(r *HistoryArchiveChunk) { r.V = 2 },
		"empty":           func(r *HistoryArchiveChunk) { r.Entries = nil },
		"duplicate child": func(r *HistoryArchiveChunk) { r.Entries = append(r.Entries, r.Entries[0]) },
		"too many": func(r *HistoryArchiveChunk) {
			for i := 0; i < protocol.MaxHistoryArchiveEntries; i++ {
				e := r.Entries[0]
				e.Envelope.ID = protocol.NewID()
				r.Entries = append(r.Entries, e)
			}
		},
		"unreferenced blob": func(r *HistoryArchiveChunk) { r.Entries[0].Blobs[0].ID = protocol.NewID() },
		"digest":            func(r *HistoryArchiveChunk) { r.Entries[0].Blobs[0].CT = []byte("wrong ciphertext") },
		"duplicate blob":    func(r *HistoryArchiveChunk) { r.Entries[0].Blobs = append(r.Entries[0].Blobs, r.Entries[0].Blobs[0]) },
		"duplicate ref": func(r *HistoryArchiveChunk) {
			r.Entries[0].Envelope.Blobs = append(r.Entries[0].Envelope.Blobs, r.Entries[0].Envelope.Blobs[0])
		},
		"attention":    func(r *HistoryArchiveChunk) { r.Entries[0].Envelope.Attn = true },
		"session":      func(r *HistoryArchiveChunk) { r.Entries[0].Envelope.Session = protocol.NewID() },
		"live request": func(r *HistoryArchiveChunk) { r.Entries[0].Envelope.Kind = KindQuestion },
		"child bound":  func(r *HistoryArchiveChunk) { r.Entries[0].Envelope.CT = make([]byte, MaxCiphertext+1) },
	} {
		t.Run(name, func(t *testing.T) {
			n := archiveTestChunk()
			change(&n)
			data, _ := json.Marshal(n)
			if _, err := ParseHistoryArchiveChunk(data); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
	for _, invalid := range [][]byte{append(data, []byte(` {}`)...), append(data[:len(data)-1], []byte(`,"extra":true}`)...), []byte(strings.Repeat(" ", protocol.MaxHistoryArchivePlaintext+1)), {0xff}} {
		if _, err := ParseHistoryArchiveChunk(invalid); err == nil {
			t.Fatal("non-strict chunk accepted")
		}
	}
	// Signed references to ordinary historical files may remain lazy.
	r.Entries[0].Blobs = nil
	lazy, err := MarshalHistoryArchiveChunk(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(lazy), `"blobs":[]`) || r.Entries[0].Blobs != nil {
		t.Fatal("marshal must emit an empty array without mutating its input")
	}
	if _, err := ParseHistoryArchiveChunk(lazy); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseHistoryArchiveChunk([]byte(strings.Replace(string(lazy), `"blobs":[]`, `"blobs":null`, 1))); err == nil {
		t.Fatal("null embedded blobs accepted")
	}
}

func TestHistoryArchiveGoBrowserSignedChunk(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	from, to := newParty(t, "owner/host"), newParty(t, "owner/phone")
	body, _ := json.Marshal(protocol.DeviceHistory{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Recipient: "owner/host", Item: json.RawMessage(`{}`)})
	in := Inner{V: Version2, ID: protocol.NewID(), From: "owner/host", To: "owner/phone", TS: 1, Kind: KindMessage, Sub: SubDeviceHistory, Replica: true, Body: string(body)}
	recipient, _ := to.pub.Recipient()
	e, err := Seal(in, from.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(e, to.id, in.To, from.pub); err != nil {
		t.Fatal(err)
	}
	chunk, err := MarshalHistoryArchiveChunk(HistoryArchiveChunk{V: 1, Entries: []HistoryArchiveEntry{{Envelope: e}}})
	if err != nil {
		t.Fatal(err)
	}
	fixture, _ := json.Marshal(struct {
		Chunk json.RawMessage `json:"chunk"`
		Key   []byte          `json:"key"`
	}{Chunk: chunk, Key: from.pub.SignKey})
	module, err := filepath.Abs("../ui/static/historyarchive.mjs")
	if err != nil {
		t.Fatal(err)
	}
	script := `
import {webcrypto} from 'node:crypto';
import {pathToFileURL} from 'node:url';
globalThis.crypto ||= webcrypto;
const archiveURL=pathToFileURL(process.argv[1]);
const {parseArchiveChunk}=await import(archiveURL);
const wire=await import(new URL('./wire.mjs',archiveURL));
const parts=[];for await(const part of process.stdin)parts.push(part);
const fixture=JSON.parse(Buffer.concat(parts).toString('utf8'));
const chunk=await parseArchiveChunk(new TextEncoder().encode(JSON.stringify(fixture.chunk)),1);
if(!Array.isArray(chunk.entries[0].blobs)||chunk.entries[0].blobs.length)throw Error('Empty blobs interoperability');
await wire.verifyEnvelope(wire.parseEnvelope(chunk.entries[0].envelope),Buffer.from(fixture.key,'base64'));
`
	cmd := exec.Command(node, "--input-type=module", "-e", script, module)
	cmd.Stdin = strings.NewReader(string(fixture))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Go to browser signed archive: %v\n%s", err, output)
	}
}

func TestHistoryArchiveChildInertAllowlist(t *testing.T) {
	for _, sub := range []string{"", SubHistoryArchive, SubGroupInvite, SubGroupConsent, SubReaction, SubDecision, SubInvitationSync, SubFile} {
		if ValidateHistoryArchiveChild(Inner{V: Version2, Kind: KindMessage, Sub: sub}) == nil {
			t.Fatalf("accepted child subtype %q", sub)
		}
	}
	body, _ := json.Marshal(protocol.DeviceHistory{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Recipient: "owner/host", Item: json.RawMessage(`{}`)})
	in := Inner{V: Version2, Kind: KindMessage, Sub: SubDeviceHistory, Replica: true, Body: string(body)}
	if err := ValidateHistoryArchiveChild(in); err != nil {
		t.Fatal(err)
	}
	in.Kind = KindTask
	if ValidateHistoryArchiveChild(in) == nil {
		t.Fatal("archive child may execute")
	}
	history := Inner{V: Version2, Kind: KindMessage, Sub: SubHistory, Replica: true, Conv: testConv, LID: testLID, Root: json.RawMessage(`{"v":1}`)}
	if err := ValidateHistoryArchiveChild(history); err != nil {
		t.Fatal(err)
	}
	history.Replica = false
	if ValidateHistoryArchiveChild(history) == nil {
		t.Fatal("history child is not a replica")
	}
	f := newRoomFixture(t)
	carrier, _ := json.Marshal(protocol.GroupCarrier{V: 1, Hash: strings.Repeat("a", 64), ToKey: "01234567-89abcdef-01234567-89abcdef"})
	for _, sub := range []string{SubGroupProof, SubGroupContext} {
		proof := Inner{V: Version2, Kind: KindMessage, Sub: sub, PID: protocol.NewID(), Conv: f.group.ID(), LID: protocol.NewID(), Root: f.gRaw, Body: string(carrier), Attachments: []Attachment{{Name: sub + ".json", Size: 10, Blob: Blob{Size: 100}}}}
		if err := ValidateHistoryArchiveChild(proof); err != nil {
			t.Fatalf("valid PID-bound %s rejected: %v", sub, err)
		}
	}
}
