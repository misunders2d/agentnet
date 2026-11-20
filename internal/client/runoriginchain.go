package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Run origin version 2 (docs/revival/INSTALL.md, "Run origin socket"): what
// a run's request is (kind), whether every request in its cause chain is its
// person's own (chain), a handle for replying to that chain (reply), and
// plain names of who asked (agent, device). The cause chain is the one
// AgentNet already records and checks (roomChain): a run's ask in the
// person's "My agents" room replies to the request it serves. This device
// follows only what it holds; whatever it cannot follow is "mixed".

const (
	runOriginOwn   = "own"
	runOriginMixed = "mixed"

	replyHandlePrefix = "rh-"
	maxClientID       = 128
)

// Reply handles and client ids: local to this device, never sent anywhere.
// A reply handle names the request a reply goes to, for its person only; a
// client id names one message sent through a handle, so a repeat stores
// nothing new.
const replyHandleSchema = `
CREATE TABLE reply_handles(
  handle TEXT PRIMARY KEY,
  request TEXT NOT NULL UNIQUE,
  person TEXT NOT NULL,
  conv TEXT NOT NULL,
  lid TEXT NOT NULL,
  topic TEXT NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL);
CREATE TABLE client_sends(
  client_id TEXT PRIMARY KEY,
  id TEXT NOT NULL,
  lid TEXT NOT NULL,
  created_ms INTEGER NOT NULL);
`

// runOriginChain is version 2's description of one run, frozen at its
// vouch and compared at every answer. Only comparable strings.
type runOriginChain struct {
	kind, chain, agent, device string
	target                     replyTarget // the request a reply goes to
}

// replyTarget is a conversation request: the chain's own human request, or
// the run's own request. A request outside any conversation has none.
type replyTarget struct{ conv, lid, topic string }

// chainLink is one stored request of a cause chain.
type chainLink struct {
	from, key, kind, conv, lid, reply, origin, human, topic string
}

// runOriginPerson is the verified roster person of the exact key that
// signed a request from address (runOriginIdentity), or "".
func (a *Agent) runOriginPerson(from, key string) (string, error) {
	person, err := permissionPersonIn(a.store.db, from, key)
	if err != nil || person != "" {
		return person, err
	}
	if from == a.Address && key != "" && key == a.Self().Fingerprint() {
		p, found, e := personBySelfIn(a.store.db, a.Address)
		if e != nil {
			return "", e
		}
		if found && p.info.State == personSelf && p.has(from, key) {
			return p.info.Person, nil
		}
	}
	return "", nil
}

// runOriginChainOf describes the run serving request j for person, whose
// author is runOriginIdentity's. The chain is "own" only when it ends at a
// request of person's own, from their own current verified human device,
// that no agent wrote, and every request on the way was signed by one of
// person's own devices and written by them or their agents. A guest, another
// person, a link this device does not hold, a loop, or an agent's ask with
// no upstream request here (a terminal's, or a run's whose request was in
// another chat) makes it "mixed".
func (a *Agent) runOriginChainOf(j job, person, author string) (runOriginChain, error) {
	var c runOriginChain
	first, err := a.requestLink(j.ID)
	if err != nil {
		return c, err
	}
	switch first.kind {
	case envelope.KindTask, envelope.KindQuestion, envelope.KindMessage:
	default:
		return c, fmt.Errorf("a %q request is not a task, question or message", first.kind)
	}
	c.kind = first.kind
	if dev := DeviceWords(first.from); runOriginText(dev) {
		c.device = dev
	}
	if author == runOriginAgent {
		if name := a.askingAgentName(first); runOriginText(name) {
			c.agent = name
		}
	}
	c.chain, c.target = runOriginMixed, replyTarget{first.conv, first.lid, first.topic}
	if first.conv == "" {
		c.target = replyTarget{}
	}
	link := first
	seen := map[string]bool{}
	for {
		mine, human, err := a.ownChainLink(link, person)
		if err != nil {
			return c, err
		}
		if !mine {
			return c, nil
		}
		// The run's own request is the person's only by runOriginIdentity
		// (a local ask, continuation or reply never is); an upstream one by
		// its own marks and key.
		if link == first && author == runOriginHuman || link != first && human {
			c.chain = runOriginOwn
			if link.conv != "" {
				c.target = replyTarget{link.conv, link.lid, link.topic}
			}
			return c, nil
		}
		if link.conv == "" || link.reply == "" || seen[link.conv+"/"+link.lid] {
			return c, nil
		}
		seen[link.conv+"/"+link.lid] = true
		next, found, err := a.causeLink(link.conv, link.reply)
		if err != nil {
			return c, err
		}
		if !found {
			return c, nil
		}
		link = next
	}
}

