package client

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func groupOrdinaryPublication(t *testing.T) (*world, GroupContext) {
	t.Helper()
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	next := p
	next.Proof = nil
	next.State.Seq = 1
	next.State.Prev = p.State.Hash()
	next.State.Members = slices.Clone(p.State.Members)
	bob, _, _ := w.bob.store.selfPerson(w.bob.Address)
	for i := range next.State.Members {
		if next.State.Members[i].Person == bob.roster.Person {
			next.State.Members[i].Admin = false
		}
	}
	var err error
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	return w, next
}

func publishGroupFixtureCaps(t *testing.T, a *Agent, group bool) {
	t.Helper()
	waitNamedAgentCaps(t, a)
	label, name, _ := protocol.SplitAddress(a.Address)
	var profile protocol.Profile
	if err := a.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	caps := slices.DeleteFunc(slices.Clone(ownCaps), func(cap string) bool { return cap == protocol.CapGroup })
	if group {
		caps = append(caps, protocol.CapGroup)
	}
	slices.Sort(caps)
	for _, session := range profile.Sessions {
		ts := time.Now().Unix() + 100
		if group {
			ts += 100
		}
		rec := protocol.CapsRecord{Address: a.Address, Session: session, Caps: caps, TS: ts}
		rec.Sign(a.id.Sign)
		if err := a.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func groupFixturePayload(t *testing.T, a, recipient *Agent, root protocol.ConvRoot, sub string, descriptor protocol.GroupCarrier, value any, wrongDigest bool) envelope.Envelope {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path, cleanup, err := a.StageUpload(sub+".json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	key, err := recipient.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	att, err := a.spoolNamed(OutgoingFile{Path: path, Name: sub + ".json"}, key)
	if err != nil {
		t.Fatal(err)
	}
	if wrongDigest {
		att.SHA256 = strings.Repeat("f", 64)
	}
	descriptor.ToKey = recipient.Self().Fingerprint()
	body, _ := json.Marshal(descriptor)
	rootRaw, _ := json.Marshal(root)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: a.Address, To: recipient.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Conv: root.ID(), Root: rootRaw, Sub: sub, Body: string(body), Attachments: []envelope.Attachment{att}}
	env, err := envelope.Seal(in, a.id.Sign, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.addConvOutbox([]outCopy{{in: in, env: env, state: stateQueued, required: protocol.CapGroup}}, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestGroupCarrierCapabilityAndDigestFailClosed(t *testing.T) {
	w, p, c := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), c, p); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, false)
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var held int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE required_cap=? AND state=?`, protocol.CapGroup, stateConvWaiting).Scan(&held)
	if held != 2 {
		t.Fatalf("unsupported carrier downgraded/failed: %d", held)
	}
	if _, err := w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("unsupported context installed %v", err)
	}
	publishGroupFixtureCaps(t, w.bob, true)
	bad := groupFixturePayload(t, w.alice, w.bob, p.Root, envelope.SubGroupProof, protocol.GroupCarrier{V: 1, Seq: 0, Hash: c.Hash}, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c}}, true)
	if _, err := w.alice.deliver(tctx(t), bad, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "signed wrong plaintext digest quarantined", func() bool {
		var reason string
		e := w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, bad.ID).Scan(&reason)
		return e == nil && reason == reasonInvalid
	})
	if _, err := w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("bad digest installed context %v", err)
	}
	features, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.alice.releaseConv(tctx(t), features)
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "unsupported original batch recovers after grp1", func() bool {
		got, e := w.bob.GroupContext(p.State.Conv)
		return e == nil && got.State.Hash() == p.State.Hash()
	})
}

func TestGroupCarrierLatestHeadWithdrawalAndAtomicRecipient(t *testing.T) {
	t.Run("latest observed head", func(t *testing.T) {
		w, p := groupOrdinaryPublication(t)
		if err := w.alice.NoteGroupHead(protocol.GroupHead{Conv: p.State.Conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: p.State.Seq + 1, Hash: strings.Repeat("e", 64)}); err != nil {
			t.Fatal(err)
		}
		for _, env := range groupQueuedCarriers(t, w.alice) {
			handled, allowed, err := w.alice.mayDeliverGroup(env)
			if !handled || allowed || err != nil {
				t.Fatalf("newer head allowed delivery %v %v %v", handled, allowed, err)
			}
		}
	})
	t.Run("withdrawal before retry", func(t *testing.T) {
		w, p, withdrawal := ordinaryWithdrawalFixture(t)
		if err := w.alice.AcceptGroupWithdrawal(tctx(t), withdrawal); err != nil {
			t.Fatal(err)
		}
		for _, env := range groupQueuedCarriers(t, w.alice) {
			if env.To != w.bob.Address {
				continue
			}
			handled, allowed, err := w.alice.mayDeliverGroup(env)
			if !handled || allowed || err != nil {
				t.Fatalf("withdrawn recipient allowed delivery %v %v %v", handled, allowed, err)
			}
		}
		var leaked int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND recipient=? AND state=?`, p.State.Conv, w.bob.Address, stateQueued).Scan(&leaked)
		if leaked != 0 {
			t.Fatalf("withdrawn copies retained deliverable %d", leaked)
		}
	})
	t.Run("atomic exact recipient recheck", func(t *testing.T) {
		w, p, c := newGroupPublicationFixture(t)
		beforeOutbox = func() {
			w.alice.store.db.Exec(`UPDATE persons SET state=? WHERE person=(SELECT person FROM person_devices WHERE address=?)`, personConflict, w.bob.Address)
		}
		t.Cleanup(func() { beforeOutbox = func() {} })
		if _, err := w.alice.PublishGroup(tctx(t), c, p); err == nil {
			t.Fatal("frozen recipient admitted at transaction boundary")
		}
		beforeOutbox = func() {}
		var published, rows int
		w.alice.store.db.QueryRow(`SELECT published FROM group_publications WHERE conv=?`, p.State.Conv).Scan(&published)
		w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=?`, p.State.Conv).Scan(&rows)
		if published != 0 || rows != 0 {
			t.Fatalf("recipient recheck was not atomic %d %d", published, rows)
		}
		tx, err := w.alice.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err = groupDeliveryRecipient(tx, p, w.bob.Address, strings.Repeat("a", 64)); err == nil {
			t.Fatal("wrong recipient key admitted")
		}
	})
}

func TestGroupCarrierLinkedKeyRemovalAndNoOldKeys(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	stop := runAgent(t, w.alice)
	phone, awaited, _ := linkPhone(t, w.alice, "group-carrier-phone")
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	stop()
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	next := p
	next.Proof = nil
	next.State.Seq = 1
	next.State.Prev = p.State.Hash()
	next.State.Title = "Linked current"
	var err error
	next, err = w.alice.SignGroupState(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	commit, err := w.alice.BuildGroupCommit(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.PublishGroup(tctx(t), commit, next); err != nil {
		t.Fatal(err)
	}
	var phoneCopies []envelope.Envelope
	for _, env := range groupQueuedCarriers(t, w.alice) {
		if env.To == phone.Address {
			phoneCopies = append(phoneCopies, env)
			if _, err = w.alice.deliver(tctx(t), env, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	eventually(t, "new linked key receives current-only carrier", func() bool {
		got, e := phone.GroupContext(p.State.Conv)
		return e == nil && got.State.Hash() == next.State.Hash() && len(got.Proof) == 0
	})
	if _, err = phone.DecodeGroupCommit(tctx(t), first); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("carrier disclosed old encryption key %v", err)
	}
	// A new exact-key batch queued before removal is refused at retry.
	copies, err := w.alice.groupDeliveryCopies(tctx(t), next)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, func(tx *sql.Tx, _ string) error {
		for _, copy := range copies {
			var desc protocol.GroupCarrier
			json.Unmarshal([]byte(copy.in.Body), &desc)
			if e := groupDeliveryRecipient(tx, next, copy.env.To, desc.ToKey); e != nil {
				return e
			}
		}
		return nil
	}, ""); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	for _, copy := range copies {
		if copy.env.To != phone.Address {
			continue
		}
		handled, allowed, e := w.alice.mayDeliverGroup(copy.env)
		if !handled || allowed || e != nil {
			t.Fatalf("removed exact key delivered %v %v %v", handled, allowed, e)
		}
	}
	if len(phoneCopies) != 2 {
		t.Fatalf("linked delivery batch %d", len(phoneCopies))
	}
}

// The original public proof may legitimately exceed an envelope after JSON
// base64 encoding; it remains opaque ciphertext and travels in an age blob.
func TestGroupCarrierLargePublicProofAndBlobRetry(t *testing.T) {
	w, p, c := newGroupPublicationFixture(t)
	// Accepted legacy records can include their original plaintext proof inside
	// age ciphertext. Construct a valid signed chain, never arbitrary padding.
	records := []protocol.GroupCommit{c}
	states := []protocol.GroupState{p.State}
	legacy := p
	for {
		prev := states[len(states)-1]
		next := prev
		next.Seq++
		next.Prev = prev.Hash()
		next.Sign(w.alice.id.Sign)
		legacy = GroupContext{Root: p.Root, Proof: slices.Clone(states), State: next}
		encoded, _ := json.Marshal(legacy)
		if len(encoded) > 205<<10 {
			break
		}
		records = append(records, sealedCurrentProof(t, w.alice, w.bob, GroupContext{Root: p.Root, State: next}))
		states = append(states, next)
	}
	resolve, err := w.alice.groupResolver(tctx(t), legacy)
	if err != nil || VerifyGroupContext(legacy, resolve) != nil {
		t.Fatalf("invalid large legacy fixture %v", err)
	}
	large := sealedCurrentProof(t, w.alice, w.bob, legacy)
	if err := large.Validate(); err != nil {
		t.Fatal(err)
	}
	for start := 0; start < len(records); start += 16 {
		if err := w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: records[start:min(start+16, len(records))]}); err != nil {
			t.Fatal(err)
		}
	}
	page := protocol.GroupJournalPage{Records: []protocol.GroupCommit{large}}
	raw, _ := json.Marshal(page)
	if len(raw) <= envelope.MaxCiphertext {
		t.Fatal("fixture did not exceed envelope bound")
	}
	env := groupFixturePayload(t, w.alice, w.bob, p.Root, envelope.SubGroupProof, protocol.GroupCarrier{V: 1, Seq: large.Seq, Hash: large.Hash}, page, false)
	if err := w.alice.uploadAll(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
		t.Fatal(err)
	}
	// Resume from a real existing encrypted partial file, then refuse one
	// transient range request without acknowledging the carrier.
	ciphertext, err := os.ReadFile(w.alice.spoolPath(env.Blobs[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	part := w.bob.downloadPath(env.Blobs[0].ID) + ".part"
	if err = os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(part, ciphertext[:4096], 0600); err != nil {
		t.Fatal(err)
	}
	faults := injectFaults(w.bob)
	faults.add("GET", "/v1/blobs/", 1, false)
	if err := w.bob.accept(tctx(t), env); err == nil {
		t.Fatal("interrupted blob receive acknowledged")
	}
	var stored int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, env.ID).Scan(&stored)
	if stored != 0 {
		t.Fatal("interrupted carrier recorded as received")
	}
	if info, e := os.Stat(part); e != nil || info.Size() != 4096 {
		t.Fatalf("encrypted partial lost: %v", e)
	}
	if err := w.bob.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	original, err := groupProofRecord(w.bob.store.db, p.State.Conv, p.Root.Creator.Fingerprint, large.Seq)
	if err != nil || !bytes.Equal(original.Ciphertext, large.Ciphertext) {
		t.Fatalf("large original proof altered %v", err)
	}
	if _, err = w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("opaque proof granted current membership %v", err)
	}
	decoded, err := w.bob.DecodeGroupCommit(tctx(t), large)
	if err != nil || decoded.State.Hash() != legacy.State.Hash() {
		t.Fatalf("original large ciphertext not a valid context: %v", err)
	}
	if _, err = os.Stat(part); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed encrypted partial retained %v", err)
	}
}

func groupQueuedCarriers(t *testing.T, a *Agent) []envelope.Envelope {
	t.Helper()
	all, err := a.store.queued()
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func TestGroupCarrierCiphertextTamperAndOrdinaryRedownload(t *testing.T) {
	w, p, c := newGroupPublicationFixture(t)
	env := groupFixturePayload(t, w.alice, w.bob, p.Root, envelope.SubGroupProof, protocol.GroupCarrier{V: 1, Seq: 0, Hash: c.Hash}, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c}}, false)
	if err := w.alice.uploadAll(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
		t.Fatal(err)
	}
	blob := env.Blobs[0]
	path := filepath.Join(w.hub.Dir, "blobs", blob.ID+".blob")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bad := slices.Clone(original)
	bad[len(bad)/2] ^= 1
	if err = os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err = w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, env.ID).Scan(&reason); err != nil || reason != reasonInvalid {
		t.Fatalf("signed ciphertext tamper not refused %q %v", reason, err)
	}
	if _, err = w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("tampered ciphertext granted membership %v", err)
	}
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	f := FileInfo{BlobID: blob.ID, ctSize: blob.Size, ctSHA256: blob.SHA256}
	part := w.bob.downloadPath(blob.ID) + ".part"
	if err = os.WriteFile(part, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if err = w.bob.fetchCiphertext(tctx(t), f); !errors.Is(err, errFileCiphertextIntegrity) || errors.Is(err, errPermanent) {
		t.Fatalf("ordinary corrupt partial classification %v", err)
	}
	if err = w.bob.fetchCiphertext(tctx(t), f); err != nil {
		t.Fatalf("ordinary redownload failed %v", err)
	}
	got, err := os.ReadFile(w.bob.downloadPath(blob.ID))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("ordinary redownload differs %v", err)
	}
}

func TestGroupCarrierMalformedAuthorityAndContext(t *testing.T) {
	for _, name := range []string{"signature", "realm", "context root", "context signature", "recipient key"} {
		t.Run(name, func(t *testing.T) {
			w, p, c := newGroupPublicationFixture(t)
			sub := envelope.SubGroupProof
			var value any
			if strings.HasPrefix(name, "context") {
				if err := w.bob.IngestGroupProofPage(tctx(t), p.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{c}}); err != nil {
					t.Fatal(err)
				}
				sub = envelope.SubGroupContext
				bad := p
				if name == "context root" {
					bad.Root.Nonce = protocol.NewID()
				} else {
					bad.State.Sig = bytes.Repeat([]byte{0}, len(p.State.Sig))
				}
				value = bad
			} else {
				bad := c
				if name == "signature" {
					bad.Sig = bytes.Repeat([]byte{0}, len(c.Sig))
				} else if name == "realm" {
					bad.Realm = protocol.NewID()
					bad.Sign(w.alice.id.Sign)
				}
				value = protocol.GroupJournalPage{Records: []protocol.GroupCommit{bad}}
			}
			env := groupFixturePayload(t, w.alice, w.bob, p.Root, sub, protocol.GroupCarrier{V: 1, Seq: c.Seq, Hash: c.Hash}, value, false)
			if name == "recipient key" {
				in, err := envelope.Open(env, w.bob.id, w.bob.Address, w.alice.Self())
				if err != nil {
					t.Fatal(err)
				}
				descriptor := protocol.GroupCarrier{V: 1, Seq: c.Seq, Hash: c.Hash, ToKey: w.alice.Self().Fingerprint()}
				raw, _ := json.Marshal(descriptor)
				in.Body = string(raw)
				key, _ := w.bob.Self().Recipient()
				env, err = envelope.Seal(in, w.alice.id.Sign, key)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := w.alice.uploadAll(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			if err := w.alice.hub.do(tctx(t), "POST", "/v1/messages", env, nil); err != nil {
				t.Fatal(err)
			}
			if err := w.bob.accept(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			var reason string
			if err := w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, env.ID).Scan(&reason); err != nil || reason != reasonInvalid {
				t.Fatalf("invalid carrier not refused %q %v", reason, err)
			}
			var rows int
			w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox`).Scan(&rows)
			if rows != 0 {
				t.Fatalf("invalid carrier entered inbox/jobs: %d", rows)
			}
			if _, err := w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
				t.Fatalf("invalid carrier installed context %v", err)
			}
		})
	}
}

