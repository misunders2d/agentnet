package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// This device suspended (owner policy "latest only", v0.8.17). A Hub that
// requires a newer AgentNet than this device runs refuses its requests with
// HTTP 426 {"error":"update_required","latest":"vX.Y.Z","url":...} and, on
// its push stream, sends an update_required event and then pings only. That
// is availability, never authority: it grants or withholds nothing else,
// and nothing it names is downloaded (the update's origin is fixed: see
// autoupdate.go).
//
// The refusal is recorded (config update_required, with the version it
// refused), so the person and their coding agents are told "Update AgentNet
// to vX to continue" (doctor, inbox, the hook line, the page), and the
// daemon's automatic update is asked to install vX. The daemon then sends
// the Hub only what it still takes from a refused device (updateAllowed):
// its stream, that stream's ping acknowledgements and the version probe,
// and what lets admitted work drain (receipts, answers and results, the
// lookups they make first and the upload of their files). Its retry pass
// sends only those, so nothing loops, and everything else stays queued for
// the updated program. The record ends when another version runs here, or
// when the Hub serves this device again: its stream brings members, a
// message or a receipt, which it sends only to a device it serves. An
// answered request proves nothing: the Hub answers a refused device some
// requests too.

// codeUpdateRequired is the Hub's refusal of this build.
const codeUpdateRequired = "update_required"

// ErrUpdateRequired matches (errors.Is) the Hub's refusal of this build:
// it serves this device again only once it runs a newer AgentNet.
var ErrUpdateRequired = errors.New("the Hub requires a newer AgentNet")

const updateRequiredKey = "update_required" // config: the Hub's refusal of the build that ran

// releaseTag is a published release's version, the only kind recorded or
// shown as the version to update to.
var releaseTag = regexp.MustCompile(`^v\d{1,6}\.\d{1,6}\.\d{1,6}$`)

// UpdateRequired is the Hub's refusal of this device's build.
type UpdateRequired struct {
	Latest  string `json:"latest"`        // the release to run (vX.Y.Z); "" when the Hub named none
	URL     string `json:"url,omitempty"` // its page as the Hub named it: shown, never downloaded
	Running string `json:"running"`       // the version the Hub refused
	At      int64  `json:"at"`            // unix seconds
}

// Message is what people are told.
func (u UpdateRequired) Message() string {
	if u.Latest == "" {
		return "Update AgentNet to continue."
	}
	return "Update AgentNet to " + u.Latest + " to continue."
}

// serves says what the refusal means for device address.
func (u UpdateRequired) serves(address string) string {
	return "Your Hub serves this device (" + address + ", AgentNet " + u.Running + ") again only once it runs that version: until then messages from here stay queued, and messages to it wait on the Hub."
}

// ExplainUpdateRequired is u's message with what it means here and what
// the automatic update does about it, for doctor and commands.
func (a *Agent) ExplainUpdateRequired(u UpdateRequired) string {
	return u.Message() + " " + u.serves(a.Address) + " " + a.autoUpdateWords()
}

// updateRequiredError is the Hub's refusal as an error, keeping only what
// may be shown: a release tag and an https page. The Hub's own words are
// not kept: the hook line goes to models.
func updateRequiredError(latest, url string) *HubError {
	if !releaseTag.MatchString(latest) {
		latest = ""
	}
	if (protocol.Release{Version: "v0.0.0", URL: url}).Validate() != nil {
		url = ""
	}
	return &HubError{Status: http.StatusUpgradeRequired, Code: codeUpdateRequired, Msg: UpdateRequired{Latest: latest}.Message(), Latest: latest, URL: url}
}

// updateGate is a Hub connection's view of the Hub's refusal of this build.
type updateGate struct {
	mu      sync.Mutex
	refusal *HubError       // the Hub's standing refusal; nil while it serves this device
	known   bool            // the Hub answered this process (else refusal is as recorded)
	hold    bool            // refuse here what the Hub would refuse (the daemon)
	refused func(*HubError) // told of each refusal the Hub gives, outside the lock
}

