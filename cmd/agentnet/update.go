package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Self-update: `agentnet update` replaces this executable with an official
// release built by scripts/build.sh, checked against that release's
// SHA256SUMS. The origin is fixed here; nothing received (a Hub
// recommendation, a message) chooses what is downloaded. The new program
// must be able to open this home's database. Afterwards this home's daemon,
// if it runs this file, is asked to switch to it (client/restart.go): it
// does so once no job runs. Nothing else running is stopped or restarted,
// and no database is changed by the update itself.

// executable returns this program's file. Tests replace it.
var executable = os.Executable

// releaseBase is the project's release origin. Tests replace it.
var releaseBase = "https://github.com/misunders2d/agentnet/releases"

// updateClient makes the downloads (system CAs); every redirect hop must
// stay HTTPS. Tests replace its transport.
var updateClient = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: httpsOnly}

func httpsOnly(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a non-HTTPS redirect to %s", req.URL.Host)
	}
	if len(via) >= 10 {
		return errors.New("too many redirects")
	}
	return nil
}

const (
	maxSums  = 1 << 20   // SHA256SUMS
	maxAsset = 256 << 20 // one release binary
)

var releaseTagPattern = regexp.MustCompile(`^v(\d{1,6})\.(\d{1,6})\.(\d{1,6})$`)

// buildPattern is a development build's stamp: a release tag followed by
// `git describe` commits (-N-gHASH), or by +anything, and/or -dirty.
var buildPattern = regexp.MustCompile(`^(v\d{1,6}\.\d{1,6}\.\d{1,6})(?:-\d{1,9}-g[0-9a-f]{4,40}|\+[0-9A-Za-z.]{1,64})?(?:-dirty)?$`)

// devBase returns the release a development build was made after. Such a
// build is that release plus changes, so only a newer release is an update.
// The base is only a lower bound when stamped with git describe without
// --tags, which skips lightweight release tags: the schema check below still
// guards what gets installed.
func devBase(version string) ([3]int, bool) {
	m := buildPattern.FindStringSubmatch(version)
	if m == nil || m[1] == version {
		return [3]int{}, false
	}
	return parseRelease(m[1])
}