func TestGroupCarrierEnvelopeBounds(t *testing.T) {
	w, p, c := newGroupPublicationFixture(t)
	env := groupFixturePayload(t, w.alice, w.bob, p.Root, envelope.SubGroupContext, protocol.GroupCarrier{V: 1, Seq: c.Seq, Hash: c.Hash}, p, false)
	in, err := envelope.Open(env, w.bob.id, w.bob.Address, w.alice.Self())
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(env); len(raw) >= protocol.MaxBody || len(env.CT) >= envelope.MaxCiphertext {
		t.Fatal("descriptor envelope exceeded existing limits")
	}
	recipient, _ := w.bob.Self().Recipient()
	for _, name := range []string{"size", "ciphertext size", "two files", "unknown descriptor", "request", "PID", "target"} {
		t.Run(name, func(t *testing.T) {
			bad := in
			bad.Attachments = slices.Clone(in.Attachments)
			switch name {
			case "size":
				bad.Attachments[0].Size = protocol.MaxGroupState + 1
			case "ciphertext size":
				bad.Attachments[0].Blob.Size = protocol.MaxGroupState + (64 << 10) + 1
			case "two files":
				bad.Attachments = append(bad.Attachments, bad.Attachments[0])
			case "unknown descriptor":
				bad.Body = strings.TrimSuffix(bad.Body, "}") + `,"extra":1}`
			case "request":
				bad.Kind = envelope.KindQuestion
			case "PID":
				bad.PID = "malformed-pid"
			case "target":
				bad.Target = &envelope.Target{Address: w.bob.Address}
			}
			if _, err := envelope.Seal(bad, w.alice.id.Sign, recipient); err == nil {
				t.Fatal("malformed carrier envelope sealed")
			}
		})
	}
}

