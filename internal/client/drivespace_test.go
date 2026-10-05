package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/gdrive"
)

func TestDriveEncryptedStateOwnershipAndAgentGrants(t *testing.T) {
	t.Parallel()
	w, conv, _ := dmWithHistory(t)
	enableDriveTestWorkspace(t, w)
	alice, _, e := w.alice.Person()
	if e != nil {
		t.Fatal(e)
	}
	bob, _, e := w.bob.Person()
	if e != nil {
		t.Fatal(e)
	}
	sp := gdrive.Space{Conv: conv, Folder: "fixture-folder", Name: "Private Project", Owner: alice.Person, Revision: 1}
	if e = w.alice.AdmitDriveSpace(alice.Person, sp); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.AdmitDriveSpace(alice.Person, sp); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.AdmitDriveSpace(bob.Person, sp); e == nil {
		t.Fatal("peer impersonated metadata owner")
	}
	attack := sp
	attack.Owner = bob.Person
	attack.Revision = 2
	attack.Previous = sp.Folder
	if e = w.bob.AdmitDriveSpace(bob.Person, attack); e == nil {
		t.Fatal("nonowner replaced folder")
	}
	fork := sp
	fork.Folder = "another-folder"
	if e = w.bob.AdmitDriveSpace(alice.Person, fork); e == nil {
		t.Fatal("same-revision fork applied")
	}
	gap := sp
	gap.Revision = 3
	gap.Previous = sp.Folder
	if e = w.bob.AdmitDriveSpace(alice.Person, gap); e == nil {
		t.Fatal("history gap applied")
	}
	if e = w.bob.AdmitDriveSpace(alice.Person, sp); e != nil {
		t.Fatal("idempotent history replay failed", e)
	}
	mu := w.bob.driveLock()
	mu.Lock()
	s, e := w.bob.readDrive()
	if e != nil {
		t.Fatal(e)
	}
	s.ClientID = "123-fixture.apps.googleusercontent.com"
	s.Token = gdrive.Token{Access: "PRIVATE-ACCESS", Refresh: "PRIVATE-REFRESH", Scope: gdrive.FullScope, Expiry: time.Now().Add(time.Hour)}
	s.Account = "private@example.test"
	if e = w.bob.writeDrive(s); e != nil {
		t.Fatal(e)
	}
	mu.Unlock()
	raw, e := os.ReadFile(filepath.Join(w.bob.home, "google-drive.age"))
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"PRIVATE-ACCESS", "PRIVATE-REFRESH", "private@example.test", "Private Project"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("local Google data stored in plaintext", secret)
		}
	}
	p, e := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, nil, nil, "Drive fixture")
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "Drive agent invite", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	if _, e = w.bob.AcceptParticipation(tctx(t), p.PID); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.CheckDriveGrant(conv, p.PID, false); e == nil {
		t.Fatal("default granted agent Drive access")
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "grant", PID: p.PID, Grant: "read"}); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.CheckDriveGrant(conv, p.PID, false); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.CheckDriveGrant(conv, p.PID, true); e == nil {
		t.Fatal("read grant allowed write")
	}
	if e = w.alice.CheckDriveGrant(conv, p.PID, false); e == nil {
		t.Fatal("grant escaped agent host/client")
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "grant", PID: p.PID, Grant: "write"}); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.CheckDriveGrant(conv, p.PID, true); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "grant", PID: p.PID, Grant: "none"}); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.CheckDriveGrant(conv, p.PID, false); e == nil {
		t.Fatal("revoked broker grant still active")
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "grant", PID: p.PID, Grant: "read"}); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.DismissParticipation(tctx(t), p.PID); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.CheckDriveGrant(conv, p.PID, false); e == nil {
		t.Fatal("dismissed participation kept Drive access")
	}
	sp.Revision = 2
	sp.Previous = sp.Folder
	sp.Disconnected = true
	if e = w.bob.AdmitDriveSpace(alice.Person, sp); e != nil {
		t.Fatal(e)
	}
	v, e := w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "status"})
	if e != nil || v.Space != nil || len(v.Grants) != 0 {
		t.Fatalf("disconnect view %+v %v", v, e)
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "connect", Folder: "attacker-folder", ConfirmOutside: true}); e == nil {
		t.Fatal("disconnected space lost original ownership")
	}
}

type drivePublishFixture func(context.Context, gdrive.Space) error

