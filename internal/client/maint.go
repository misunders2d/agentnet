package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
// ciphertext not belonging to a message still queued or waiting to be sent, and
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
	// A frozen request awaiting receiver setup, and a conversation message
	// waiting until its recipient can read it, still own their exact
	// ciphertext: it is uploaded once they are released, never spooled again.
	rows, err := a.store.db.Query(`SELECT u.blob_id FROM uploads u JOIN outbox o ON o.id = u.message_id WHERE o.state IN (?, ?, ?)`, stateQueued, stateReceiverWaiting, stateConvWaiting)
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
	if _, err := a.store.db.Exec(`DELETE FROM uploads WHERE message_id IN (SELECT id FROM outbox WHERE state NOT IN (?, ?, ?))`, stateQueued, stateReceiverWaiting, stateConvWaiting); err != nil {
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
	var hubErr *HubError
	switch r, err := a.HubRelease(ctx); {
	case errors.As(err, &hubErr) && hubErr.Status == http.StatusNotFound:
		add("update", true, "recommendation endpoint unavailable (an older Hub may not support it)")
	case err != nil:
		add("update", true, "recommendation unknown or unavailable: %v", err)
	case r.Version == "":
		add("update", true, "no client version recommended by the Hub")
	case r.Version == protocol.Version:
		add("update", true, "this build (%s) is the one the Hub recommends", r.Version)
	case !protocol.Newer(r.Version, protocol.Version):
		add("update", true, "the Hub recommends %s; this build is %s: no comparable newer recommendation", r.Version, protocol.Version)
	default:
		add("update", true, "the Hub recommends %s; this is %s: see agentnet help update and %s", r.Version, protocol.Version, r.URL)
	}
	// After the requests above: a refusal one of them met is recorded.
	if u, ok := a.UpdateRequired(); ok {
		add("suspended", false, "%s See agentnet help update.", a.ExplainUpdateRequired(u))
	} else {
		add("auto-update", true, "%s", a.autoUpdateWords())
	}
	switch r, err := a.Responder(); {
	case err != nil:
		add("responder", false, "%v", err)
	case r == nil:
		if chosen, _ := a.ResponderChosen(); chosen {
			add("responder", true, "manual only (chosen): questions and tasks wait for you")
		} else {
			add("responder", true, "not chosen yet: this computer has no default agent, so questions and tasks wait for you; set one: %s (agentnet responder list shows the programs found)", DefaultAgentFix(a.suggestedHarness()))
		}
	default:
		if _, err := exec.LookPath(Harnesses[r.Harness].bin); err != nil {
			add("responder", false, "%s selected but %q is not on PATH", r.Harness, Harnesses[r.Harness].bin)
		} else if n, err := a.Approvals(); err != nil {
			add("responder", false, "%v", err)
		} else if n == 0 {
			add("responder", true, "%s in %s; %s", r.Harness, r.Dir, NoApprovals)
		} else {
			add("responder", true, "%s in %s; answers questions from %d approved agent(s)", r.Harness, r.Dir, n)
		}
	}
	// Held questions, tasks awaiting acceptance and needs_human items wait
	// for a person; where no desktop notification can be shown (a server),
	// this line is how they come to light.
	if items, err := a.Review(); err != nil {
		add("review", false, "%v", err)
	} else if len(items) > 0 {
		add("review", true, "%d item(s) wait for your decision: agentnet inbox --review", len(items))
	} else {
		add("review", true, "nothing waits for your decision")
	}
	// What else waits here, apart from review: each says where it is
	// decided (agentnet inbox --review lists them all).
	if w, err := a.Waiting(); err != nil {
		add("waiting", false, "%v", err)
	} else {
		if n := len(w.AgentRequests); n > 0 {
			why, stuck := w.AgentRequests[0].Why, false
			for _, r := range w.AgentRequests {
				if r.Stuck { // nothing here would run it: that first
					why, stuck = r.Why, true
					break
				}
			}
			add("agent requests", !stuck, "%d request(s) to your agent have not run: %s (agentnet inbox --review)", n, why)
		}
		if n := len(w.AgentInvites) + len(w.GroupInvites); n > 0 {
			add("invitations", true, "%d invitation(s) wait for your decision (agentnet inbox --review)", n)
		}
		if n := len(w.Links); n > 0 {
			add("device links", true, "%d device(s) ask to be linked to your person: agentnet person links", n)
		}
		if n := len(w.Held); n > 0 {
			add("held messages", true, "%d message(s) held here, not shown (agentnet inbox --review says why)", n)
		}
	}
	// A service nobody sits at needs someone who decides its waiting
	// requests from their own devices: a steward (MEL-532).
	if role, _ := a.Role(); role == "service" {
		if who, err := a.store.deciders(); err != nil {
			add("stewards", false, "%v", err)
		} else if len(who) == 0 {
			add("stewards", false, "nobody decides this machine's waiting requests from their own devices yet; whoever installed it can name a steward here: agentnet operator grant --person ADDRESS (one device of that person; all its devices then decide)")
		} else {
			var names []string
			for _, d := range who {
				if d.Label != "" {
					names = append(names, fmt.Sprintf("%q (all their devices)", d.Label))
				} else {
					names = append(names, d.Address)
				}
			}
			add("stewards", true, "%s decide this machine's waiting requests from their own devices", strings.Join(names, ", "))
		}
	}
	if why, err := a.ReviewToHealth(); err != nil {
		add("review-to", false, "%v", err)
	} else if why != "" {
		add("review-to", false, "%s", why)
	} else if to, _ := a.ReviewTo(); to != "" {
		add("review-to", true, "notices go to %s; none failed", to)
	}
	return out
}