func TestGroupCarrierLegacyExactReplayAndOversizeBeforeCustody(t *testing.T) {
	w, p, first := newGroupPublicationFixture(t)
	if _, err := w.alice.PublishGroup(tctx(t), first, p); err != nil {
		t.Fatal(err)
	}
	next := p.State
	next.Seq = 1
	next.Prev = p.State.Hash()
	next.Sign(w.alice.id.Sign)
	legacy := GroupContext{Root: p.Root, Proof: []protocol.GroupState{p.State}, State: next}
	commit := sealedCurrentProof(t, w.alice, w.bob, legacy)
	if _, err := w.alice.PublishGroup(tctx(t), commit, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.PublishGroup(tctx(t), commit, legacy); err != nil {
		t.Fatalf("exact accepted legacy replay changed contract %v", err)
	}
	var saved []byte
	w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=1`, p.State.Conv).Scan(&saved)
	raw, _ := json.Marshal(commit)
	if !bytes.Equal(raw, saved) {
		t.Fatal("accepted legacy ciphertext upgraded")
	}
	// Oversized whole contexts are refused before a new publication is saved
	// or custody requested, even if the commit's ciphertext bound allows it.
	states := []protocol.GroupState{p.State, next}
	for {
		prev := states[len(states)-1]
		n := prev
		n.Seq++
		n.Prev = prev.Hash()
		n.Sign(w.alice.id.Sign)
		legacy = GroupContext{Root: p.Root, Proof: slices.Clone(states), State: n}
		raw, _ = json.Marshal(legacy)
		if len(raw) > protocol.MaxGroupState {
			break
		}
		states = append(states, n)
	}
	oversize := sealedCurrentProof(t, w.alice, w.bob, legacy)
	if err := oversize.Validate(); err != nil {
		t.Fatalf("fixture must fit signed commit boundary %v", err)
	}
	if _, err := w.alice.PublishGroup(tctx(t), oversize, legacy); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unforwardable context reached publication %v", err)
	}
	var rows int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=? AND seq=?`, p.State.Conv, oversize.Seq).Scan(&rows)
	if rows != 0 {
		t.Fatalf("unforwardable record persisted for perpetual recovery %d", rows)
	}
}

