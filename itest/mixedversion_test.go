package itest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The mixed-version release gate (TestMixedVersion) runs real programs of
// two versions against one relay: this source, built as the next release,
// and the newest release already published (its git tag). Owner rule:
// older apps must not impede others. Run it before every release:
//
//	go test ./itest -run TestMixedVersion -v -count=1
//
// It needs the release tags in the repository (git fetch --tags); without
// them it is skipped, unless AGENTNET_REQUIRE_COMPAT=1 (set in CI) makes
// that a failure. AGENTNET_MIXED_TAG runs it against another published tag.

// noReleaseOrigin is the release origin compiled into every binary these
// tests build: nothing listens there, so no automatic update or update
// check of a test binary ever reaches the project's real releases.
const noReleaseOrigin = "http://127.0.0.1:9/agentnet-test-releases"

// compatRepo is the git repository release tags are built from
// (AGENTNET_COMPAT_REPO, default the one this test is in).
func compatRepo() string {
	if r := os.Getenv("AGENTNET_COMPAT_REPO"); r != "" {
		return r
	}
	return ".."
}

// gitOut runs git in repo and returns its trimmed stdout.
func gitOut(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// releaseBelowHead is the newest published release tag (vX.Y.Z) in HEAD's
// history other than one on HEAD's own commit: what devices not updated
// yet run once HEAD is released. "" when the repository has none (a
// shallow clone without tags).
func releaseBelowHead() string {
	repo := compatRepo()
	head, err := gitOut(repo, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	tags, err := gitOut(repo, "tag", "--merged", "HEAD", "--sort=-v:refname", "--list", "v*.*.*")
	if err != nil {
		return ""
	}
	for _, tag := range strings.Fields(tags) {
		if !protocol.IsRelease(tag) {
			continue
		}
		if c, err := gitOut(repo, "rev-parse", "--verify", "--quiet", tag+"^{commit}"); err == nil && c != head {
			return tag
		}
	}
	return ""
}

// nextRelease is the release after tag: its next patch version.
func nextRelease(t *testing.T, tag string) string {
	t.Helper()
	i := strings.LastIndexByte(tag, '.')
	n, err := strconv.Atoi(tag[i+1:])
	if !protocol.IsRelease(tag) || err != nil {
		t.Fatalf("%q is not a release tag vX.Y.Z", tag)
	}
	return tag[:i+1] + strconv.Itoa(n+1)
}

// stampFlags are the link flags of a build that reports version.
func stampFlags(version string) string {
	return "-X github.com/misunders2d/agentnet/internal/protocol.Version=" + version + " -X main.releaseBase=" + noReleaseOrigin
}

// buildTagCLI builds the release tag from the local repository
// (compatRepo), reporting itself as that release, and returns the binary.
// It is kept in a cache keyed by the tag's commit, the Go version and the
// platform (AGENTNET_COMPAT_CACHE, default agentnet-itest in the user's
// cache directory), so each tag is compiled once per machine. A tag the
// repository does not have fails the test.
func buildTagCLI(t *testing.T, tag string) string {
	t.Helper()
	repo := compatRepo()
	commit, err := gitOut(repo, "rev-parse", "--verify", "--quiet", tag+"^{commit}")
	if err != nil || commit == "" {
		t.Fatalf("release %s is not in %s (git fetch --tags)", tag, repo)
	}
	cache := os.Getenv("AGENTNET_COMPAT_CACHE")
	if cache == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			t.Fatalf("no cache directory for release builds (set AGENTNET_COMPAT_CACHE): %v", err)
		}
		cache = filepath.Join(dir, "agentnet-itest")
	}
	name := "agentnet"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	key := fmt.Sprintf("%s-%s-%s-%s-%s", tag, commit[:12], runtime.Version(), runtime.GOOS, runtime.GOARCH)
	bin := filepath.Join(cache, key, name)
	if _, err := os.Stat(bin); err == nil {
		return bin
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	tarball := filepath.Join(t.TempDir(), "src.tar")
	for _, cmd := range []*exec.Cmd{
		exec.Command("git", "-C", repo, "archive", "-o", tarball, commit),
		exec.Command("tar", "-x", "-f", tarball, "-C", src),
	} {
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("extract %s: %v\n%s", tag, err, out)
		}
	}
	// Built beside the cached file and renamed into place: a concurrent
	// run never sees half a binary.
	tmp := filepath.Join(filepath.Dir(bin), fmt.Sprintf(".build-%d-%s", os.Getpid(), name))
	build := exec.Command("go", "build", "-trimpath", "-ldflags", stampFlags(tag), "-o", tmp, "./cmd/agentnet")
	build.Dir = src
	if out, err := build.CombinedOutput(); err != nil {
		os.Remove(tmp)
		t.Fatalf("build %s: %v\n%s", tag, err, out)
	}
	if err := os.Rename(tmp, bin); err != nil {
		t.Fatal(err)
	}
	return bin
}

