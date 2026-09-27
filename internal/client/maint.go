package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/blobfile"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// CleanupResult says what Cleanup removed.
type CleanupResult struct {
	SpoolFiles  int // encrypted copies of messages that failed or were abandoned
	DirectFiles int // directly received ciphertext that is not the only copy of anything kept
}

// Cleanup frees local storage the running system no longer needs: spooled
// ciphertext not belonging to a message still queued for sending, and
// directly received uploads never attached to a message (after a day). With
// saved, it also removes directly received ciphertext of attachments already
// saved as files. It refuses to run while the daemon is running.
func (a *Agent) Cleanup(saved bool) (CleanupResult, error) {
	var res CleanupResult
	release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock"))
	if errors.Is(err, lockfile.ErrLocked) {
		return res, errors.New("stop the agentnet daemon first")
	}
	if err != nil {
		return res, err
	}
	defer release()
	releaseSpool, err := lockfile.Acquire(a.spoolLockPath())
	if errors.Is(err, lockfile.ErrLocked) {
		return res, errors.New("a message with attachments is being sent; try again when it is queued")
	}
	if err != nil {
		return res, err
	}
	defer releaseSpool()

	keep := map[string]bool{}
	rows, err := a.store.db.Query(`SELECT u.blob_id FROM uploads u JOIN outbox o ON o.id = u.message_id WHERE o.state = ?`, stateQueued)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		keep[id] = true
	}
	rows.Close()
	spool := filepath.Join(a.home, "spool")
	entries, _ := os.ReadDir(spool)
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".age")
		if keep[id] || !e.Type().IsRegular() {
			continue
		}
		if err := os.Remove(filepath.Join(spool, e.Name())); err != nil {
			return res, err
		}
		res.SpoolFiles++
	}
	if _, err := a.store.db.Exec(`DELETE FROM uploads WHERE message_id IN (SELECT id FROM outbox WHERE state != ?)`, stateQueued); err != nil {
		return res, err
	}

	q := `SELECT id FROM direct_blobs d WHERE d.state = 'stored' AND
		((NOT EXISTS (SELECT 1 FROM attachments a WHERE a.blob_id = d.id) AND d.updated_at < ?)`
	if saved {
		q += ` OR NOT EXISTS (SELECT 1 FROM attachments a WHERE a.blob_id = d.id AND a.saved_path IS NULL)`
	}
	rows, err = a.store.db.Query(q+`)`, time.Now().Add(-24*time.Hour).Unix())
	if err != nil {
		return res, err
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := blobfile.RemoveIfExists(a.downloadPath(id), a.downloadPath(id)+".direct"); err != nil {
			return res, err
		}
		if _, err := a.store.db.Exec(`DELETE FROM direct_blobs WHERE id = ?`, id); err != nil {
			return res, err
		}
		res.DirectFiles++
	}
	if res.DirectFiles > 0 {
		return res, secfile.SyncDir(filepath.Dir(a.downloadPath("x")))
	}
	return res, nil
}

// Check is one doctor finding.
type Check struct {
	Name, Result string
	OK           bool
}

// Doctor checks the local installation and its Hub, returning one line per
// check with an actionable result.
func (a *Agent) Doctor(ctx context.Context) []Check {
	var out []Check
	add := func(name string, ok bool, format string, args ...any) {
		out = append(out, Check{Name: name, OK: ok, Result: fmt.Sprintf(format, args...)})
	}
	add("version", true, "agentnet %s, protocol %d", protocol.Version, protocol.ProtocolVersion)
	add("agent", true, "%s, fingerprint %s", a.Address, a.Self().Fingerprint())
	if _, err := secfile.Read(filepath.Join(a.home, "identity.json")); err != nil {
		add("keys", false, "%v", err)
	} else {
		add("keys", true, "owner-only")
	}
	if release, err := lockfile.Acquire(filepath.Join(a.home, "daemon.lock")); err == nil {
		release()
		add("daemon", false, "not running: messages arrive only while `agentnet daemon` runs")
	} else {
		add("daemon", true, "running")
	}
	var v protocol.VersionInfo
	if err := a.hub.do(ctx, "GET", "/v1/version", nil, &v); err != nil {
		add("hub", false, "%s unreachable: %v", a.hub.base, err)
	} else if v.Protocol != protocol.ProtocolVersion {
		add("hub", false, "%s speaks protocol %d, this client %d: update the older side", a.hub.base, v.Protocol, protocol.ProtocolVersion)
	} else {
		add("hub", true, "%s, version %s", a.hub.base, v.Version)
	}
	if _, err := a.directory(ctx, a.Address); err != nil {
		if errors.Is(err, ErrRevoked) {
			add("membership", false, "this agent has been revoked")
		} else {
			add("membership", false, "%v", err)
		}
	} else {
		add("membership", true, "active")
	}
	switch r, err := a.HubRelease(ctx); {
	case err != nil:
		add("update", true, "the Hub gives no client recommendation (%v)", err)
	case r.Version == "":
		add("update", true, "no client version recommended by the Hub")
	case r.Version == protocol.Version:
		add("update", true, "this build (%s) is the one the Hub recommends", r.Version)
	default:
		add("update", true, "the Hub recommends %s; this is %s: see agentnet help update and %s", r.Version, protocol.Version, r.URL)
	}
	switch r, err := a.Responder(); {
	case err != nil:
		add("responder", false, "%v", err)
	case r == nil:
		if chosen, _ := a.ResponderChosen(); chosen {
			add("responder", true, "manual only (chosen): questions and tasks wait for you")
		} else {
			add("responder", true, "not chosen yet: questions and tasks wait for you; see agentnet responder list")
		}
	default:
		if _, err := exec.LookPath(Harnesses[r.Harness].bin); err != nil {
			add("responder", false, "%s selected but %q is not on PATH", r.Harness, Harnesses[r.Harness].bin)
		} else {
			add("responder", true, "%s in %s", r.Harness, r.Dir)
		}
	}
	return out
}