// parseRelease returns the numbers of a release tag vX.Y.Z.
func parseRelease(tag string) ([3]int, bool) {
	m := releaseTagPattern.FindStringSubmatch(tag)
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func olderRelease(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// assetName is this platform's release file, as scripts/build.sh names it.
func assetName() string {
	name := "agentnet-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func runUpdate(ctx context.Context, home string, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "only show what would be installed")
	status := fs.Bool("status", false, "show whether this home's daemon switched after the last update")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *status {
		return updateStatus(home)
	}
	if fs.NArg() > 1 {
		return errors.New("usage: update [--check] [vX.Y.Z]")
	}
	current := protocol.Version
	cur, isRelease := parseRelease(current)
	base, isDev := devBase(current)
	target := fs.Arg(0)
	switch {
	case target != "":
		if _, ok := parseRelease(target); !ok {
			return fmt.Errorf("%q is not a release version (vX.Y.Z)", target)
		}
	case !isRelease && !isDev:
		return fmt.Errorf("this is a development build (%s) not made from a release; name the release to install: agentnet update vX.Y.Z", current)
	default:
		latest, err := latestRelease(ctx)
		if err != nil {
			return fmt.Errorf("cannot find the latest release: %w", err)
		}
		target = latest
	}
	tv, _ := parseRelease(target)
	if target == current {
		fmt.Printf("agentnet %s is already installed\n", current)
		return nil
	}
	if isRelease && olderRelease(tv, cur) {
		return fmt.Errorf("%s is older than this %s; downgrades are not supported (databases only move forward; see agentnet help update)", target, current)
	}
	if isDev && !olderRelease(base, tv) {
		return fmt.Errorf("%s is not newer than %s, the release this development build (%s) was made after; installing it could take code and database backwards, so nothing was changed", target, releaseName(base), current)
	}
	started, err := executable()
	if err != nil {
		return fmt.Errorf("cannot find this program's file: %w", err)
	}
	// The file to replace is the symlink's target, not the link.
	exe, err := filepath.EvalSymlinks(started)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %w", started, err)
	}
	via := ""
	if exe != started {
		via = " (reached through " + started + ")"
	}
	fmt.Printf("current %s, target %s, file %s%s (%s)\n", current, target, exe, via, assetName())
	if *check {
		return nil
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return errors.New("this runs inside a container (/.dockerenv): update the container image instead of the file in it")
	}
	// One update of this file at a time, whichever home started it.
	release, err := lockfile.Acquire(updateLockPath(exe))
	if err != nil {
		return fmt.Errorf("another update of %s is running (or its directory is not writable): %w", exe, err)
	}
	defer release()
	// Decisions above used this program's own version. Another update may
	// have replaced the file before the lock was taken, so check, under the
	// lock, that the file still is this version before replacing it.
	if now, err := fileVersion(ctx, exe); err != nil || now != versionLine(current) {
		return fmt.Errorf("%s changed since this update started (it reports %q; this is agentnet %s); nothing was changed: run agentnet update again", exe, now, current)
	}
	staged, protoLine, err := stageRelease(ctx, filepath.Dir(exe), target)
	if err != nil {
		return err
	}
	if err := checkSchema(ctx, home, staged, target, isRelease && olderRelease(cur, tv)); err != nil {
		os.Remove(staged)
		return err
	}
	if err := replaceExecutable(exe, staged); err != nil {
		os.Remove(staged)
		return err
	}
	fmt.Printf("updated %s: %s -> %s; the previous file is kept as %s\n", exe, current, protoLine, exe+".old")
	if !strings.HasSuffix(protoLine, fmt.Sprintf("(protocol %d)", protocol.ProtocolVersion)) {
		fmt.Printf("note: the protocol generation changed; run agentnet doctor after restarting to see whether your Hub matches\n")
	}
	asked := switchDaemon(home, exe, current, target)
	reportRunning(home, exe, asked)
	return nil
}

// updateStatus shows what became of the last switch request of this home,
// as the record it is, and what runs now: whether a daemon holds the home
// and whether the process that switched is still alive. It never presents
// the record as the current state.
func updateStatus(home string) error {
	running := false
	if st, err := os.Stat(home); err == nil && st.IsDir() {
		if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
			release()
		} else {
			running = true
		}
	}
	if pending, err := client.PendingUpdateSwitch(home); err == nil && pending != "" {
		if running {
			fmt.Printf("switch to agentnet %s requested: pending (the daemon switches once no job runs)\n", pending)
		} else {
			fmt.Printf("switch to agentnet %s requested; no daemon is running for this home now, so it completes when the daemon starts\n", pending)
		}
		return nil
	}
	act, ok, err := client.ReadUpdateActivation(home)
	switch {
	case err != nil:
		return err
	case !ok:
		fmt.Println("no switch was requested in this home")
		return nil
	case act.Result == client.ActivationRunning:
		fmt.Printf("last switch: at %s the daemon started as agentnet %s (process %d)\n", act.At.Format(time.RFC3339), act.Running, act.PID)
	default:
		fmt.Printf("last switch: at %s the switch to agentnet %s was %s: %s\n", act.At.Format(time.RFC3339), act.To, strings.ReplaceAll(act.Result, "_", " "), act.Detail)
	}
	alive, known := processAlive(act.PID)
	switch {
	case !running:
		fmt.Println("now: no daemon is running for this home")
	case act.Result == client.ActivationRunning && known && alive:
		fmt.Printf("now: a daemon is running, and process %d is still alive\n", act.PID)
	case act.Result == client.ActivationRunning && known:
		fmt.Printf("now: a daemon is running, but not process %d; its version is not checked here\n", act.PID)
	default:
		fmt.Println("now: a daemon is running; which version is not checked here")
	}
	return nil
}

func releaseName(v [3]int) string { return fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2]) }

var schemaLine = regexp.MustCompile(`^schema (\d{1,5})$`)

// checkSchema refuses a program that could not open this home's database:
// one that supports fewer schema steps than the database has. A program too
// old to say (it predates `version --schema`) is accepted only as a newer
// release over a release, where steps only grow.
func checkSchema(ctx context.Context, home, staged, target string, releaseForward bool) error {
	have, found, err := client.HomeSchema(home)
	if err != nil {
		return fmt.Errorf("cannot read the schema of this home's database (%v); nothing was changed", err)
	}
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(vctx, staged, "version", "--schema").Output()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	supports := -1
	if len(lines) == 2 {
		if m := schemaLine.FindStringSubmatch(strings.TrimSpace(lines[1])); m != nil {
			supports, _ = strconv.Atoi(m[1])
		}
	}
	switch {
	case supports >= 0 && found && supports < have:
		return fmt.Errorf("agentnet %s opens home databases up to schema %d, but this home's is at %d; installing it would leave the daemon unable to start, so nothing was changed", target, supports, have)
	case supports < 0 && !releaseForward:
		return fmt.Errorf("agentnet %s does not say which home databases it can open, so it cannot be checked against this one; nothing was changed", target)
	}
	return nil
}