func (f drivePublishFixture) PublishDriveSpace(ctx context.Context, s gdrive.Space) error {
	return f(ctx, s)
}
func TestDriveCreatedFolderPublicationRecovery(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	s, e := w.alice.readDrive()
	if e != nil {
		t.Fatal(e)
	}
	s.ClientID = "fixture"
	s.Token = gdrive.Token{Access: "fixture-token", Scope: gdrive.FileScope, Expiry: time.Now().Add(time.Hour)}
	if e = w.alice.writeDrive(s); e != nil {
		t.Fatal(e)
	}
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/files" {
			t.Errorf("unexpected recovery provider call %s %s", r.Method, r.URL)
			w.WriteHeader(400)
			return
		}
		creates++
		fmt.Fprint(w, `{"id":"recovery-folder","name":"Recoverable Project","mimeType":"application/vnd.google-apps.folder"}`)
	}))
	defer srv.Close()
	provider := &gdrive.Client{API: srv.URL}
	fail := drivePublishFixture(func(context.Context, gdrive.Space) error { return errors.New("synthetic publication unavailable") })
	req := DriveRequest{Conv: conv, Action: "create", Name: "Recoverable Project", ConfirmOutside: true}
	v, e := w.alice.driveCommandWithPublisher(tctx(t), req, nil, provider, fail)
	if e != nil || v.Pending == nil || v.Pending.Folder != "recovery-folder" || v.Space != nil {
		t.Fatalf("recovery identity lost or falsely shared: %+v %v", v, e)
	}
	if creates != 1 {
		t.Fatal("create count")
	}
	saved, e := w.alice.readDrive()
	if e != nil || saved.Pending[conv].Folder != "recovery-folder" {
		t.Fatal("recovery not durable")
	}
	v, e = w.alice.driveCommandWithPublisher(tctx(t), req, nil, provider, fail)
	if e != nil || v.Pending == nil || creates != 1 {
		t.Fatal("Create retried instead of saved publication")
	}
	published := 0
	ok := drivePublishFixture(func(_ context.Context, s gdrive.Space) error {
		published++
		if s.Folder != "recovery-folder" || s.Revision != 1 {
			t.Fatal("changed pending identity")
		}
		return nil
	})
	req.Action = "publish-pending"
	v, e = w.alice.driveCommandWithPublisher(tctx(t), req, nil, provider, ok)
	if e != nil || v.Pending != nil || v.Space == nil || v.Space.Folder != "recovery-folder" || creates != 1 || published != 1 {
		t.Fatalf("publication retry %+v %v create%d publish%d", v, e, creates, published)
	}
}

func TestDriveConversationWireAndLinkedHistory(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	me, _, e := w.alice.Person()
	if e != nil {
		t.Fatal(e)
	}
	sp := gdrive.Space{Conv: conv, Folder: "wire-folder", Name: "Wire project", Owner: me.Person, Revision: 1}
	if e = w.alice.PublishDriveSpace(tctx(t), sp); e != nil {
		t.Fatal("PublishDriveSpace actual envelope path", e)
	}
	if e = w.alice.AdmitDriveSpace(me.Person, sp); e != nil {
		t.Fatal(e)
	}
	eventually(t, "Drive metadata at second person", func() bool { s, e := w.bob.readDrive(); return e == nil && s.Spaces[conv] == sp })
	if msgs, e := w.bob.ConversationMessages(conv); e != nil || len(msgs) != 3 {
		t.Fatalf("Drive metadata became visible chat turn: %d %v", len(msgs), e)
	}
	phone := linked(t, w.alice)
	eventually(t, "Drive metadata on newly linked device", func() bool { s, e := phone.readDrive(); return e == nil && s.Spaces[conv] == sp })
	s, e := phone.readDrive()
	if e != nil || s.Token.Access != "" || s.Account != "" || len(s.Grants) != 0 {
		t.Fatal("Google credentials/grants synced through conversation")
	}
	sp.Revision = 2
	sp.Previous = sp.Folder
	sp.Folder = "wire-folder-two"
	if e = w.alice.PublishDriveSpace(tctx(t), sp); e != nil {
		t.Fatal(e)
	}
	if e = w.alice.AdmitDriveSpace(me.Person, sp); e != nil {
		t.Fatal(e)
	}
	eventually(t, "updated Drive metadata on peer and linked device", func() bool {
		b, e1 := w.bob.readDrive()
		p, e2 := phone.readDrive()
		return e1 == nil && e2 == nil && b.Spaces[conv] == sp && p.Spaces[conv] == sp
	})
}