// drainsWork reports whether a refused device may still post a message of
// outer kind: an answer or a result finishes what was admitted before.
func drainsWork(kind string) bool { return kind == envelope.KindAnswer || kind == envelope.KindResult }

// updateAllowed is what the Hub still takes from a device it refuses (its
// suspended allowlist, contract REVISION 2.2): the push stream, which says
// when it is served again, that stream's ping acknowledgements, the version
// probe and the recommendation; and what lets admitted work drain: receipts
// of what was received, the read-only lookups an answer or a result makes
// before it is stored (a member's directory entry, sessions and profile,
// and a person's chain: a conversation reply refreshes each member's person
// first), the upload of its files (reserve, chunks, the upload's state,
// complete; never a download), and posts of answers and results (by the
// posted message's outer kind). body is the request's.
func updateAllowed(method, path string, body []byte) bool {
	p, _, _ := strings.Cut(path, "?")
	seg := strings.Split(strings.TrimPrefix(p, "/v1/"), "/")
	switch {
	case method == http.MethodGet && (p == "/v1/stream" || p == "/v1/version" || p == "/v1/release"),
		method == http.MethodPost && p == "/v1/stream/ack":
		return true
	case method == http.MethodPost && p == "/v1/messages":
		var env struct {
			Kind string `json:"kind"`
		}
		return json.Unmarshal(body, &env) == nil && drainsWork(env.Kind)
	case method == http.MethodPost && len(seg) == 3 && seg[0] == "messages" && seg[1] != "" && seg[2] == "ack":
		return true
	case method == http.MethodGet && seg[0] == "agents" && (len(seg) == 3 || len(seg) == 4 && (seg[3] == "sessions" || seg[3] == "profile")):
		return seg[1] != "" && seg[2] != ""
	case method == http.MethodGet && len(seg) == 3 && seg[0] == "persons" && seg[2] == "chain":
		return seg[1] != ""
	case method == http.MethodPost && p == "/v1/blobs":
		return true
	case seg[0] == "blobs" && len(seg) == 2 && seg[1] != "":
		return method == http.MethodGet || method == http.MethodPut
	case method == http.MethodPost && len(seg) == 3 && seg[0] == "blobs" && seg[1] != "" && seg[2] == "complete":
		return true
	}
	return false
}

// before returns the refusal a held request gets without asking the Hub.
func (g *updateGate) before(method, path string, body []byte) error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.hold || g.refusal == nil || updateAllowed(method, path, body) {
		return nil
	}
	e := *g.refusal
	return &e
}

// beforePost returns the refusal a held message of outer kind gets here,
// before anything of it is sent: the files of a message the Hub would
// refuse wait with it, so no retry pass uploads them again and again.
func (g *updateGate) beforePost(kind string) error {
	if g == nil || drainsWork(kind) {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.hold || g.refusal == nil {
		return nil
	}
	e := *g.refusal
	return &e
}

// after notes the Hub's answer: an update_required refusal stands from now.
// Nothing else ends one here: the Hub answers a refused device some
// requests (updateAllowed), so only its stream says it serves this device
// again (updateServed).
func (g *updateGate) after(err error) {
	var he *HubError
	if g != nil && errors.As(err, &he) && he.Code == codeUpdateRequired {
		g.set(he)
	}
}

func (g *updateGate) set(he *HubError) {
	e := *he
	g.mu.Lock()
	g.refusal, g.known = &e, true
	refused := g.refused
	g.mu.Unlock()
	if refused != nil {
		refused(&e)
	}
}

// clear ends a standing refusal: the Hub served this device. It reports
// whether there was one.
func (g *updateGate) clear() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	was := g.refusal != nil
	g.refusal, g.known = nil, true
	return was
}

// holding reports whether requests are refused here now.
func (g *updateGate) holding() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.hold && g.refusal != nil
}

func (g *updateGate) setHold(on bool) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.hold = on
	g.mu.Unlock()
}

// current is the standing refusal, or nil.
func (g *updateGate) current() *HubError {
	refusal, _ := g.state()
	return refusal
}