// switchDaemon asks this home's running daemon to switch to exe, now target,
// and reports only what it observes: a switch it has seen done, pending
// (a job is running), or what stopped it. It reports whether it asked.
func switchDaemon(home, exe, from, target string) bool {
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		return false
	}
	if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
		release()
		fmt.Printf("this home's daemon is not running; started, it runs agentnet %s\n", target)
		return false
	}
	r := client.UpdateRequest{ID: protocol.NewID(), Exe: exe, From: from, To: target, At: time.Now()}
	if err := client.RequestUpdateSwitch(home, r); err != nil {
		fmt.Printf("could not ask this home's daemon to switch (%v); restart it to run agentnet %s\n", err, target)
		return false
	}
	deadline := time.Now().Add(switchWait)
	for time.Now().Before(deadline) {
		if act, ok, _ := client.ReadUpdateActivation(home); ok && act.ID == r.ID {
			switch {
			case act.Result == client.ActivationRunning && act.Running == target:
				fmt.Printf("this home's daemon now runs agentnet %s (seen: it restarted as process %d); an open messenger page reconnects at the same address\n", target, act.PID)
			case act.Result == client.ActivationFailed:
				fmt.Printf("this home's daemon stopped to switch, but agentnet %s did not take over: %s\n", target, act.Detail)
			default:
				fmt.Printf("this home's daemon did not switch: %s\n", act.Detail)
			}
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Printf("asked this home's daemon to switch to agentnet %s: pending. It starts no new job and switches once the running one has finished (agentnet inbox shows it); agentnet update --status tells whether it did\n", target)
	return true
}

// switchWait bounds how long update waits to see the daemon switch.
var switchWait = 20 * time.Second

// versionLine is what `agentnet version` prints for version v.
func versionLine(v string) string {
	return fmt.Sprintf("agentnet %s (protocol %d)", v, protocol.ProtocolVersion)
}

// fileVersion runs path's `version` (bounded) and returns its first line.
func fileVersion(ctx context.Context, path string) (string, error) {
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, path, "version").Output()
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line, err
}

// updateLockPath is the lock that serializes updates of exe: a hidden
// file next to it, so it covers every home and user that updates this file.
func updateLockPath(exe string) string {
	return filepath.Join(filepath.Dir(exe), "."+filepath.Base(exe)+".update-lock")
}

// latestRelease reads the tag the project's "latest release" page redirects
// to, without following it.
func latestRelease(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", releaseBase+"/latest", nil)
	if err != nil {
		return "", err
	}
	c := *updateClient
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	tag := loc[strings.LastIndex(loc, "/")+1:]
	if resp.StatusCode/100 != 3 || !strings.Contains(loc, "/releases/tag/") {
		return "", fmt.Errorf("unexpected answer %s", resp.Status)
	}
	if _, ok := parseRelease(tag); !ok {
		return "", fmt.Errorf("latest release %q is not vX.Y.Z", tag)
	}
	return tag, nil
}

// fetch downloads url into w, refusing more than max bytes.
func fetch(ctx context.Context, url string, max int64, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := updateClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	if n > max {
		return fmt.Errorf("%s: larger than %d bytes", url, max)
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return fmt.Errorf("%s: truncated (%d of %d bytes)", url, n, resp.ContentLength)
	}
	return nil
}

