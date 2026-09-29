package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The Hub's operator may recommend a client version. The daemon receives it
// on its stream (on connect and when it changes), saves it, and tells the
// person once per recommendation (content-free desktop notice) and each
// harness session once (hook line). Builds are compared only for equality:
// version strings such as git hashes or "dev" have no order. AgentNet never
// downloads or runs anything; the URL is the operator's recommendation, not
// authority to install.

// saveRelease stores a pushed recommendation (an empty version clears it).
func (a *Agent) saveRelease(data []byte) error {
	var r protocol.Release
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if r.Version == "" {
		return a.store.deleteConfig("release")
	}
	if err := r.Validate(); err != nil {
		return err
	}
	raw, _ := json.Marshal(r)
	return a.store.setConfig(map[string]string{"release": string(raw)})
}

// Release returns the recommendation last received from the Hub, if any.
func (a *Agent) Release() (protocol.Release, bool) { return a.store.release() }

func (s *store) release() (protocol.Release, bool) {
	v, err := s.config("release")
	if err != nil {
		return protocol.Release{}, false
	}
	var r protocol.Release
	if json.Unmarshal([]byte(v), &r) != nil || r.Validate() != nil {
		return protocol.Release{}, false
	}
	return r, true
}

// updateRecommended returns the recommendation if it names a release newer
// than this build: never an older one (a preview ahead of the stable
// recommendation is not told to go back) or a version it cannot compare.
func (s *store) updateRecommended() (protocol.Release, bool) {
	r, ok := s.release()
	return r, ok && protocol.Newer(r.Version, protocol.Version)
}

// notifyRelease shows one desktop notice per recommendation. It runs on the
// worker, never on the stream reader; it is marked only when shown, and a
// failed attempt is not repeated until the next daemon start.
func (a *Agent) notifyRelease() {
	r, ok := a.store.updateRecommended()
	if !ok {
		return
	}
	if done, _ := a.store.config("release_notified"); done == r.Key() || a.releaseTried == r.Key() {
		return
	}
	a.releaseTried = r.Key()
	if err := a.notify("AgentNet", "An AgentNet update is recommended. Ask your coding agent to check it.", nil, nil); err != nil {
		a.Logf("update recommended (%s); desktop notification not shown (%v); see `agentnet version`", r.Version, err)
		return
	}
	if err := a.store.setConfig(map[string]string{"release_notified": r.Key()}); err != nil {
		a.Logf("release: %v", err)
	}
}

// releaseNudge is the hook line for a session not yet told about the
// current recommendation. The operator's note is left out: it is for
// people, not instructions for a model.
func (a *Agent) releaseNudge(ev HookEvent) (line, key string, err error) {
	r, ok := a.store.updateRecommended()
	if !ok {
		return "", "", nil
	}
	var seen sql.NullString
	err = a.store.db.QueryRow(`SELECT release_seen FROM attention WHERE harness = ? AND session = ?`, ev.Harness, ev.Session).Scan(&seen)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	if seen.String == r.Key() {
		return "", "", nil
	}
	return fmt.Sprintf("AgentNet: your Hub's operator recommends AgentNet %s; this is %s. How to update: `agentnet help update`; operator's page: %s. "+
		"Ask the person before updating unless they have already authorized it.", r.Version, protocol.Version, r.URL), r.Key(), nil
}

func (s *store) setReleaseSeen(harness, session, key string) error {
	_, err := s.db.Exec(`UPDATE attention SET release_seen = ? WHERE harness = ? AND session = ?`, key, harness, session)
	return err
}

// LocalRelease reads the recommendation saved in home without creating
// anything or contacting the Hub; ok is false when there is none (or no
// enrolled agent there).
func LocalRelease(home string) (protocol.Release, bool) {
	_, dbPath := paths(home)
	if _, err := os.Stat(dbPath); err != nil {
		return protocol.Release{}, false
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return protocol.Release{}, false
	}
	defer db.Close()
	return (&store{db: db}).release()
}

// SetRelease sets (or with an empty version clears) the Hub's client
// recommendation; admins only.
func (a *Agent) SetRelease(ctx context.Context, r protocol.Release) (protocol.Release, error) {
	req := protocol.ReleaseRequest{Release: r, Clear: r.Version == ""}
	var out protocol.Release
	err := a.hub.do(ctx, "POST", "/v1/admin/release", req, &out)
	return out, err
}

// HubRelease asks the Hub for its current recommendation.
func (a *Agent) HubRelease(ctx context.Context) (protocol.Release, error) {
	var out protocol.Release
	err := a.hub.do(ctx, "GET", "/v1/release", nil, &out)
	return out, err
}