// buildStampedCLI builds this source reporting version, a release, into
// dir: a development build would turn a relay's latest-only policy off.
func buildStampedCLI(t *testing.T, dir, version string) string {
	t.Helper()
	bin := filepath.Join(dir, "agentnet-"+version)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-ldflags", stampFlags(version), "-o", bin, "../cmd/agentnet")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", version, err, out)
	}
	return bin
}

// mixedDevice is one installation of a mixed-version world and the
// program its daemon runs now.
type mixedDevice struct {
	home, address string
	prog          *cli
	stop          func()
	runs          int
}

func (d *mixedDevice) run(args ...string) string {
	return d.prog.run(append([]string{"--home", d.home}, args...)...)
}

func (d *mixedDevice) try(args ...string) (string, error) {
	return d.prog.try(append([]string{"--home", d.home}, args...)...)
}

// stdout runs a command that may fail and returns its stdout only.
func (d *mixedDevice) stdout(args ...string) (string, error) {
	cmd := exec.Command(d.prog.bin, append([]string{"--home", d.home}, args...)...)
	cmd.Dir = d.prog.dir
	cmd.Env = append(os.Environ(), d.prog.env...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// daemon (re)starts the device's daemon with prog: a restart, or the
// person installing another version.
func (d *mixedDevice) daemon(prog *cli) {
	if d.stop != nil {
		d.stop()
	}
	d.prog = prog
	d.runs++
	d.stop = prog.start(fmt.Sprintf("%s-%d.log", d.home, d.runs), "--home", d.home, "daemon")
}

// count is how often line occurs in what the device's daemons wrote.
func (d *mixedDevice) count(line string) int {
	n := 0
	for i := 1; i <= d.runs; i++ {
		data, _ := os.ReadFile(filepath.Join(d.prog.dir, fmt.Sprintf("%s-%d.log", d.home, i)))
		n += strings.Count(string(data), line)
	}
	return n
}

// shows reports whether the device's dm show of conv has text.
func (d *mixedDevice) shows(conv, text string) bool {
	out, err := d.stdout("dm", "show", conv)
	return err == nil && strings.Contains(out, text)
}

// has reports whether conv is among the device's conversations.
func (d *mixedDevice) has(conv string) bool {
	out, err := d.stdout("dm", "list")
	return err == nil && strings.Contains(out, conv)
}

// db opens the device's database read-only.
func (d *mixedDevice) db(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(d.prog.dir, d.home, "agent.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// outboxRow is one copy a device sent, as its own database records it.
type outboxRow struct {
	id, lid, recipient, state, sub, err string
}

func (d *mixedDevice) outbox(t *testing.T) []outboxRow {
	t.Helper()
	db := d.db(t)
	defer db.Close()
	rows, err := db.Query(`SELECT id, coalesce(lid, ''), recipient, state, coalesce(sub, ''), coalesce(error, '') FROM outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []outboxRow
	for rows.Next() {
		var r outboxRow
		if err := rows.Scan(&r.id, &r.lid, &r.recipient, &r.state, &r.sub, &r.err); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// held counts what the device holds back (its quarantine).
func (d *mixedDevice) held(t *testing.T) int {
	t.Helper()
	db := d.db(t)
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM quarantine`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// summarize counts rows by recipient and state.
func summarize(rows []outboxRow) string {
	n := map[string]int{}
	for _, r := range rows {
		n[r.recipient+" "+r.state]++
	}
	keys := make([]string, 0, len(n))
	for k := range n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%d ", k, n[k])
	}
	return b.String()
}

// mixedWorld is one relay running this source as a release, and devices
// on several versions.
type mixedWorld struct {
	t       *testing.T
	dir     string
	head    *cli   // this source as the relay's release
	release string // that release
	addr    string
	cert    string
	stopHub func()
	hubRuns int
	admin   *mixedDevice
}

// startHub (re)starts the relay with a grace period for outdated devices.
func (w *mixedWorld) startHub(grace time.Duration) {
	w.t.Helper()
	w.hubRuns++
	w.stopHub = w.head.start(fmt.Sprintf("hub-%d.log", w.hubRuns), "hub", "serve", "--data", "hub", "--listen", w.addr,
		"--update-grace", grace.String())
	if w.hubRuns == 1 {
		waitFile(w.t, filepath.Join(w.dir, "hub", "bootstrap-invite.txt"))
		return
	}
	waitUntil(w.t, "the relay listening again", 15*time.Second, func() bool { return w.members() != nil })
}

// join enrolls a device with prog under an invite for label (the bootstrap
// admin invite when label is ""), starts its daemon and creates its person.
func (w *mixedWorld) join(home, label, agent, person string, prog *cli) *mixedDevice {
	w.t.Helper()
	var code string
	if label == "" {
		code = w.head.run("hub", "bootstrap-invite", "--raw", "--data", "hub")
		inv, err := protocol.DecodeInvite(code)
		if err != nil {
			w.t.Fatal(err)
		}
		w.cert, label = inv.CertPEM, "admin"
	} else {
		code = w.admin.run("admin", "invite", "--raw", label)
	}
	prog.run("--home", home, "join", "--agent", agent, code)
	d := &mixedDevice{home: home, address: label + "/" + agent, prog: prog}
	d.daemon(prog)
	d.run("person", "create", person)
	return d
}

// link adds a device with prog to owner's person (person link, join with
// the code, approve on owner) and starts its daemon.
func (w *mixedWorld) link(owner *mixedDevice, home, agent string, prog *cli) *mixedDevice {
	w.t.Helper()
	var code string
	for _, l := range strings.Split(owner.run("person", "link"), "\n") {
		if strings.HasPrefix(l, "agentnet-link-v2:") {
			code = l
		}
	}
	if code == "" {
		w.t.Fatal("person link printed no link code")
	}
	prog.run("--home", home, "join", "--agent", agent, code)
	label, _, _ := strings.Cut(owner.address, "/")
	d := &mixedDevice{home: home, address: label + "/" + agent, prog: prog}
	d.daemon(prog)
	var id string
	waitUntil(w.t, d.address+"'s link request", 30*time.Second, func() bool {
		for _, l := range strings.Split(owner.run("person", "links"), "\n") {
			if f := strings.Fields(l); len(f) >= 3 && f[1] == "pending" && f[2] == d.address {
				id = f[0]
				return true
			}
		}
		return false
	})
	owner.run("person", "approve", id)
	return d
}

// hubDB opens the relay's database read-only.
func (w *mixedWorld) hubDB() *sql.DB {
	w.t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(w.dir, "hub", "hub.db")+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		w.t.Fatal(err)
	}
	return db
}

// requests counts the signed requests each device made to the relay so far.
// Each carries a fresh nonce, which the relay keeps for ten minutes and
// checks before the version: refused requests count too.
func (w *mixedWorld) requests() map[string]int {
	w.t.Helper()
	db := w.hubDB()
	defer db.Close()
	rows, err := db.Query(`SELECT agent, count(*) FROM nonces GROUP BY agent`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var a string
		var n int
		if err := rows.Scan(&a, &n); err != nil {
			w.t.Fatal(err)
		}
		out[a] = n
	}
	return out
}

// custody lists the ids of the messages the relay holds for address.
func (w *mixedWorld) custody(address string) []string {
	w.t.Helper()
	db := w.hubDB()
	defer db.Close()
	rows, err := db.Query(`SELECT id FROM messages WHERE recipient = ? AND state = ? ORDER BY seq`, address, protocol.StateCustody)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// releaseSince is when this relay first ran release.
func (w *mixedWorld) releaseSince(release string) time.Time {
	w.t.Helper()
	db := w.hubDB()
	defer db.Close()
	var ms int64
	if err := db.QueryRow(`SELECT since_ms FROM releases_served WHERE version = ?`, release).Scan(&ms); err != nil {
		w.t.Fatalf("release %s not recorded as served: %v", release, err)
	}
	return time.UnixMilli(ms)
}

// members is the relay's member list as the admin's device asks for it,
// or nil while the relay does not answer.
func (w *mixedWorld) members() map[string]protocol.Member {
	w.t.Helper()
	id, err := identity.Load(filepath.Join(w.dir, w.admin.home, "identity.json"))
	if err != nil {
		w.t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(w.cert)) {
		w.t.Fatal("relay certificate")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}, Proxy: nil}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://"+w.addr+"/v1/agents", nil)
	if err != nil {
		w.t.Fatal(err)
	}
	protocol.SignRequest(req, w.admin.address, id.Sign, nil)
	req.Header.Set(protocol.VersionHeader, w.release) // as the admin's own program does
	res, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	var m protocol.Members
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&m) != nil {
		return nil
	}
	out := map[string]protocol.Member{}
	for _, e := range m.Members {
		out[e.Address] = e
	}
	return out
}

// waitUntil polls cond every 250ms until it holds, at most timeout, and
// returns how long that took.
func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for !cond() {
		if time.Since(start) > timeout {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return time.Since(start)
}

// ownRecordSubs are the own-device record carriers a receiving program
// stores without a receipt (MIXED-1, found by this gate; not version
// specific): the relay keeps them in custody after they were received.
var ownRecordSubs = map[string]bool{"read-sync": true, "invitation-sync": true, "topic-sync": true, "topic-state-sync": true}

// firstKeepsRefused is the first release that knows the relay's refusal of
// an outdated version (426 update_required): it keeps a refused send queued,
// delivers it after the update and its doctor says it is suspended. Older
// releases mark that send failed. The gate checks the older release against
// what its version can do, so it keeps passing once this release is the one
// below HEAD.
const firstKeepsRefused = "v0.8.17"

// TestMixedVersion is the release gate for "older apps must not impede
// others", with real programs in separate processes: a relay running this
// source as the next release (so its latest-only policy is on), devices on
// this source, devices on the newest published release below it (built
// from its git tag), and one device on this source still stamped as that
// older release (what this release's devices are once the next one ships).
//
//   - Within the grace period everyone works, the older release included,
//     and an own device on the older release syncs with its person's
//     current devices without flooding them (J3).
//   - After it the outdated devices are suspended while connected: they are
//     told to update, make (almost) no requests and nothing loops, and the
//     others' DMs and group turns are delivered and admitted at once, with
//     no failed or waiting copies; copies to suspended devices wait in the
//     relay's custody (J1).
//   - Updating by hand (the daemon restarted on this release) delivers the
//     backlog; a send kept while refused (this source, and releases from
//     firstKeepsRefused on) goes then, and a send a release before that
//     marked failed stays failed, nowhere delivered (J2).
func TestMixedVersion(t *testing.T) {
	below := releaseBelowHead()
	oldTag := os.Getenv("AGENTNET_MIXED_TAG")
	if oldTag == "" {
		oldTag = below
	}
	if below == "" {
		if os.Getenv("AGENTNET_REQUIRE_COMPAT") == "1" {
			t.Fatalf("no published release tag below HEAD in %s: the mixed-version gate needs the tags (git fetch --tags; CI checks out with fetch-depth 0)", compatRepo())
		}
		t.Skip("no published release tag below HEAD in this repository (a shallow clone?): git fetch --tags to run the mixed-version gate")
	}
	release := nextRelease(t, below)
	keepsRefused := !protocol.Newer(firstKeepsRefused, oldTag)
	start := time.Now()
	dir := t.TempDir()
	head := &cli{t: t, bin: buildStampedCLI(t, dir, release), dir: dir}
	stale := &cli{t: t, bin: buildStampedCLI(t, dir, oldTag), dir: dir}
	old := &cli{t: t, bin: buildTagCLI(t, oldTag), dir: dir}
	if v := old.run("version"); !strings.HasPrefix(v, "agentnet "+oldTag+" ") {
		t.Fatalf("the %s build reports %q", oldTag, v)
	}
	t.Logf("relay and current devices: this source as %s; older devices: release %s (built from its tag; keeps a refused send: %v) and this source stamped %s; builds took %s",
		release, oldTag, keepsRefused, oldTag, time.Since(start).Round(time.Second))
	t.Cleanup(func() { // registered first, so it runs after the processes stop
		if t.Failed() {
			logs, _ := filepath.Glob(filepath.Join(dir, "*.log"))
			for _, l := range logs {
				data, _ := os.ReadFile(l)
				t.Logf("--- %s\n%s", filepath.Base(l), data)
			}
		}
	})

	w := &mixedWorld{t: t, dir: dir, head: head, release: release, addr: freeAddr(t)}
	w.startHub(time.Hour) // phase 1: everyone within the grace period
	alice := w.join("alice", "", "laptop", "Alice", head)
	w.admin = alice
	bob := w.join("bob", "bob", "desk", "Bob", head)
	carol := w.join("carol", "carol", "desk", "Carol", old)
	dave := w.join("dave", "dave", "desk", "Dave", stale)

	newDM := func(to *mixedDevice) string {
		var conv string
		waitUntil(t, "a DM with "+to.address, 30*time.Second, func() bool {
			out, err := alice.stdout("dm", "new", to.address)
			conv = out
			return err == nil && len(out) == 64
		})
		return conv
	}
	ab, ac, ad := newDM(bob), newDM(carol), newDM(dave)
	var group client.GroupContext
	if err := json.Unmarshal([]byte(alice.run("group", "create", "Mixed versions")), &group); err != nil {
		t.Fatal(err)
	}
	g := group.Root.ID()
	for _, d := range []*mixedDevice{bob, carol} {
		person := strings.Fields(d.run("person"))[0]
		var prop client.GroupInvitationInfo
		waitUntil(t, "a group invitation for "+d.address, 30*time.Second, func() bool {
			out, err := alice.stdout("group", "invite", g, person)
			return err == nil && json.Unmarshal([]byte(out), &prop) == nil && prop.ID != ""
		})
		waitUntil(t, d.address+" sees the group invitation", 30*time.Second, func() bool {
			out, err := d.stdout("group", "invitations")
			return err == nil && strings.Contains(out, prop.ID)
		})
		waitUntil(t, d.address+" in the group", 30*time.Second, func() bool {
			d.try("group", "accept", prop.ID)
			return d.has(g)
		})
	}

	send := func(d *mixedDevice, conv, text string) string {
		t.Helper()
		out := d.run("dm", "send", conv, text)
		if f := strings.Fields(out); len(f) < 2 || f[1] == "failed" || f[1] == "waiting" {
			t.Fatalf("%s: dm send %q: %s", d.address, text, out)
		}
		return out
	}
	// Within the grace period everyone works, the older release included.
	send(alice, ab, "ab in grace")
	send(alice, ac, "alice to carol in grace")
	send(alice, ad, "alice to dave in grace")
	send(alice, g, "alice to the group in grace")
	waitUntil(t, "alice's turns at everyone", 30*time.Second, func() bool {
		return bob.shows(ab, "ab in grace") && carol.shows(ac, "alice to carol in grace") && dave.shows(ad, "alice to dave in grace") &&
			bob.shows(g, "alice to the group in grace") && carol.shows(g, "alice to the group in grace")
	})
	send(bob, ab, "ba in grace")
	send(carol, ac, "carol to alice in grace")
	send(carol, g, "carol to the group in grace")
	send(dave, ad, "dave to alice in grace")
	waitUntil(t, "the answers within the grace period", 30*time.Second, func() bool {
		return alice.shows(ab, "ba in grace") && alice.shows(ac, "carol to alice in grace") && alice.shows(ad, "dave to alice in grace") &&
			alice.shows(g, "carol to the group in grace") && bob.shows(g, "carol to the group in grace")
	})
	t.Logf("phase 1: conversations ready and used within the grace period after %s", time.Since(start).Round(time.Second))

	// J3: alice adds an own device on the older release, then one on this
	// release, within the grace period. Own-device sync with the older
	// one must not flood her current devices: what they hold back stays
	// bounded across member-list pushes and a reconnect of the older one.
	aliceOld := w.link(alice, "alice-old", "old", old)
	waitUntil(t, "alice's history on her older device", 60*time.Second, func() bool {
		return aliceOld.shows(ab, "ba in grace") && aliceOld.shows(g, "carol to the group in grace")
	})
	alicePhone := w.link(alice, "alice-phone", "phone", head)
	waitUntil(t, "alice's history on her new device", 60*time.Second, func() bool {
		return alicePhone.shows(ab, "ba in grace") && alicePhone.shows(g, "carol to the group in grace")
	})
	current := []*mixedDevice{alice, alicePhone}
	heldBefore := map[*mixedDevice]int{}
	for _, d := range current {
		heldBefore[d] = d.held(t)
	}
	for i := 1; i <= 3; i++ { // each join pushes a new member list to every stream
		label := fmt.Sprintf("extra%d", i)
		head.run("--home", label, "join", "--agent", "desk", alice.run("admin", "invite", "--raw", label))
		time.Sleep(time.Second)
	}
	aliceOld.daemon(old) // and the older device reconnects
	send(alice, ab, "ab after the links")
	send(bob, ab, "ba after the links")
	waitUntil(t, "the new turns on all of alice's devices", 30*time.Second, func() bool {
		return aliceOld.shows(ab, "ab after the links") && aliceOld.shows(ab, "ba after the links") && alicePhone.shows(ab, "ba after the links")
	})
	time.Sleep(3 * time.Second)
	for _, d := range current {
		held := d.held(t)
		t.Logf("J3: %s holds %d -> %d; outbox %s", d.address, heldBefore[d], held, summarize(d.outbox(t)))
		if held > heldBefore[d] || held > 2 {
			t.Errorf("J3: %s's held rows grew from %d to %d with an own device on %s", d.address, heldBefore[d], held, oldTag)
		}
	}
	// quiet checks that nothing loops: in a window without turns or joins
	// each device makes at most a few requests (a ping's acknowledgement
	// and the pass it starts) and gets at most a couple of member lists.
	quiet := func(phase string, list []*mixedDevice) {
		t.Helper()
		pushes := map[*mixedDevice]int{}
		for _, d := range list {
			pushes[d] = d.count("hub members: ")
		}
		r0, t0 := w.requests(), time.Now()
		time.Sleep(10 * time.Second)
		r1, window := w.requests(), time.Since(t0)
		for _, d := range list {
			n, p := r1[d.address]-r0[d.address], d.count("hub members: ")-pushes[d]
			t.Logf("%s: quiet %s: %s made %d signed requests, got %d member lists", phase, window.Round(time.Second), d.address, n, p)
			if n > 6 || p > 2 {
				t.Errorf("%s: %s is not quiet: %d requests and %d member lists in %s", phase, d.address, n, p, window.Round(time.Second))
			}
		}
	}
	quiet("J3", []*mixedDevice{alice, alicePhone, aliceOld, bob})

	// The others' baseline: the same exchange while nobody is suspended.
	type turn struct {
		from, to *mixedDevice
		conv     string
		text     string
	}
	exchange := func(phase string) (map[string]int, time.Duration) {
		t.Helper()
		r0 := w.requests()
		var worst time.Duration
		for _, tr := range []turn{
			{alice, bob, ab, "ab " + phase},
			{bob, alice, ab, "ba " + phase},
			{alice, bob, g, "alice to the group " + phase},
			{bob, alice, g, "bob to the group " + phase},
		} {
			send(tr.from, tr.conv, tr.text)
			took := waitUntil(t, tr.to.address+" admits "+tr.text, 30*time.Second, func() bool { return tr.to.shows(tr.conv, tr.text) })
			worst = max(worst, took)
		}
		time.Sleep(3 * time.Second) // receipts and own-device copies settle
		r1, n := w.requests(), map[string]int{}
		for a := range r1 {
			n[a] = r1[a] - r0[a]
		}
		return n, worst
	}
	baseline, baseWorst := exchange("before suspension")
	t.Logf("baseline exchange: alice %d, bob %d signed requests; slowest turn %s", baseline[alice.address], baseline[bob.address], baseWorst.Round(time.Millisecond))

	// Phase 2: the grace period ends while everyone is connected. The relay
	// restarts with a grace period, counted from when it first ran the
	// release, that ends shortly after the devices are back.
	since := w.releaseSince(release)
	w.stopHub()
	grace := time.Since(since).Round(time.Second) + 15*time.Second
	w.startHub(grace)
	end := since.Add(grace)
	devices := []*mixedDevice{alice, alicePhone, bob, carol, dave, aliceOld}
	suspended := []*mixedDevice{carol, dave, aliceOld}
	took := waitUntil(t, "everyone back within the grace period", 15*time.Second, func() bool {
		m := w.members()
		for _, d := range devices {
			if m[d.address].Presence != protocol.PresenceConnected || m[d.address].Suspended {
				return false
			}
		}
		return true
	})
	if !time.Now().Before(end) {
		t.Fatalf("reconnecting took %s: the grace period ended first", took)
	}
	connects := map[*mixedDevice]int{}
	for _, d := range suspended {
		connects[d] = d.count("connected to hub as ")
	}
	waitUntil(t, "the outdated devices suspended", time.Until(end)+20*time.Second, func() bool {
		m := w.members()
		for _, d := range suspended {
			if !m[d.address].Suspended {
				return false
			}
		}
		return true
	})
	if time.Now().Before(end) {
		t.Fatal("suspended before the grace period ended")
	}
	m := w.members()
	for _, d := range devices {
		e := m[d.address]
		t.Logf("member %s: %s, version %q, suspended %v", d.address, e.Presence, e.Version, e.Suspended)
		if wantSuspended := d.prog != head; e.Suspended != wantSuspended || e.Presence != protocol.PresenceConnected {
			t.Errorf("member %s: %+v", d.address, e)
		}
	}

	// The outdated devices are told to update. A release that knows the
	// refusal (keepsRefused) keeps a refused send queued for after the
	// update and its doctor says it is suspended; an older one (v0.8.16 and
	// before) marks the send failed and its doctor shows the relay's
	// recommendation. The expectation follows the version, not the output:
	// a known release that fell back to failing would be caught.
	updateTo := "Update AgentNet to " + release + " to continue."
	keptRefused := func(who string, d *mixedDevice, out string, err error) {
		t.Helper()
		if f := strings.Fields(out); err != nil || len(f) < 2 || f[1] != "queued" || !strings.Contains(out, updateTo) {
			t.Errorf("J1: %s's refused send was not kept with the update line: %v %s", who, err, out)
		}
		if doc, _ := d.try("doctor"); !strings.Contains(doc, "suspended  "+updateTo) {
			t.Errorf("J1: %s's doctor:\n%s", who, doc)
		}
	}
	carolSend, err := carol.try("dm", "send", ac, "carol while suspended")
	t.Logf("J1: %s (%s) dm send while suspended (err %v): %s", carol.address, oldTag, err, carolSend)
	carolID := strings.Fields(carolSend + " -")[0]
	if keepsRefused {
		keptRefused(carol.address+" ("+oldTag+")", carol, carolSend, err)
	} else {
		if !strings.Contains(carolSend, "failed") || !strings.Contains(carolSend, "426") {
			t.Errorf("J1: the %s send while suspended: %s", oldTag, carolSend)
		}
		if doc, _ := carol.try("doctor"); !strings.Contains(doc, "recommends "+release) {
			t.Errorf("J1: %s's doctor does not name %s:\n%s", carol.address, release, doc)
		}
	}
	daveSend, err := dave.try("dm", "send", ad, "dave while suspended")
	t.Logf("J1: %s (this source as %s) dm send while suspended (err %v): %s", dave.address, oldTag, err, daveSend)
	daveID := strings.Fields(daveSend + " -")[0]
	keptRefused(dave.address+" (this source)", dave, daveSend, err)

	// J1: the others are not impeded, and nothing loops.
	during, worst := exchange("while others are suspended")
	t.Logf("J1: suspended exchange: alice %d, bob %d signed requests (baseline %d, %d); slowest turn %s",
		during[alice.address], during[bob.address], baseline[alice.address], baseline[bob.address], worst.Round(time.Millisecond))
	if worst > 10*time.Second {
		t.Errorf("J1: a turn between current devices took %s", worst)
	}
	for _, d := range []*mixedDevice{alice, bob} {
		if during[d.address] > 2*baseline[d.address]+10 {
			t.Errorf("J1: %s made %d requests for the exchange (baseline %d)", d.address, during[d.address], baseline[d.address])
		}
	}
	for conv, text := range map[string]string{ac: "backlog for carol", ad: "backlog for dave"} {
		before := time.Now()
		send(alice, conv, text)
		if took := time.Since(before); took > 10*time.Second {
			t.Errorf("J1: a DM to a suspended device took %s to send", took)
		}
	}
	time.Sleep(time.Second)
	quiet("J1", devices)
	for _, d := range suspended {
		if n := d.count("connected to hub as "); n != connects[d] {
			t.Errorf("J1: %s reconnected while suspended (%d connections, %d before)", d.address, n, connects[d])
		}
	}
	// No copy failed or waits, except copies to the suspended devices, which
	// wait in custody. Own-device record carriers are never acknowledged,
	// suspension or not (MIXED-1): counted apart, by kind.
	isSuspended := map[string]bool{}
	for _, d := range suspended {
		isSuspended[d.address] = true
	}
	for _, d := range []*mixedDevice{alice, alicePhone, bob} {
		rows, unacked := d.outbox(t), map[string]int{}
		t.Logf("J1: %s outbox %s", d.address, summarize(rows))
		for _, r := range rows {
			switch {
			case r.state == "delivered", isSuspended[r.recipient] && r.state != "failed":
			case r.state == "custody" && ownRecordSubs[r.sub]:
				unacked[r.recipient+" "+r.sub]++
			default:
				t.Errorf("J1: %s's copy %s (%q) to %s is %s (%s)", d.address, r.id, r.sub, r.recipient, r.state, r.err)
			}
		}
		if len(unacked) > 0 {
			t.Logf("J1: MIXED-1: %s's own-device record carriers to current devices left in custody: %v", d.address, unacked)
		}
	}
	for _, d := range suspended {
		t.Logf("J1: the relay holds %d messages for %s", len(w.custody(d.address)), d.address)
	}
	if n := len(w.custody(carol.address)); n < 3 {
		t.Errorf("J1: the relay holds %d messages for %s, want the DM and both group turns", n, carol.address)
	}
	if n := len(w.custody(dave.address)); n < 1 {
		t.Errorf("J1: the relay holds %d messages for %s, want the DM", n, dave.address)
	}
	if carol.shows(ac, "backlog for carol") || carol.shows(g, "bob to the group while others are suspended") || dave.shows(ad, "backlog for dave") {
		t.Error("J1: a suspended device received messages")
	}

	// J2: each outdated device is updated by hand (its daemon restarted on
	// this release) and catches up.
	carol.daemon(head)
	took = waitUntil(t, "carol's backlog after her update", 30*time.Second, func() bool {
		return carol.shows(ac, "backlog for carol") && carol.shows(g, "alice to the group while others are suspended") &&
			carol.shows(g, "bob to the group while others are suspended")
	})
	t.Logf("J2: carol received her backlog %s after her update", took.Round(time.Millisecond))
	if keepsRefused {
		took = waitUntil(t, "carol's kept send after her update", 30*time.Second, func() bool { return alice.shows(ac, "carol while suspended") })
		t.Logf("J2: carol's send kept while refused (%s) was delivered %s after her update", oldTag, took.Round(time.Millisecond))
	}
	dave.daemon(head)
	took = waitUntil(t, "dave's kept send and his backlog after his update", 30*time.Second, func() bool {
		return alice.shows(ad, "dave while suspended") && dave.shows(ad, "backlog for dave")
	})
	t.Logf("J2: dave's send kept while refused was delivered, and his backlog received, %s after his update", took.Round(time.Millisecond))
	aliceOld.daemon(head)
	took = waitUntil(t, "alice's other device catches up after its update", 30*time.Second, func() bool {
		return aliceOld.shows(ab, "ba while others are suspended") && aliceOld.shows(g, "bob to the group while others are suspended")
	})
	t.Logf("J2: alice's other device caught up %s after its update", took.Round(time.Millisecond))
	waitUntil(t, "the updated devices served", 15*time.Second, func() bool {
		m := w.members()
		for _, d := range devices {
			if m[d.address].Suspended || m[d.address].Version != release {
				return false
			}
		}
		return true
	})
	for _, d := range []*mixedDevice{carol, dave} {
		if doc, _ := d.try("doctor"); strings.Contains(doc, "Update AgentNet to") {
			t.Errorf("J2: %s's doctor after its update:\n%s", d.address, doc)
		}
		waitUntil(t, "the relay's custody for "+d.address+" drained", 15*time.Second, func() bool { return len(w.custody(d.address)) == 0 })
	}
	// A send kept while refused went once updated and no copy of it is
	// failed. What a release before keepsRefused marked failed stays
	// failed and was delivered nowhere: nothing resends it behind the
	// person's back.
	sendCopies := func(d *mixedDevice, id string) []outboxRow {
		var out []outboxRow
		for _, r := range d.outbox(t) {
			if r.id == id || r.lid == id {
				out = append(out, r)
			}
		}
		return out
	}
	kept := map[*mixedDevice]string{dave: daveID}
	if keepsRefused {
		kept[carol] = carolID
	} else {
		copies := sendCopies(carol, carolID)
		if len(copies) == 0 {
			t.Errorf("J2: carol's outbox has no copy of her refused send %s", carolID)
		}
		for _, r := range copies {
			t.Logf("J2: carol's send the older release marked failed: copy to %s is %s (%s)", r.recipient, r.state, r.err)
			if r.state != "failed" {
				t.Errorf("J2: carol's refused send is %s after her update", r.state)
			}
		}
		if alice.shows(ac, "carol while suspended") {
			t.Error("J2: the send the older release reported failed was delivered")
		}
	}
	for d, id := range kept {
		var copies []outboxRow
		waitUntil(t, d.address+"'s kept send receipted", 15*time.Second, func() bool {
			copies = sendCopies(d, id)
			for _, r := range copies {
				if r.state != protocol.StateDelivered {
					return false
				}
			}
			return len(copies) > 0
		})
		for _, r := range copies {
			t.Logf("J2: %s's send kept while refused: copy to %s is %s", d.address, r.recipient, r.state)
		}
	}
	// Own-device record carriers stay in custody after their device got
	// them (MIXED-1); nothing else may.
	var known int
	time.Sleep(2 * time.Second)
	for _, id := range w.custody(aliceOld.address) {
		sub := ""
		for _, d := range []*mixedDevice{alice, alicePhone, bob} {
			for _, r := range d.outbox(t) {
				if r.id == id {
					sub = r.sub
				}
			}
		}
		if !ownRecordSubs[sub] {
			t.Errorf("J2: the relay still holds %s (%q) for %s after its update", id, sub, aliceOld.address)
		}
		known++
	}
	if known > 0 {
		t.Logf("J2: MIXED-1: the relay keeps %d own-device record carriers (read/invitation/topic sync) for %s in custody after it received them", known, aliceOld.address)
	}
	if r, ok, err := client.ReadAutoUpdate(filepath.Join(dir, dave.home)); err != nil || ok && r.Tries > 1 {
		t.Errorf("J2: dave's automatic update record %+v (%v): tried more than once", r, err)
	}
	t.Logf("mixed-version gate done in %s", time.Since(start).Round(time.Second))
}