// state is the standing refusal and whether the Hub said so to this
// process (rather than as recorded when it started).
func (g *updateGate) state() (*HubError, bool) {
	if g == nil {
		return nil, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.refusal, g.known
}

// requiredRecord serializes recording the refusal and ending it. Both are
// written on their own goroutine: a request may be made while its caller
// holds the home database's only connection.
type requiredRecord struct {
	mu     sync.Mutex
	writes sync.WaitGroup // Close waits for them
}

// noteUpdateRequired records the Hub's refusal of this build, from any
// request or the stream's update_required event, and asks the daemon's
// automatic update to look (autoupdate.go). A refusal already recorded is
// not written again.
func (a *Agent) noteUpdateRequired(*HubError) {
	a.autoUpdateDue()
	a.wakeWorker()
	a.required.writes.Add(1)
	go func() {
		defer a.required.writes.Done()
		a.required.mu.Lock()
		defer a.required.mu.Unlock()
		he := a.hub.gate.current()
		if he == nil {
			return // served again meanwhile
		}
		if prev, ok := a.store.updateRequired(); ok && prev.Latest == he.Latest && prev.URL == he.URL {
			return
		}
		u := UpdateRequired{Latest: he.Latest, URL: he.URL, Running: protocol.Version, At: time.Now().Unix()}
		raw, _ := json.Marshal(u)
		if err := a.store.setConfig(map[string]string{updateRequiredKey: string(raw)}); err != nil {
			a.Logf("update required: %v", err)
		}
		a.Logf("your Hub serves this device again only once it runs a newer AgentNet: %s (this is %s); messages to it wait on the Hub", u.Message(), protocol.Version)
		a.changes.bump()
	}()
}

// endUpdateRequired ends a recorded refusal unless one stands again; it
// reports whether there was one.
func (a *Agent) endUpdateRequired(was bool) bool {
	a.required.mu.Lock()
	defer a.required.mu.Unlock()
	if a.hub.gate.current() != nil {
		return false
	}
	if _, ok := a.store.updateRequired(); !ok && !was {
		return false
	}
	if err := a.store.deleteConfig(updateRequiredKey); err != nil {
		a.Logf("update required: %v", err)
	}
	a.Logf("the Hub serves this device again")
	a.changes.bump()
	return true
}

// onUpdateRequiredEvent takes the stream's update_required event
// ({"latest":...,"url":...}): the Hub holds this stream with pings only
// until this device runs latest.
func (a *Agent) onUpdateRequiredEvent(data []byte) {
	var ev struct {
		Latest string `json:"latest"`
		URL    string `json:"url"`
	}
	if json.Unmarshal(data, &ev) != nil {
		a.Logf("unreadable update_required event; taken as one without a version")
	}
	if a.hub.gate != nil {
		a.hub.gate.set(updateRequiredError(ev.Latest, ev.URL))
	}
}

// updateServed: the Hub serves this device (its stream brought something
// it sends only to a device it serves). A recorded refusal of this build
// ends, and what waited is tried again now. The member list (sent at each
// connection) also clears a refusal another process recorded.
func (a *Agent) updateServed(event string) {
	was := a.hub.gate.clear()
	if !was && event != "members" {
		return
	}
	if !a.endUpdateRequired(was) {
		return
	}
	a.convWork.due(convPublish | convRetry | convRelease)
	a.kickNow()
}

// oldUpdateRefusals are the errors programs before v0.8.17 recorded on a
// send the Hub refused with 426 update_required, which they took as final
// (the row failed): the refusal itself, and the same met by a file's
// upload. This program records neither: to it a refusal waits.
var oldUpdateRefusals = []string{"hub: update_required (426)", "attachment upload: hub: update_required (426)"}

// requeueOldUpdateRefusals puts back in the queue what an earlier program
// failed only because the Hub required a newer AgentNet: rows failed with
// exactly those errors, none other, none the person stopped, and only
// while every file of it is still spooled here (else it stays failed:
// nothing incomplete goes). The Hub refused them before custody, so this
// sends the same sealed envelope again and runs nothing again; each file
// is offered to the Hub again, resumably (an upload it took then may have
// been reclaimed since). A startup pass: idempotent.
func (a *Agent) requeueOldUpdateRefusals() (int, error) {
	rows, err := a.store.db.Query(`SELECT id, envelope FROM outbox WHERE state = ? AND send_stopped = 0 AND error IN (?, ?)`, stateFailed, oldUpdateRefusals[0], oldUpdateRefusals[1])
	if err != nil {
		return 0, err
	}
	type refused struct {
		id    string
		blobs []envelope.Blob
	}
	var found []refused
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			continue // unreadable: left as it is
		}
		found = append(found, refused{id, env.Blobs})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range found {
		kept := true
		for _, b := range r.blobs {
			var owned int
			if err := a.store.db.QueryRow(`SELECT count(*) FROM uploads WHERE blob_id = ? AND message_id = ?`, b.ID, r.id).Scan(&owned); err != nil {
				return n, err
			}
			if _, err := os.Stat(a.spoolPath(b.ID)); owned == 0 || err != nil {
				kept = false
				break
			}
		}
		if !kept {
			continue
		}
		tx, err := a.store.db.Begin()
		if err != nil {
			return n, err
		}
		res, err := tx.Exec(`UPDATE outbox SET state = ?, error = NULL WHERE id = ? AND state = ? AND send_stopped = 0 AND error IN (?, ?)`, stateQueued, r.id, stateFailed, oldUpdateRefusals[0], oldUpdateRefusals[1])
		if err == nil {
			_, err = tx.Exec(`UPDATE uploads SET state = ? WHERE message_id = ?`, protocol.BlobUploading, r.id)
		}
		if err != nil {
			tx.Rollback()
			return n, err
		}
		if err := a.store.done(tx.Commit()); err != nil {
			return n, err
		}
		if c, _ := res.RowsAffected(); c > 0 {
			n++
		}
	}
	if n > 0 {
		a.changes.bump()
	}
	return n, nil
}