func TestGroupCarrierOrdinaryRunDelivery(t *testing.T) {
	if !slices.Contains(ownCaps, protocol.CapGroup) {
		t.Fatal("grp1 missing after qualified engine parity")
	}
	w, p := groupOrdinaryPublication(t)
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	eventually(t, "ordinary member receives encrypted current context", func() bool {
		got, err := w.bob.GroupContext(p.State.Conv)
		return err == nil && got.State.Hash() == p.State.Hash() && len(got.Proof) == 0
	})
	var page protocol.GroupJournalPage
	err := w.bob.hub.do(tctx(t), "GET", "/v1/groups/"+p.State.Conv+"/chain?creator="+p.Root.Creator.Fingerprint+"&after=-1", nil, &page)
	var denied *HubError
	if !errors.As(err, &denied) || denied.Status != 403 {
		t.Fatalf("ordinary chain ACL changed %v", err)
	}
	msgs, err := w.bob.store.convMessages(p.State.Conv, w.bob.Address, w.bob.Self().Fingerprint(), nil)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("carrier became chat %+v %v", msgs, err)
	}
	unread, err := w.bob.ConvUnread()
	if err != nil || len(unread[p.State.Conv]) != 0 {
		t.Fatalf("carrier unread %+v %v", unread, err)
	}
	var jobs, alerts int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE kind IN ('question','task') OR state!=''`).Scan(&jobs)
	w.bob.store.db.QueryRow(`SELECT count(*) FROM alerts`).Scan(&alerts)
	if jobs != 0 || alerts != 0 {
		t.Fatalf("carrier execution/attention jobs=%d alerts=%d", jobs, alerts)
	}
	if review, e := w.bob.PageReview(); e != nil || len(review.Device)+len(review.Conv)+len(review.Held) != 0 {
		t.Fatalf("carrier review cards %+v %v", review, e)
	}
	if more, e := w.bob.historyPageFor(w.bob.Self(), historyPos{}); e != nil || more {
		t.Fatalf("carrier exported as linked history %v %v", more, e)
	}
	var exported int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub='history'`).Scan(&exported)
	if exported != 0 {
		t.Fatalf("carrier became history/file grant: %d", exported)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		if files, e := filepath.Glob(filepath.Join(a.home, "staging", "upload-*")); e != nil || len(files) != 0 {
			t.Fatalf("carrier left staged plaintext %+v %v", files, e)
		}
		if files, e := filepath.Glob(filepath.Join(a.home, "downloads", "*.json")); e != nil || len(files) != 0 {
			t.Fatalf("carrier persisted decrypted JSON %+v %v", files, e)
		}
	}
	var proof int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM group_proof_records WHERE conv=?`, p.State.Conv).Scan(&proof)
	if proof != 2 {
		t.Fatalf("original prefix incomplete %d", proof)
	}
}

func TestGroupCarrierReorderHeldDuplicateRestart(t *testing.T) {
	w, p := groupOrdinaryPublication(t)
	stop := runAgent(t, w.bob)
	publishGroupFixtureCaps(t, w.bob, true)
	var context, proof envelope.Envelope
	for _, env := range groupQueuedCarriers(t, w.alice) {
		var sub, body string
		w.alice.store.db.QueryRow(`SELECT sub,body FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body)
		var desc protocol.GroupCarrier
		json.Unmarshal([]byte(body), &desc)
		if desc.Seq == 1 {
			if sub == envelope.SubGroupContext {
				context = env
			}
			if sub == envelope.SubGroupProof {
				proof = env
			}
		}
	}
	if context.ID == "" || proof.ID == "" {
		t.Fatal("producer batch incomplete")
	}
	if _, err := w.alice.deliver(tctx(t), context, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reordered context held for original proof", func() bool {
		var reason string
		err := w.bob.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, context.ID).Scan(&reason)
		return err == nil && reason == reasonProof
	})
	if _, err := w.bob.GroupContext(p.State.Conv); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("context installed without prefix %v", err)
	}
	if _, err := w.alice.deliver(tctx(t), proof, nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "held context wakes after proof", func() bool {
		got, err := w.bob.GroupContext(p.State.Conv)
		return err == nil && got.State.Hash() == p.State.Hash()
	})
	stop()
	reopened, err := Open(w.bob.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.accept(tctx(t), context); err != nil {
		t.Fatal(err)
	}
	var count int
	reopened.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, context.ID).Scan(&count)
	if count != 1 {
		t.Fatalf("duplicate context rows %d", count)
	}
	got, err := reopened.GroupContext(p.State.Conv)
	if err != nil || got.State.Hash() != p.State.Hash() {
		t.Fatalf("restart lost context %v", err)
	}
}

