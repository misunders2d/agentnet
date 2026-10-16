//go:build linux

package static

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// idbCheck runs in a real browser: the device store's IndexedDB writes are
// all or nothing, keep a CryptoKey (the probe), and page in key order.
const idbCheck = `<!doctype html><meta charset="utf-8"><script type="module">
import { openIDB, probeStore, Engine } from "/engine.mjs";
const out = {};
try {
  const name = "agentnet-check-" + Date.now();
  const st = await openIDB(name);
  await probeStore(st);
  out.probe = true;
  await st.write([{ s: "kv", k: "kept", v: 1 }]);
  try {
    await st.write([{ s: "kv", k: "first", v: "rolled back" }, { s: "kv", k: "kept", v: 2 }, { s: "kv", k: "second", v: () => {} }]);
  } catch (e) { out.error = e.name; }
  out.first = (await st.get("kv", "first")) ?? null;
  out.kept = (await st.get("kv", "kept")) ?? null;
  // The retraction mark moves only after a committed write that puts a
  // retraction or removes a message row.
  const marks = [st.retractionMark()];
  await st.write([{ s: "inbox", k: "m1", v: { id: "m1", body: "hi" } }]);
  marks.push(st.retractionMark());
  await st.write([{ s: "outbox", k: "r1", v: { id: "r1", control: true, sub: "retraction" } }]);
  marks.push(st.retractionMark());
  try { await st.write([{ s: "inbox", k: "r2", v: { id: "r2", control: true, sub: "retraction", f: () => {} } }]); } catch (e) { /* not stored */ }
  marks.push(st.retractionMark());
  await st.write([{ s: "inbox", k: "m1", v: undefined }]);
  marks.push(st.retractionMark());
  out.marks = marks.map((m) => m - marks[0]).join(",");
  await st.write(["c", "a", "d", "b"].map((k) => ({ s: "held", k, v: { id: k } })));
  out.pages = [];
  for (let pos = "", page; (page = await st.after("held", pos, 2)).length; pos = page[page.length - 1].id) out.pages.push(page.map((v) => v.id).join(""));
  // The production bulk read preserves caller order, missing entries and
  // independent structured clones, including duplicate and empty keys.
  await st.write([
    { s: "kv", k: "bulk/a", v: { label: "A", nested: { value: 1 } } },
    { s: "kv", k: "bulk/b", v: { label: "B", nested: { value: 2 } } },
    { s: "kv", k: "", v: { label: "empty" } },
  ]);
  const bulk = await st.getMany("kv", ["bulk/b", "", "bulk/missing", "bulk/a", "bulk/a"]);
  out.bulkOrder = bulk.map(v => v?.label ?? "<missing>").join(",");
  out.bulkMissing = bulk[2] === undefined;
  out.bulkDuplicate = bulk[3] !== bulk[4] && bulk[3].nested !== bulk[4].nested;
  bulk[3].nested.value = 99;
  const again = await st.getMany("kv", ["bulk/a"]);
  out.bulkClone = bulk[4].nested.value === 1 && again[0].nested.value === 1 && (await st.get("kv", "bulk/a")).nested.value === 1;
  const empty = await st.getMany("kv", []);
  out.bulkEmpty = Array.isArray(empty) && empty.length === 0;

  // Build the real authority-check format from the existing peer index and
  // production engine helper; the write itself is the actual IDB transaction.
  const engine = new Engine({ store: st, base: location.origin });
  const peer = "selected/peer", other = "unrelated/peer";
  const incoming = { v: 1, id: "peer-in", from: peer, kind: "message", body: "original incoming", at: 1000 };
  const outgoing = { v: 1, id: "peer-out", to: peer, kind: "message", body: "original outgoing", at: 2000, state: "queued" };
  const unrelated = { ...incoming, id: "other-in", from: other };
  await st.write([{ s: "inbox", k: incoming.id, v: incoming }, { s: "outbox", k: outgoing.id, v: outgoing }, { s: "inbox", k: unrelated.id, v: unrelated }]);
  const peerChecks = async () => {
    const checks = [], view = { inbox: await st.byThreadPeer("inbox", peer), outbox: await st.byThreadPeer("outbox", peer) };
    await engine.authorityRows({ thread_peer: peer }, checks, view);
    return checks;
  };
  await st.write([{ s: "kv", k: "peer-success", v: true }], await peerChecks());
  out.peerGuardSuccess = await st.get("kv", "peer-success") === true;
  const unaffected = await peerChecks();
  await st.write([{ s: "inbox", k: unrelated.id, v: { ...unrelated, body: "other changed" } }, { s: "outbox", k: "other-out", v: { ...outgoing, id: "other-out", to: other } }]);
  await st.write([{ s: "kv", k: "other-peer-safe", v: true }], unaffected);
  out.otherPeerSafe = await st.get("kv", "other-peer-safe") === true;

  const beforeInsert = await peerChecks();
  await st.write([{ s: "inbox", k: "peer-inserted", v: { ...incoming, id: "peer-inserted", body: "concurrent insertion" } }]);
  try {
    await st.write([{ s: "kv", k: "insert-partial", v: true }, { s: "outbox", k: outgoing.id, v: { ...outgoing, body: "must roll back" } }], beforeInsert);
  } catch (e) { out.peerInsertError = e.message; }
  out.peerInsertRollback = await st.get("kv", "insert-partial") === undefined && (await st.get("outbox", outgoing.id)).body === outgoing.body && (await st.get("inbox", "peer-inserted")).body === "concurrent insertion";

  const beforeChange = await peerChecks();
  await st.write([{ s: "outbox", k: outgoing.id, v: { ...outgoing, body: "concurrent change" } }]);
  try {
    await st.write([{ s: "kv", k: "change-partial", v: true }, { s: "inbox", k: incoming.id, v: { ...incoming, body: "must roll back" } }], beforeChange);
  } catch (e) { out.peerChangeError = e.message; }
  out.peerChangeRollback = await st.get("kv", "change-partial") === undefined && (await st.get("inbox", incoming.id)).body === incoming.body && (await st.get("outbox", outgoing.id)).body === "concurrent change";
  st.close();
  indexedDB.deleteDatabase(name);
  // Blocked: an older connection (another tab, at version 1) keeps our open
  // from upgrading; the open is refused at once, and once that connection
  // goes away the late connection is closed, not left open: the database
  // can be deleted without waiting on anyone.
  const name2 = name + "-blocked";
  const old = await new Promise((res, rej) => { const r = indexedDB.open(name2, 1); r.onupgradeneeded = () => r.result.createObjectStore("kv"); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
  try { await openIDB(name2); out.blocked = "opened"; } catch (e) { out.blocked = e.message; }
  old.close();
  await new Promise((r) => setTimeout(r, 300)); // the deferred open completes and is closed
  out.lateClosed = await new Promise((res) => { const d = indexedDB.deleteDatabase(name2); d.onblocked = () => res("blocked by a lingering connection"); d.onsuccess = () => res(true); d.onerror = () => res(String(d.error)); setTimeout(() => res("timeout"), 2000); });
} catch (e) { out.fail = String(e && e.message || e); }
await fetch("/result", { method: "POST", body: JSON.stringify(out) });
</script>`