// UpdateRequired returns the Hub's standing refusal of this build, if any:
// as the Hub last answered this process, else as recorded.
func (a *Agent) UpdateRequired() (UpdateRequired, bool) {
	if he, known := a.hub.gate.state(); he != nil {
		return UpdateRequired{Latest: he.Latest, URL: he.URL, Running: protocol.Version}, true
	} else if known {
		return UpdateRequired{}, false
	}
	return a.store.updateRequired()
}

// updateRequired reads the recorded refusal; one of another version (this
// program replaced the one refused) is over.
func (s *store) updateRequired() (UpdateRequired, bool) {
	v, err := s.config(updateRequiredKey)
	if err != nil || v == "" {
		return UpdateRequired{}, false
	}
	var u UpdateRequired
	if json.Unmarshal([]byte(v), &u) != nil || u.Running != protocol.Version {
		return UpdateRequired{}, false
	}
	return u, true
}

// LocalUpdateRequired reads the recorded refusal in home without creating
// anything or contacting the Hub.
func LocalUpdateRequired(home string) (UpdateRequired, bool) {
	_, dbPath := paths(home)
	if _, err := os.Stat(dbPath); err != nil {
		return UpdateRequired{}, false
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return UpdateRequired{}, false
	}
	defer db.Close()
	return (&store{db: db}).updateRequired()
}

// updateRequiredNudge is the hook line while the Hub refuses this build.
// It names only a release tag and its https page, never the Hub's words.
func (a *Agent) updateRequiredNudge() (line, key string) {
	u, ok := a.UpdateRequired()
	if !ok {
		return "", ""
	}
	line = "AgentNet: " + u.Message() + " " + u.serves(a.Address) + " " + a.autoUpdateWords() + " How to update: `agentnet help update`"
	if u.URL != "" {
		line += "; release page: " + u.URL
	}
	return line + ".", "update_required " + u.Latest + " " + u.URL
}
