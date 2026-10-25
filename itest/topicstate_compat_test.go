package itest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The last published tss1 writer must keep its original mark when a linked
// reader advertises only tss2. Run real old code: the current resolver already
// knows the implication and cannot stand in for that writer.
func TestTopicStateActualOldWriterBacklog(t *testing.T) {
	const tag = "v0.9.5"
	commit, err := gitOut(compatRepo(), "rev-parse", "--verify", tag+"^{commit}")
	if err != nil {
		if os.Getenv("AGENTNET_REQUIRE_COMPAT") == "1" {
			t.Fatal("topic-state compatibility needs the published v0.9.5 tag")
		}
		t.Skip("fetch the published v0.9.5 tag to run the actual old-writer test")
	}
	dir := t.TempDir()
	head := &cli{t: t, bin: buildStampedCLI(t, dir, nextRelease(t, tag)), dir: dir}
	old := &cli{t: t, bin: buildTagCLI(t, tag), dir: dir}
	if !strings.Contains(filepath.Base(filepath.Dir(old.bin)), "-"+commit[:12]+"-") {
		t.Fatal("cached old writer does not identify the resolved release commit")
	}
	helper := buildOldTopicMarkHelper(t, tag, commit)
	t.Logf("actual old writer and operation adapter: %s at %s", tag, commit)
	w := &mixedWorld{t: t, dir: dir, head: head, release: nextRelease(t, tag), addr: freeAddr(t)}
	w.startHub(time.Hour) // Isolate signed capability behavior from suspension.
	alice := w.join("alice-old", "", "laptop", "Alice", old)
	w.admin = alice
	bob := w.join("bob", "bob", "desk", "Bob", head)
	phone := w.link(alice, "alice-phone", "phone", head)
	phoneKey, err := identity.Load(filepath.Join(dir, phone.home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the linked phone's signed tss2-only advertisement", 30*time.Second, func() bool {
		var p protocol.Profile
		if !w.get("/v1/agents/"+phone.address+"/profile", &p) || len(p.Sessions) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, raw := range p.Caps {
			c, e := protocol.ParseCapsRecord(raw)
			if e == nil && c.Address == phone.address && c.Verify(phoneKey.Public(phone.address).SignKey) == nil && c.Has(protocol.CapTopicStateSyncV2) && !c.Has(protocol.CapTopicStateSync) && len(c.Caps) <= protocol.MaxAdvertisedCaps {
				seen[c.Session] = true
			}
		}
		for _, s := range p.Sessions {
			if !seen[s] {
				return false
			}
		}
		return true
	})
	first := strings.Fields(alice.run("send", bob.address, "original topic for old-writer compatibility"))[0]
	phoneDB := phone.db(t)
	defer phoneDB.Close()
	waitUntil(t, "the linked phone's original topic history", 30*time.Second, func() bool {
		var n int
		return phoneDB.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, first).Scan(&n) == nil && n == 1
	})
	mark := exec.Command(helper, filepath.Join(dir, alice.home), bob.address, first)
	if out, e := mark.CombinedOutput(); e != nil {
		t.Fatalf("published writer's topic operation: %v\n%s", e, out)
	}
	alice.daemon(old) // Only this owned fixture process; flush through old code.
	oldDB := alice.db(t)
	defer oldDB.Close()
	var carrier, sealed, body, required string
	waitUntil(t, "the old writer's retained V1 carrier", 30*time.Second, func() bool {
		return oldDB.QueryRow(`SELECT id,envelope,body,required_cap FROM outbox WHERE recipient=? AND sub='topic-state-sync' AND required_cap='tss1' AND state='waiting' LIMIT 1`, phone.address).Scan(&carrier, &sealed, &body, &required) == nil
	})
	var record protocol.TopicStateSync
	if err = json.Unmarshal([]byte(body), &record); err != nil || len(record.Marks) != 1 {
		t.Fatalf("old writer did not retain one V1 mark: %v", err)
	}
	aliceKey, err := identity.Load(filepath.Join(dir, alice.home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := record.Marks[0]
	if record.V != 1 || required != protocol.CapTopicStateSync || want.Scope != bob.address || want.Topic != first || want.Mark != protocol.TopicMarkDone || want.Count != 1 || want.Writer != aliceKey.Public(alice.address).Fingerprint() {
		t.Fatal("old writer retained the wrong logical mark")
	}
	alice.run("send", bob.address, "unrelated delivery past an old-writer waiting mark")
	bobDB := bob.db(t)
	defer bobDB.Close()
	waitUntil(t, "unrelated delivery while the old mark waits", 30*time.Second, func() bool {
		var n int
		return bobDB.QueryRow(`SELECT count(*) FROM inbox WHERE body=?`, "unrelated delivery past an old-writer waiting mark").Scan(&n) == nil && n == 1
	})
	var gotSealed, gotBody, state string
	if err = oldDB.QueryRow(`SELECT envelope,body,state FROM outbox WHERE id=?`, carrier).Scan(&gotSealed, &gotBody, &state); err != nil || gotSealed != sealed || gotBody != body || state != "waiting" {
		t.Fatal("unrelated delivery replaced or lost the old waiting carrier", err)
	}
	var pending int
	if err = phoneDB.QueryRow(`SELECT count(*) FROM topic_marks WHERE owner=? AND scope=? AND topic=?`, record.Person, want.Scope, want.Topic).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("old writer unexpectedly bypassed the tss2-only advertisement", err)
	}
	alice.daemon(head) // Upgrade the writer, preserving its exact home and copy.
	waitUntil(t, "the retained old mark after writer upgrade", 30*time.Second, func() bool {
		var mark, writer string
		var count int64
		return phoneDB.QueryRow(`SELECT mark,count,writer FROM topic_marks WHERE owner=? AND scope=? AND topic=?`, record.Person, want.Scope, want.Topic).Scan(&mark, &count, &writer) == nil && mark == want.Mark && count == want.Count && writer == want.Writer
	})
	waitUntil(t, "the original carrier's delivery receipt", 30*time.Second, func() bool {
		return oldDB.QueryRow(`SELECT envelope,body,state FROM outbox WHERE id=?`, carrier).Scan(&gotSealed, &gotBody, &state) == nil && state == "delivered"
	})
	if gotSealed != sealed || gotBody != body {
		t.Fatal("upgrading resealed the retained carrier")
	}
	var copies int
	if err = oldDB.QueryRow(`SELECT count(*) FROM outbox o WHERE recipient=? AND sub='topic-state-sync' AND EXISTS(SELECT 1 FROM json_each(o.body,'$.marks') m WHERE json_extract(m.value,'$.scope')=? AND json_extract(m.value,'$.topic')=? AND json_extract(m.value,'$.writer')=?)`, phone.address, want.Scope, want.Topic, want.Writer).Scan(&copies); err != nil || copies != 1 {
		t.Fatalf("old mark duplicated after upgrade: %d copies, %v", copies, err)
	}
}