// stageRelease downloads the release asset into dir (the executable's own
// directory, so the replacement is a rename on one filesystem), checks it
// against SHA256SUMS and runs it to confirm its version. It returns the
// staged path and the version line; on any failure nothing is left behind.
func stageRelease(ctx context.Context, dir, tag string) (path, staged string, err error) {
	base := releaseBase + "/download/" + tag + "/"
	var sums bytes.Buffer
	if err := fetch(ctx, base+"SHA256SUMS", maxSums, &sums); err != nil {
		return "", "", err
	}
	want := ""
	sc := bufio.NewScanner(&sums)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == assetName() {
			want = strings.ToLower(f[0])
		}
	}
	if len(want) != 64 {
		return "", "", fmt.Errorf("release %s has no checksum for %s", tag, assetName())
	}
	f, err := os.CreateTemp(dir, ".agentnet-update-*")
	if err != nil {
		return "", "", fmt.Errorf("cannot write in %s (%v); if this copy was installed some other way, update it that way", dir, err)
	}
	stagedName := f.Name() // the results are cleared on failure, so keep it here
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(stagedName)
		}
	}()
	path = stagedName
	h := sha256.New()
	if err = fetch(ctx, base+assetName(), maxAsset, io.MultiWriter(f, h)); err != nil {
		return "", "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", "", fmt.Errorf("%s does not match the release checksum; nothing was changed", assetName())
	}
	if err = f.Sync(); err != nil {
		return "", "", err
	}
	if err = f.Close(); err != nil {
		return "", "", err
	}
	if err = os.Chmod(path, 0o755); err != nil {
		return "", "", err
	}
	line, runErr := fileVersion(ctx, path)
	if runErr != nil || !strings.HasPrefix(line, "agentnet "+tag+" (protocol ") {
		err = fmt.Errorf("the downloaded %s reports %q, not %s; nothing was changed", assetName(), line, tag)
		return "", "", err
	}
	return path, line, nil
}

// replaceExecutable puts staged in place of exe and keeps the previous
// file as exe.old. On Unix the previous file is first hard-linked to
// exe.old and staged is then renamed over exe, so exe always exists. Windows
// cannot replace a running .exe, but can rename it: the previous file is
// renamed to exe.old and staged into place, and put back if that fails
// (a crash between the two leaves only exe.old; rename it back).
func replaceExecutable(exe, staged string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot remove %s from an earlier update (is it still running? restart what uses it, then retry): %w", old, err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Link(exe, old); err == nil {
			if err := os.Rename(staged, exe); err != nil {
				os.Remove(old)
				return fmt.Errorf("installing failed; nothing was changed: %w", err)
			}
			return nil
		}
		// No hard links here (some filesystems): fall back to renames.
	}
	if err := os.Rename(exe, old); err != nil {
		return fmt.Errorf("cannot move the current program aside: %w", err)
	}
	if err := os.Rename(staged, exe); err != nil {
		if rerr := os.Rename(old, exe); rerr != nil {
			return fmt.Errorf("installing failed (%v) and restoring failed (%v): the previous program is at %s", err, rerr, old)
		}
		return fmt.Errorf("installing failed, the previous program is back in place: %w", err)
	}
	return nil
}

// reportRunning says what still runs the previous program. It stops
// nothing: restarting interrupts running jobs, so it is the person's call.
func reportRunning(home, exe string, asked bool) {
	if st, err := os.Stat(home); err == nil && st.IsDir() && !asked {
		if release, err := lockfile.Acquire(filepath.Join(home, "daemon.lock")); err == nil {
			release()
		} else {
			fmt.Printf("a daemon for %s is running; it keeps the program it was started with until restarted. Restart it when no job is running (agentnet inbox shows running jobs):\n", home)
			fmt.Println("  Linux: systemctl --user restart agentnet   macOS: launchctl unload/load the agentnet plist   Windows: schtasks /end /tn agentnet, then schtasks /run /tn agentnet")
		}
	}
	if runtime.GOOS == "linux" {
		var pids []string
		entries, _ := os.ReadDir("/proc")
		for _, e := range entries {
			if pid, err := strconv.Atoi(e.Name()); err != nil || pid == os.Getpid() {
				continue // not a process, or this updater itself
			}
			// The previous file shows as exe.old after a rename, or as
			// "exe (deleted)" when it was replaced while still linked as
			// exe.old.
			if target, err := os.Readlink("/proc/" + e.Name() + "/exe"); err == nil && (target == exe+".old" || target == exe+" (deleted)") {
				pids = append(pids, e.Name())
			}
		}
		if len(pids) > 0 {
			fmt.Printf("processes still running the previous file (%s): %s; restart them to use the new version\n", exe+".old", strings.Join(pids, " "))
		}
		return
	}
	fmt.Println("any agentnet process started before the update (a daemon of any home, a Hub) keeps the previous version until restarted")
}