func TestGroupCarrierAtomicRecoveryExactlyOnce(t *testing.T) {
	for _, responseLoss := range []bool{false, true} {
		t.Run(map[bool]string{false: "local insert failure", true: "CAS response loss"}[responseLoss], func(t *testing.T) {
			w, p, c := newGroupPublicationFixture(t)
			if responseLoss {
				injectFaults(w.alice).add("POST", "/v1/groups/", 1, true)
			} else {
				if _, err := w.alice.store.db.Exec(`CREATE TRIGGER fail_group_enqueue BEFORE INSERT ON outbox WHEN NEW.sub='group-context' BEGIN SELECT RAISE(ABORT,'synthetic enqueue failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.alice.PublishGroup(tctx(t), c, p); err == nil {
				t.Fatal("injected interruption succeeded")
			}
			var published, rows int
			w.alice.store.db.QueryRow(`SELECT published FROM group_publications WHERE conv=?`, p.State.Conv).Scan(&published)
			w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=?`, p.State.Conv).Scan(&rows)
			if published != 0 || rows != 0 {
				t.Fatalf("partial transition published=%d rows=%d", published, rows)
			}
			if !responseLoss {
				w.alice.store.db.Exec(`DROP TRIGGER fail_group_enqueue`)
			}
			if err := w.alice.RecoverGroupPublications(tctx(t)); err != nil {
				t.Fatal(err)
			}
			if _, err := w.alice.PublishGroup(tctx(t), c, p); err != nil {
				t.Fatal(err)
			}
			w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=?`, p.State.Conv).Scan(&rows)
			if rows != 2 {
				t.Fatalf("batch duplicated %d", rows)
			}
			got, err := w.alice.GroupContext(p.State.Conv)
			if err != nil || got.State.Hash() != p.State.Hash() {
				t.Fatalf("recovery membership %v", err)
			}
		})
	}
}

func TestGroupCarrierSupersededRecoveryNeedsPublishedCurrentBatch(t *testing.T) {
	w, current := groupOrdinaryPublication(t)
	var raw, payload []byte
	if err := w.alice.store.db.QueryRow(`SELECT record,payload FROM group_publications WHERE conv=? AND seq=0`, current.State.Conv).Scan(&raw, &payload); err != nil {
		t.Fatal(err)
	}
	var old protocol.GroupCommit
	var packet GroupContext
	json.Unmarshal(raw, &old)
	json.Unmarshal(payload, &packet)
	var before int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=?`, current.State.Conv).Scan(&before)
	if _, err := w.alice.store.db.Exec(`UPDATE group_publications SET published=0 WHERE conv=?`, current.State.Conv); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.PublishGroup(tctx(t), old, packet); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("superseded custody completed without newer durable published batch %v", err)
	}
	var pending int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM group_publications WHERE conv=? AND published=0`, current.State.Conv).Scan(&pending)
	if pending != 2 {
		t.Fatalf("unpublished current authorized completion %d", pending)
	}
	if _, err := w.alice.store.db.Exec(`UPDATE group_publications SET published=1 WHERE conv=? AND seq=1`, current.State.Conv); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.RecoverGroupPublications(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var after int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=?`, current.State.Conv).Scan(&after)
	if before != after {
		t.Fatalf("superseded replay created old fan-out %d -> %d", before, after)
	}
	got, err := w.alice.GroupContext(current.State.Conv)
	if err != nil || got.State.Hash() != current.State.Hash() {
		t.Fatalf("superseded replay rewound current snapshot %v", err)
	}
	var saved []byte
	w.alice.store.db.QueryRow(`SELECT record FROM group_publications WHERE conv=? AND seq=0`, current.State.Conv).Scan(&saved)
	if !bytes.Equal(saved, raw) {
		t.Fatal("superseded accepted bytes changed")
	}
}