// Compile a private test adapter with the published source, rather than adding
// a shipped CLI command or writing either device's database from the test.
func buildOldTopicMarkHelper(t *testing.T, tag, commit string) string {
	t.Helper()
	src := t.TempDir()
	archive := filepath.Join(src, "source.tar")
	if out, err := exec.Command("git", "-C", compatRepo(), "archive", "-o", archive, commit).CombinedOutput(); err != nil {
		t.Fatalf("archive published topic writer: %v\n%s", err, out)
	}
	extract := exec.Command("tar", "-x", "-f", "source.tar")
	extract.Dir = src
	if out, err := extract.CombinedOutput(); err != nil {
		t.Fatalf("extract published topic writer: %v\n%s", err, out)
	}
	const adapter = `package main
import ("fmt"; "os"; "github.com/misunders2d/agentnet/internal/client")
func main() {
 if len(os.Args)!=4 { os.Exit(2) }
 a,err:=client.Open(os.Args[1]); if err!=nil { fmt.Fprintln(os.Stderr,err); os.Exit(1) }
 covered,err:=a.MarkTopicDone(os.Args[2],os.Args[3],0)
 closeErr:=a.Close()
 if err!=nil || closeErr!=nil || !covered { fmt.Fprintln(os.Stderr,"old topic mark failed",err,closeErr,covered); os.Exit(1) }
}
`
	path := filepath.Join(src, "cmd", "topic-mark-compat")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "main.go"), []byte(adapter), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(src, "topic-mark-compat")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-X github.com/misunders2d/agentnet/internal/protocol.Version="+tag, "-o", bin, "./cmd/topic-mark-compat")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build published topic writer adapter: %v\n%s", err, out)
	}
	return bin
}