// ownChainLink reports whether link was signed by one of person's own
// devices and written by them or their agent (mine), and whether by them,
// on their own human device (human). A guest's turn is never mine.
func (a *Agent) ownChainLink(link chainLink, person string) (mine, human bool, err error) {
	p, err := a.runOriginPerson(link.from, link.key)
	if err != nil || p != person {
		return false, false, err
	}
	agent := envelope.AgentOrigin(link.origin)
	if link.human != "" {
		var h envelope.HumanTurn
		if err := json.Unmarshal([]byte(link.human), &h); err != nil {
			return false, false, err
		}
		if h.AuthorPID != "" {
			for _, e := range h.Proof {
				if e.Type == protocol.EventScope && e.PID == h.AuthorPID && e.Role != "" {
					return false, false, nil // a guest's turn, carried by a member device
				}
			}
			agent = true
		}
	}
	if agent {
		return true, false, nil
	}
	own, err := a.ownHumanAuthor(a.store.db, link.from, link.key)
	return true, own, err
}

// requestLink reads the run's own stored request.
func (a *Agent) requestLink(id string) (chainLink, error) {
	var l chainLink
	err := a.store.db.QueryRow(`SELECT sender,coalesce(verified_by,''),kind,coalesce(conv,''),coalesce(lid,''),coalesce(reply_to,''),coalesce(origin,''),coalesce(human,''),coalesce(topic,'') FROM inbox WHERE id=? AND replica=0`, id).
		Scan(&l.from, &l.key, &l.kind, &l.conv, &l.lid, &l.reply, &l.origin, &l.human, &l.topic)
	return l, err
}

// causeLink reads the request ref (a logical or copy id) names in conv, as
// roomCauseIn does: a verified addressed question or task held here, or one
// this device sent. Copies that disagree are not followed.
func (a *Agent) causeLink(conv, ref string) (chainLink, bool, error) {
	rows, err := a.store.db.Query(`SELECT sender,coalesce(verified_by,''),kind,coalesce(lid,id),coalesce(reply_to,''),coalesce(origin,''),coalesce(human,''),coalesce(topic,'') FROM inbox WHERE conv=? AND (id=? OR lid=?) AND verified_by IS NOT NULL AND sub IS NULL AND kind IN ('question','task') AND target IS NOT NULL
 UNION ALL SELECT ?,?,kind,coalesce(lid,id),coalesce(reply_to,''),coalesce(origin,''),coalesce(human,''),coalesce(topic,'') FROM outbox WHERE conv=? AND (id=? OR lid=?) AND sub IS NULL AND kind IN ('question','task') AND target IS NOT NULL`,
		conv, ref, ref, a.Address, a.Self().Fingerprint(), conv, ref, ref)
	if err != nil {
		return chainLink{}, false, err
	}
	defer rows.Close()
	var l chainLink
	found := false
	for rows.Next() {
		n := chainLink{conv: conv}
		if err := rows.Scan(&n.from, &n.key, &n.kind, &n.lid, &n.reply, &n.origin, &n.human, &n.topic); err != nil {
			return chainLink{}, false, err
		}
		if found && n != l {
			return chainLink{}, false, nil
		}
		l, found = n, true
	}
	return l, found, rows.Err()
}

// askingAgentName is the plain name of the agent that wrote link: this
// device's own agent of that harness by its label (one per harness per
// device), else the harness's name. Never an address or id.
func (a *Agent) askingAgentName(link chainLink) string {
	harness, ok := strings.CutPrefix(link.origin, envelope.OriginAgentPrefix)
	if !ok || harness == "" {
		return ""
	}
	if link.from == a.Address {
		if agents, err := a.localAgentsIn(a.store.db); err == nil {
			for _, e := range agents {
				if e.Responder != nil && e.Responder.Harness == harness && e.Record.Label != "" {
					return e.Record.Label
				}
			}
		}
	}
	return HarnessLabel(harness)
}

// replyHandleFor is the reply handle of the run serving request for person:
// minted once, kept, the same at every answer. A run whose target is outside
// a conversation has none ("").
func (a *Agent) replyHandleFor(request, person string, t replyTarget) (string, error) {
	if t.conv == "" || t.lid == "" {
		return "", nil
	}
	if _, err := a.store.db.Exec(`INSERT OR IGNORE INTO reply_handles(handle,request,person,conv,lid,topic,created_ms) VALUES(?,?,?,?,?,?,?)`,
		replyHandlePrefix+protocol.NewID(), request, person, t.conv, t.lid, t.topic, time.Now().UnixMilli()); err != nil {
		return "", err
	}
	var handle, p, conv, lid string
	if err := a.store.db.QueryRow(`SELECT handle,person,conv,lid FROM reply_handles WHERE request=?`, request).Scan(&handle, &p, &conv, &lid); err != nil {
		return "", err
	}
	if p != person || conv != t.conv || lid != t.lid {
		return "", errors.New("the request's reply handle names another reply")
	}
	return handle, nil
}

// ErrReplyHandle: the handle is not this device's, or not this person's.
var ErrReplyHandle = errors.New("unknown reply handle: it is valid only on the device that gave it, for that device's own person")

