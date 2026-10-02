package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func claudeReceiverFixture(t *testing.T, a *Agent, lazyParent ...bool) (ClaudeReplyChannelOwner, claudeNativeRoute) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("synthetic Linux process provenance")
	}
	_, ticks, binary, e := codexProcess(os.Getpid())
	if e != nil {
		t.Fatal(e)
	}
	digest, e := codexBinaryDigest(binary)
	if e != nil {
		t.Fatal(e)
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	cwd, _ := os.Readlink("/proc/self/cwd")
	projects := filepath.Join(t.TempDir(), "projects")
	dir := filepath.Join(projects, "synthetic-workspace")
	if len(lazyParent) == 0 || !lazyParent[0] {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
	}
	route := claudeNativeRoute{PID: os.Getpid(), Boot: strings.TrimSpace(string(boot)), Ticks: ticks,
		Binary: binary, SHA256: digest, CWD: cwd, Projects: projects, Source: claudeChannelSource}
	sid := protocol.NewID()
	file := filepath.Join(dir, sid+".jsonl")
	// The production entry always requires pinned native Claude ancestry. This
	// private test entry models provenance with this exact real test process.
	if _, e = a.registerClaudeReplySession("SessionStart", sid, file, route); e != nil {
		t.Fatal(e)
	}
	owner, e := a.claudeReplyChannelOwner(sid, route)
	if e != nil {
		t.Fatal(e)
	}
	return owner, route
}