// The store against real IndexedDB, in Chrome or Chromium on Linux named
// by AGENTNET_CHROME (opt-in: AGENTNET_CHROME=google-chrome-stable go test
// ./internal/ui/static). The page is served on loopback http, which a
// browser treats as a secure context.
func TestDeviceStoreInChrome(t *testing.T) {
	chrome := os.Getenv("AGENTNET_CHROME")
	if chrome == "" {
		t.Skip("AGENTNET_CHROME is not set")
	}
	result := make(chan []byte, 1)
	files := http.FileServerFS(Files)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, idbCheck)
		case "/result":
			data, _ := io.ReadAll(r.Body)
			select {
			case result <- data:
			default:
			}
		default:
			files.ServeHTTP(w, r)
		}
	}))
	defer srv.Close()
	// Chrome runs in its own process group (the command may be a wrapper
	// script), and the whole group is stopped, so no helper process still
	// writes to the profile when it is removed.
	profile, err := os.MkdirTemp("", "agentnet-chrome-")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome, "--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profile, srv.URL+"/")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		group := -cmd.Process.Pid
		syscall.Kill(group, syscall.SIGTERM)
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			syscall.Kill(group, syscall.SIGKILL)
			<-exited
		}
		for i := 0; i < 50 && syscall.Kill(group, 0) == nil; i++ { // helpers still stopping
			time.Sleep(100 * time.Millisecond)
		}
		os.RemoveAll(profile)
	}()
	var got struct {
		Probe              bool     `json:"probe"`
		Error              string   `json:"error"`
		First              any      `json:"first"`
		Kept               any      `json:"kept"`
		Pages              []string `json:"pages"`
		Marks              string   `json:"marks"`
		Fail               string   `json:"fail"`
		BulkOrder          string   `json:"bulkOrder"`
		BulkMissing        bool     `json:"bulkMissing"`
		BulkDuplicate      bool     `json:"bulkDuplicate"`
		BulkClone          bool     `json:"bulkClone"`
		BulkEmpty          bool     `json:"bulkEmpty"`
		PeerGuardSuccess   bool     `json:"peerGuardSuccess"`
		OtherPeerSafe      bool     `json:"otherPeerSafe"`
		PeerInsertError    string   `json:"peerInsertError"`
		PeerInsertRollback bool     `json:"peerInsertRollback"`
		PeerChangeError    string   `json:"peerChangeError"`
		PeerChangeRollback bool     `json:"peerChangeRollback"`
		// A blocked open is refused at once; the connection that completes
		// later is closed, so the database can be deleted right away.
		Blocked    string `json:"blocked"`
		LateClosed any    `json:"lateClosed"`
	}
	select {
	case data := <-result:
		t.Logf("real IndexedDB results: %s", data)
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("no result from the browser")
	}
	if got.Fail != "" || !got.Probe {
		t.Fatalf("store: %+v", got)
	}
	// A value that cannot be stored fails the write, and nothing of it is
	// kept: not the value before it, not the change to a kept one.
	if got.Error != "DataCloneError" || got.First != nil || got.Kept != float64(1) {
		t.Fatalf("a failed write kept part of itself: %+v", got)
	}
	if len(got.Pages) != 2 || got.Pages[0] != "ab" || got.Pages[1] != "cd" {
		t.Fatalf("pages: %v", got.Pages)
	}
	if got.Marks != "0,0,1,1,2" {
		t.Fatalf("retraction marks after a message, a retraction, a failed retraction, a removal: %s", got.Marks)
	}
	if got.BulkOrder != "B,empty,<missing>,A,A" || !got.BulkMissing || !got.BulkDuplicate || !got.BulkClone || !got.BulkEmpty {
		t.Fatalf("bulk reads lost order, missing entries, independent clones or empty keys: %+v", got)
	}
	if !got.PeerGuardSuccess || !got.OtherPeerSafe {
		t.Fatalf("indexed peer guard rejected an unchanged scope or unrelated-peer write: %+v", got)
	}
	if got.PeerInsertError != "storage changed during verification" || !got.PeerInsertRollback || got.PeerChangeError != "storage changed during verification" || !got.PeerChangeRollback {
		t.Fatalf("indexed peer guard did not atomically reject a same-peer insertion/change: %+v", got)
	}
	if got.Blocked != "storage is blocked by another tab" {
		t.Fatalf("a blocked open was not refused: %q", got.Blocked)
	}
	if got.LateClosed != true {
		t.Fatalf("the connection that completed after the refusal was not closed: %v", got.LateClosed)
	}
}