func enableDriveTestWorkspace(t *testing.T, w *world) {
	t.Helper()
	settings, e := w.alice.DriveStorageConfig(tctx(t))
	if e != nil {
		t.Fatal(e)
	}
	cfg := gdrive.StorageConfig{Enabled: true, Project: "agentnet-fixture", DesktopClientID: "123-fixture.apps.googleusercontent.com", APIConfirmed: true, ConsentConfirmed: true, ClientsConfirmed: true}
	if _, e = w.alice.SetDriveStorageConfig(tctx(t), gdrive.StorageUpdate{Config: cfg, Expect: settings.Revision}); e != nil {
		t.Fatal(e)
	}
}
func TestDriveSettingsDefaultOffAdminAndConsentSeparation(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	settings, e := w.alice.DriveStorageConfig(tctx(t))
	if e != nil || settings.Config.Enabled || !settings.CanAdmin {
		t.Fatalf("default/admin %+v %v", settings, e)
	}
	v, e := w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "status"})
	if e != nil || v.Enabled || v.Connected {
		t.Fatal("new installation not off", v, e)
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "consent", ConfirmAccount: true}); e == nil {
		t.Fatal("consent worked while provider disabled")
	}
	draft := gdrive.SetupDraft{Config: gdrive.StorageConfig{Project: "agentnet-fixture"}, CloudAccount: "admin@example.test", ExistingProject: true, Completed: map[string]bool{"account": true}}
	if _, e = w.bob.DriveSetup(tctx(t), DriveSetupRequest{Action: "save-draft", Draft: draft}); e == nil {
		t.Fatal("member changed admin setup")
	}
	if _, e = w.alice.DriveSetup(tctx(t), DriveSetupRequest{Action: "save-draft", Draft: draft}); e != nil {
		t.Fatal(e)
	}
	a, e := w.alice.DriveSetup(tctx(t), DriveSetupRequest{Action: "status"})
	if e != nil || a.Draft.CloudAccount != "admin@example.test" || !a.Draft.Completed["account"] {
		t.Fatal("resumable setup", a, e)
	}
	member, e := w.bob.DriveSetup(tctx(t), DriveSetupRequest{Action: "status"})
	if e != nil || member.Settings.CanAdmin || member.Draft.CloudAccount != "" {
		t.Fatal("admin local account/draft leaked", member, e)
	}
	enableDriveTestWorkspace(t, w)
	s, e := w.bob.readDrive()
	if e != nil {
		t.Fatal(e)
	}
	s.ClientID = "123-fixture.apps.googleusercontent.com"
	s.Token = gdrive.Token{Access: "KEEP-LOCAL-TOKEN", Refresh: "KEEP-LOCAL-REFRESH", Scope: gdrive.FileScope, Expiry: time.Now().Add(time.Hour)}
	s.Account = "private-person@example.test"
	if e = w.bob.writeDrive(s); e != nil {
		t.Fatal(e)
	}
	settings, e = w.alice.DriveStorageConfig(tctx(t))
	if e != nil {
		t.Fatal(e)
	}
	changed := settings.Config
	changed.DesktopClientID = "456-changed.apps.googleusercontent.com"
	if _, e = w.alice.SetDriveStorageConfig(tctx(t), gdrive.StorageUpdate{Config: changed, Expect: settings.Revision}); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "list"}); e == nil {
		t.Fatal("client config silently replaced consent")
	}
	after, e := w.bob.readDrive()
	if e != nil || !sameDriveToken(after.Token, s.Token) || after.Account != s.Account || after.ClientID != s.ClientID {
		t.Fatal("admin config mutated private consent")
	}
	settings, _ = w.alice.DriveStorageConfig(tctx(t))
	changed.Enabled = false
	if _, e = w.alice.SetDriveStorageConfig(tctx(t), gdrive.StorageUpdate{Config: changed, Expect: settings.Revision}); e != nil {
		t.Fatal(e)
	}
	v, e = w.bob.DriveCommand(tctx(t), DriveRequest{Conv: conv, Action: "status"})
	if e != nil || v.Enabled {
		t.Fatal("workspace disable not effective")
	}
	after, _ = w.bob.readDrive()
	if !sameDriveToken(after.Token, s.Token) {
		t.Fatal("workspace disable silently revoked/removed local tokens")
	}
}

func sameDriveToken(a, b gdrive.Token) bool {
	return a.Access == b.Access && a.Refresh == b.Refresh && a.Scope == b.Scope && a.Expiry.Equal(b.Expiry)
}