func TestClaudeReplyRegistryRenewalAndDetach(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	views, e := w.alice.ReplySessions()
	if e != nil || len(views) != 1 || views[0].Harness != "claude" || !views[0].Active {
		t.Fatalf("registry %+v %v", views, e)
	}
	if owner.Source != claudeChannelSource || owner.SessionID == "" || owner.OwnerToken == "" {
		t.Fatal("private SDK lease missing")
	}
	// No sender text/SessionStart label grants native registration authority.
	if _, e = w.alice.ClaudeReplySessionHook("SessionStart", owner.SessionID, owner.File); e == nil {
		t.Fatal("non-native public hook obtained registration")
	}
	if _, e = w.alice.ClaudeReplyChannelOwner(owner.SessionID); e == nil {
		t.Fatal("non-native public SDK call obtained lease")
	}
	if _, e = w.alice.registerClaudeReplySession("PostToolUse", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	stable, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route)
	if e != nil || stable.Generation != owner.Generation || stable.OwnerToken != owner.OwnerToken {
		t.Fatal("ordinary hook changed active owner")
	}
	if _, e = w.alice.registerClaudeReplySession("SessionStart", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	renewed, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route)
	if e != nil || renewed.Handle != owner.Handle || renewed.Generation <= owner.Generation || renewed.OwnerToken == owner.OwnerToken {
		t.Fatal("native renewal did not retain exact binding and fence prior owner")
	}
	if _, e = w.alice.registerClaudeReplySession("Stop", owner.SessionID, filepath.Join(filepath.Dir(owner.File), "different.jsonl"), route); e == nil {
		t.Fatal("another physical file matched same SID")
	}
	if _, e = w.alice.registerClaudeReplySession("SessionEnd", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	r, e := replySessionIn(w.alice.store.db, owner.Handle)
	if e != nil || r.Active || r.CloseReason != "detached" || r.OwnerToken != "" {
		t.Fatal("native closure inferred task handoff or retained lease")
	}
	if _, e = w.alice.claudeReplyChannelOwner(owner.SessionID, route); e == nil {
		t.Fatal("detached channel reacquired lease without genuine native start")
	}
}

func TestClaudeReplyRegistryRefusesForeignRealmAndAmbiguity(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	r, e := replySessionIn(w.alice.store.db, owner.Handle)
	if e != nil {
		t.Fatal(e)
	}
	originalRealm := r.Realm
	r.Realm = "foreign"
	raw, _ := json.Marshal(r)
	if _, e = w.alice.store.db.Exec(`UPDATE reply_sessions SET record=? WHERE handle=?`, string(raw), r.Handle); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.claudeReplyChannelOwner(owner.SessionID, route); e == nil {
		t.Fatal("foreign enrollment lease accepted")
	}
	r.Realm = originalRealm
	raw, _ = json.Marshal(r)
	w.alice.store.db.Exec(`UPDATE reply_sessions SET record=? WHERE handle=?`, string(raw), r.Handle)
	r.Handle = protocol.NewID()
	raw, _ = json.Marshal(r)
	if _, e = w.alice.store.db.Exec(`INSERT INTO reply_sessions(handle,record) VALUES(?,?)`, r.Handle, string(raw)); e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.claudeReplyChannelOwner(owner.SessionID, route); e == nil {
		t.Fatal("ambiguous native lease accepted")
	}
	if _, e = w.alice.registerClaudeReplySession("SessionStart", owner.SessionID, owner.File, route); e == nil {
		t.Fatal("ambiguous registration silently selected receiver")
	}
}

func TestClaudeReplyLazyParentPathFences(t *testing.T) {
	for _, scenario := range []string{"escaping_missing_target", "inside_profile_substitution", "wrong_sid", "changed_route", "foreign_file", "strict_defaults"} {
		t.Run(scenario, func(t *testing.T) {
			w := newWorld(t, "")
			owner, route := claudeReceiverFixture(t, w.alice, true)
			if e := os.Mkdir(route.Projects, 0700); e != nil {
				t.Fatal(e)
			}
			switch scenario {
			case "escaping_missing_target":
				// Root must refuse escape even when EvalSymlinks cannot resolve its target.
				if e := os.Symlink("../../missing-outside", filepath.Dir(owner.File)); e != nil {
					t.Fatal(e)
				}
			case "inside_profile_substitution":
				target := filepath.Join(route.Projects, "different-project")
				if e := os.Mkdir(target, 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.Symlink("different-project", filepath.Dir(owner.File)); e != nil {
					t.Fatal(e)
				}
			case "wrong_sid":
				if _, e := w.alice.registerClaudeReplySession("SessionStart", "other-session", owner.File, route); e == nil {
					t.Fatal("wrong SID registered")
				}
				return
			case "changed_route":
				route.CWD += "-changed"
			case "foreign_file":
				foreign := filepath.Join(t.TempDir(), owner.SessionID+".jsonl")
				if _, e := w.alice.registerClaudeReplySession("SessionStart", owner.SessionID, foreign, route); e == nil {
					t.Fatal("foreign path registered")
				}
				return
			case "strict_defaults":
				// Pi/OMP and Codex continue using zero-option canonicalNativeFile.
				if _, e := canonicalNativeFile(owner.File); e == nil {
					t.Fatal("default missing-parent normalization weakened")
				}
				for _, h := range []string{"pi", "omp"} {
					if _, e := w.alice.RegisterReplySession(ReplySessionRegistration{Harness: h, SessionID: owner.SessionID, File: owner.File}); e == nil {
						t.Fatal("Pi/OMP missing parent accepted")
					}
				}
				if _, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route); e != nil {
					t.Fatal("owned lazy parent refused")
				}
				return
			}
			if _, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route); e == nil {
				t.Fatal("changed path/route recovered SDK owner")
			}
			if _, e := w.alice.TakeReplyReceiverInput(owner.ReplySessionCall); e == nil && scenario != "changed_route" {
				t.Fatal("unsafe path allowed Take")
			}
			// Changed caller route alone cannot change stored route: additionally prove
			// native verification rejects it at the genuine hook-method boundary.
			if scenario == "changed_route" {
				if _, e := w.alice.registerClaudeReplySession("PostToolUse", owner.SessionID, owner.File, route); e == nil {
					t.Fatal("changed hook route accepted")
				}
			}
		})
	}
}