// ReplyHandleSent is one message sent through a reply handle.
type ReplyHandleSent struct {
	ID, LID, State string
	Conv, Topic    string
	Repeated       bool // clientID named a message sent before; nothing new was sent
}

// errClientIDTaken: another send stored this client id first.
var errClientIDTaken = errors.New("client id already used")

// SendReplyHandle sends body as agentID, an agent hosted here, into the
// conversation and topic of the request handle names, for this device's own
// person only. With clientID, a message already sent with it is returned
// instead (at-least-once callers may repeat safely); the check and the store
// are one transaction.
func (a *Agent) SendReplyHandle(ctx context.Context, handle, agentID, clientID, body string, files []OutgoingFile) (ReplyHandleSent, error) {
	var out ReplyHandleSent
	if !strings.HasPrefix(handle, replyHandlePrefix) || !runOriginText(handle) {
		return out, ErrReplyHandle
	}
	if clientID != "" && (len(clientID) > maxClientID || !runOriginText(clientID)) {
		return out, errors.New("a client id is 1 to 128 printable bytes")
	}
	if agentID == "" {
		return out, errors.New("name the agent to send as with --from-agent ID")
	}
	if clientID != "" {
		if prior, ok, err := a.clientSent(clientID); err != nil || ok {
			return prior, err
		}
	}
	var person string
	err := a.store.db.QueryRow(`SELECT person,conv,lid,topic FROM reply_handles WHERE handle=?`, handle).Scan(&person, &out.Conv, &out.LID, &out.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrReplyHandle
	}
	if err != nil {
		return out, err
	}
	self, found, err := personBySelfIn(a.store.db, a.Address)
	if err != nil {
		return out, err
	}
	if !found || self.info.State != personSelf || self.info.Person != person || !self.has(a.Address, a.Self().Fingerprint()) {
		return out, ErrReplyHandle
	}
	if _, err := a.SessionAgent("", agentID); err != nil {
		return out, err
	}
	pid, err := a.replyParticipation(ctx, out.Conv, out.Topic, agentID)
	if err != nil {
		return out, err
	}
	var before beforeConvStore
	if clientID != "" {
		before = func(tx *sql.Tx, copies []outCopy, local envelope.Inner) error {
			id, lid := local.ID, local.LID
			if len(copies) > 0 {
				id, lid = copies[0].env.ID, copies[0].in.LID
			}
			res, err := tx.Exec(`INSERT OR IGNORE INTO client_sends(client_id,id,lid,created_ms) VALUES(?,?,?,?)`, clientID, id, lid, time.Now().UnixMilli())
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errClientIDTaken
			}
			return nil
		}
	}
	sent, err := a.sendAgentMessage(ctx, pid, out.Topic, body, before, files...)
	if errors.Is(err, errClientIDTaken) {
		prior, ok, e := a.clientSent(clientID)
		if e == nil && !ok {
			e = err
		}
		return prior, e
	}
	if err != nil {
		return out, err
	}
	out.ID, out.LID, out.State = sent.ID, sent.LID, sent.State
	return out, nil
}

// clientSent is the message an earlier send stored with clientID.
func (a *Agent) clientSent(clientID string) (ReplyHandleSent, bool, error) {
	var out ReplyHandleSent
	err := a.store.db.QueryRow(`SELECT c.id,c.lid,coalesce(o.state,''),coalesce(o.conv,''),coalesce(o.topic,'') FROM client_sends c LEFT JOIN outbox o ON o.id=c.id WHERE c.client_id=?`, clientID).
		Scan(&out.ID, &out.LID, &out.State, &out.Conv, &out.Topic)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	out.Repeated = true
	return out, err == nil, err
}

// replyParticipation is agentID's active participation hosted here in conv
// and topic: one it already has, or, in the person's own "My agents" room,
// the topic's (own agents join without a click, as for an ask there).
func (a *Agent) replyParticipation(ctx context.Context, conv, topic, agentID string) (string, error) {
	infos, err := a.Participations(conv)
	if err != nil {
		return "", err
	}
	for _, p := range infos {
		if p.HostHere && p.AgentID == agentID && p.Role == "" && p.Claimable() && (p.Topic == nil || *p.Topic == topic) {
			return p.PID, nil
		}
	}
	room, err := a.MyAgentsRoom(ctx)
	if err != nil {
		return "", err
	}
	if conv != room || topic == "" {
		return "", errors.New("that agent is not active in the reply's chat here")
	}
	keys, err := a.ownHumanTaskKeys()
	if err != nil {
		return "", err
	}
	p, err := a.topicParticipation(ctx, conv, topic, a.Address, agentID, keys)
	if err != nil {
		return "", err
	}
	jctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if p, err = a.awaitClaimable(jctx, p.PID); err != nil {
		return "", err
	}
	return p.PID, nil
}
