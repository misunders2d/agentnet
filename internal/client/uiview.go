package client

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Read-only views for the local messenger page. Threads are derived from
// today's reply links on every read: nothing new is stored, so a message
// whose parent arrives late simply joins its thread next time.
//
// These views are one installation's device history (version 1). Messages
// of a conversation (a DM, version 2) are never part of them: they are shown
// through the conversation APIs (agentnet dm), not as device threads; only
// PageReview names the conversation items waiting for the person.

// ThreadSummary is one reply-linked conversation with one peer.
type ThreadSummary struct {
	ID      string    `json:"id"` // earliest message of the thread (derived, not stored)
	Peer    string    `json:"peer"`
	Title   string    `json:"title"` // first line of the first message
	Last    string    `json:"last"`  // first line of the latest message
	LastAt  time.Time `json:"last_at"`
	Count   int       `json:"count"`
	Review  int       `json:"review"`  // received items waiting for a decision here
	Unread  int       `json:"unread"`  // received messages not yet read (review notices not counted)
	Running int       `json:"running"` // received items the worker is on
	Waiting bool      `json:"waiting"` // a question or task sent here has no reply yet
	// Notices counts open review notices: reports from another machine that
	// requests wait for a person there. They are not decisions here.
	Notices    int  `json:"notices"`
	NoticeOnly bool `json:"notice_only"` // every message in the thread is a review notice
	// AgentID is the thread's agent when one is named: the latest a request
	// names as its target, or an answer, result or progress as its author
	// (as admission bound them); never a received request's local executor.
	AgentID string `json:"agent_id,omitempty"`
}

// threadRow is what a summary needs to know about one message.
type threadRow struct {
	link
	in       bool
	kind     string
	state    string
	unread   bool
	notice   bool // in only: a review notice (see envelope.StatusReviewNotice)
	replied  bool // out only: a received message replies to it
	selected bool // in only: local receiver input, not a remote execution job
	// agent is the agent it names: a request's target, an output's author.
	agent string
}

// Threads lists every thread with every peer, most recent first.
func (a *Agent) Threads() ([]ThreadSummary, error) {
	peers, err := a.store.conversationPeers()
	if err != nil {
		return nil, err
	}
	var out []ThreadSummary
	for _, peer := range peers {
		ts, err := a.peerThreads(peer)
		if err != nil {
			return nil, err
		}
		out = append(out, ts...)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastAt.Equal(out[j].LastAt) {
			return out[i].LastAt.After(out[j].LastAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (a *Agent) peerThreads(peer string) ([]ThreadSummary, error) {
	groups, rows, err := a.peerThreadGroups(peer)
	if err != nil || len(groups) == 0 {
		return nil, err
	}
	var out []ThreadSummary
	for _, g := range groups {
		first, last := rows[g[0]], rows[g[len(g)-1]]
		t := ThreadSummary{ID: first.id, Peer: peer, Count: len(g), LastAt: time.Unix(last.at, 0), NoticeOnly: true}
		for _, id := range g {
			r := rows[id]
			if r.agent != "" { // oldest first: the latest named one stays
				t.AgentID = r.agent
			}
			t.NoticeOnly = t.NoticeOnly && r.notice
			switch {
			case r.notice:
				if r.state == stateNeedHuman {
					t.Notices++
				}
				continue
			case r.in && !r.selected && (r.state == stateHeld || r.state == stateAwaiting || r.state == stateNeedHuman):
				t.Review++
			case r.in && !r.selected && (r.state == stateRunning || r.state == stateCancelReq):
				t.Running++
			}
			if r.in && r.unread {
				t.Unread++
			}
			if !r.in && (r.kind == envelope.KindQuestion || r.kind == envelope.KindTask) && !r.replied {
				t.Waiting = true
			}
		}
		full, err := a.store.peerMessages(peer, []string{first.id, last.id})
		if err != nil {
			return nil, err
		}
		t.Title, t.Last = firstLine(full[first.id].Body), firstLine(full[last.id].Body)
		out = append(out, t)
	}
	return out, nil
}

// peerThreadGroups unions the device-history messages with peer into
// reply-linked threads, each oldest first (its first id is the thread's
// ThreadSummary.ID), with each message's row (link filled in). A deleted
// thread's messages are not in rows, so they join no thread.
func (a *Agent) peerThreadGroups(peer string) ([][]string, map[string]threadRow, error) {
	all, err := a.store.peerLinks(peer)
	if err != nil {
		return nil, nil, err
	}
	rows, err := a.store.threadRows(peer, a.Self().Fingerprint())
	if err != nil {
		return nil, nil, err
	}
	var links []link // device history only: rows has no conversation messages
	for _, l := range all {
		if _, ok := rows[l.id]; ok {
			links = append(links, l)
		}
	}
	if len(links) == 0 {
		return nil, rows, nil
	}
	// Union reply-linked messages into threads.
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	for _, l := range links {
		parent[l.id] = l.id
	}
	for _, l := range links {
		if _, ok := parent[l.replyTo]; ok && l.replyTo != "" {
			parent[find(l.id)] = find(l.replyTo)
		}
	}
	byID := map[string]link{}
	grouped := map[string][]string{}
	for _, l := range links {
		byID[l.id] = l
		r := find(l.id)
		grouped[r] = append(grouped[r], l.id)
	}
	var groups [][]string
	for _, g := range grouped {
		sortThread(g, byID)
		for _, id := range g {
			r := rows[id]
			r.link = byID[id]
			rows[id] = r
		}
		groups = append(groups, g)
	}
	return groups, rows, nil
}

// conversationPeers lists every address this installation has exchanged
// device-history messages with (conversation messages left out).
func (s *store) conversationPeers() ([]string, error) {
	rows, err := s.db.Query(`SELECT sender FROM inbox WHERE conv IS NULL UNION SELECT recipient FROM outbox WHERE conv IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// threadRows reads direction, kind, state and read state for every
// device-history message exchanged with peer, which sent questions or
// tasks have a reply, and the agent each names: a request's target agent,
// or an output's author (a received request's agent_id is its local
// executor, never its sender's word).
func (s *store) threadRows(peer, selfFP string) (map[string]threadRow, error) {
	out := map[string]threadRow{}
	// A review notice is exactly the shape the store files as one (see
	// receivedNotice); a reply or a message with files never is.
	rows, err := s.db.Query(`SELECT id, kind, state, read_at IS NULL, coalesce(reply_to, ''), coalesce(status, ''), (`+receivedNotice+`), EXISTS(SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id),
		CASE WHEN kind IN ('`+envelope.KindQuestion+`', '`+envelope.KindTask+`') THEN coalesce(json_extract(target, '$.agent_id'), '') ELSE coalesce(agent_id, '') END
		FROM inbox WHERE sender = ? AND conv IS NULL AND ref_id IS NULL AND NOT `+erasedInFor("inbox"),
		envelope.KindMessage, envelope.StatusReviewNotice, peer)
	if err != nil {
		return nil, err
	}
	replies := map[string]bool{}
	for rows.Next() {
		var r threadRow
		var status string
		if err := rows.Scan(&r.id, &r.kind, &r.state, &r.unread, &r.replyTo, &status, &r.notice, &r.selected, &r.agent); err != nil {
			rows.Close()
			return nil, err
		}
		r.in = true
		out[r.id] = r
		if r.replyTo != "" && status != envelope.StatusProgress {
			replies[r.replyTo] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(`SELECT id, coalesce(json_extract(envelope, '$.kind'), ''), coalesce(json_extract(target, '$.agent_id'), agent_id, '')
		FROM outbox o WHERE recipient = ? AND conv IS NULL AND ref_id IS NULL AND NOT `+erasedOut, peer, selfFP)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r threadRow
		if err := rows.Scan(&r.id, &r.kind, &r.agent); err != nil {
			return nil, err
		}
		r.replied = replies[r.id]
		out[r.id] = r
	}
	return out, rows.Err()
}

// Why a conversation item waits for the person here (ConvReview.Reason).
const (
	ReviewAwaiting   = "agent_awaiting"    // a request to this device's agent waits for a one-time accept (Accept)
	ReviewNeedsHuman = "agent_needs_human" // this device's agent said the person must decide (Accept reruns it, Resolve closes it)
	ReviewInvite     = "agent_invite"      // an invitation for this device's agent waits for its person (AcceptParticipation, DeclineParticipation)
	ReviewHeldTurn   = "person_turn"       // a question or task for the person, held in its conversation: answered there, never run
)

// ConvReview is a conversation item waiting for the person here, with the
// conversation it belongs to, so the page opens exactly that one. Listing
// or opening it decides nothing: a request is decided by its ID (Accept,
// Resolve), an invitation by its PID (AcceptParticipation,
// DeclineParticipation), as anywhere else.
type ConvReview struct {
	Reason string    `json:"reason"`
	Conv   string    `json:"conv"`
	PID    string    `json:"pid,omitempty"`
	ID     string    `json:"id,omitempty"` // the request or turn; an invitation has none (its PID names it)
	From   string    `json:"from"`         // the asking or inviting device
	Kind   string    `json:"kind,omitempty"`
	State  string    `json:"state,omitempty"`
	Body   string    `json:"body"`             // the request or turn, or the invitation's note
	Detail string    `json:"detail,omitempty"` // why it waits, as recorded here
	At     time.Time `json:"at"`
	Unread bool      `json:"unread,omitempty"`
}

// ReviewPage is what waits for the person, as the page lists it: device
// history items (Device), conversation items decided here (Conv: requests
// to this device's agent and invitations for it) and person turns held in
// conversations (Held), listed apart: they are answered in the
// conversation, never accepted or run.
type ReviewPage struct {
	Device []Message
	Conv   []ConvReview
	Held   []ConvReview
}

// PageReview is Review for the page. An invitation is listed while it waits
// for this person's decision; one this device consented to itself never
// waits, so it is never listed. A participation that cannot be resolved
// here now is left out (and logged): one conversation's trouble never takes
// the page with it.
func (a *Agent) PageReview() (ReviewPage, error) {
	var p ReviewPage
	var err error
	if p.Device, err = a.store.messages(` WHERE conv IS NULL AND `+inReview, reviewStates...); err != nil {
		return p, err
	}
	// A request to this device's agent, as Accept and Resolve take it. One
	// whose turn is erased here still waits: it is kept until its work ends.
	requests, err := a.store.convReview(`i.pid IS NOT NULL AND i.replica = 0 AND i.kind IN (?, ?) AND i.state IN (?, ?)`,
		envelope.KindQuestion, envelope.KindTask, stateAwaiting, stateNeedHuman)
	if err != nil {
		return p, err
	}
	p.Conv = a.stillDecidable(requests)
	invites, err := a.hostInvites()
	if err != nil {
		return p, err
	}
	p.Conv = append(p.Conv, invites...)
	// A held turn erased here is gone with its conversation: nothing waits on
	// it. One this person sent from another of their devices is theirs ("out"
	// in the conversation), not held for them.
	held, err := a.store.convReview(`i.state = ? AND NOT `+erasedIn, stateConvHeld)
	if err != nil {
		return p, err
	}
	own := a.ownDevices()
	for _, r := range held {
		if !own[r.From] {
			p.Held = append(p.Held, r)
		}
	}
	return p, nil
}

// stillDecidable leaves out the requests whose participation ended here
// (dismissed, declined or in conflict): the worker only closes those
// (agentVerdict), so accepting one decides nothing. A request whose
// participation cannot be resolved now stays listed.
func (a *Agent) stillDecidable(requests []ConvReview) []ConvReview {
	ended := map[string]bool{}
	seen := map[string]bool{}
	var out []ConvReview
	for _, r := range requests {
		key := r.Conv + "/" + r.PID
		if !seen[key] {
			seen[key] = true
			info, err := a.participation(r.Conv, r.PID)
			switch {
			case err == nil:
				ended[key] = info.State == PartDismissed || info.State == PartDeclined || info.State == PartConflict
			case !errors.Is(err, ErrNoParticipation) && !errors.Is(err, ErrGroupContextPending):
				a.Logf("needs-you: participation %s of %s: %v", r.PID, r.Conv, err)
			}
		}
		if !ended[key] {
			out = append(out, r)
		}
	}
	return out
}

// convReview lists the received conversation rows (alias i) matching where,
// oldest first; selected local receiver input is never among them.
func (s *store) convReview(where string, args ...any) ([]ConvReview, error) {
	rows, err := s.db.Query(`SELECT i.id, i.conv, coalesce(i.pid, ''), i.sender, i.kind, i.state, i.body, coalesce(i.detail, ''), i.received_at, i.read_at IS NULL
		FROM inbox i WHERE i.conv IS NOT NULL AND `+where+` AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs ri WHERE ri.inbox_id = i.id)
		ORDER BY i.received_at, i.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConvReview
	for rows.Next() {
		var r ConvReview
		var at int64
		if err := rows.Scan(&r.ID, &r.Conv, &r.PID, &r.From, &r.Kind, &r.State, &r.Body, &r.Detail, &at, &r.Unread); err != nil {
			return nil, err
		}
		r.At = time.Unix(at, 0)
		switch r.State {
		case stateAwaiting:
			r.Reason = ReviewAwaiting
		case stateNeedHuman:
			r.Reason = ReviewNeedsHuman
		default:
			r.Reason = ReviewHeldTurn
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// hostInvites lists the invitations for this device's agent that wait for
// its person's decision, as they resolve here now. A participation whose
// evidence is not here yet cannot be decided either, so it is not listed;
// one that fails to resolve is left out and logged. Each is listed at the
// time its first record naming this host reached this device: the time an
// inviter writes into it (ParticipationInfo.Invited) is only its claim.
func (a *Agent) hostInvites() ([]ConvReview, error) {
	rows, err := a.store.db.Query(`SELECT conv, pid, min(received_at) FROM participation_events WHERE type IN (?, ?) AND json_extract(event, '$.host.address') = ?
		GROUP BY conv, pid ORDER BY min(received_at), conv, pid`, protocol.EventInvite, protocol.EventScope, a.Address)
	if err != nil {
		return nil, err
	}
	type named struct {
		conv, pid string
		at        int64
	}
	var all []named
	for rows.Next() {
		var n named
		if err := rows.Scan(&n.conv, &n.pid, &n.at); err != nil {
			rows.Close()
			return nil, err
		}
		all = append(all, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []ConvReview
	for _, n := range all {
		info, err := a.participation(n.conv, n.pid)
		if errors.Is(err, ErrNoParticipation) || errors.Is(err, ErrGroupContextPending) {
			continue
		}
		if err != nil {
			a.Logf("needs-you: invitation %s of %s left out: %v", n.pid, n.conv, err)
			continue
		}
		if !info.HostHere || info.State != PartInvited || info.Role == protocol.RoleHuman {
			continue
		}
		out = append(out, ConvReview{Reason: ReviewInvite, Conv: info.Conv, PID: info.PID, From: info.Inviter.Address,
			Body: info.Note, Detail: info.Inviter.Label + " invited your agent. Nothing runs unless you accept.", At: time.Unix(n.at, 0)})
	}
	return out, nil
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// Quarantined is a received envelope held back from the inbox.
type Quarantined struct {
	ID         string    `json:"id"`
	Sender     string    `json:"sender"`
	Reason     string    `json:"reason"` // "key_changed" (waits for trust) or "invalid"
	ReceivedAt time.Time `json:"received_at"`
}

// Quarantine lists held-back envelopes, newest first. Their content is not
// shown: it did not verify, or the sender's key changed.
func (a *Agent) Quarantine() ([]Quarantined, error) {
	rows, err := a.store.db.Query(`SELECT id, sender, reason, received_at FROM quarantine ORDER BY received_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quarantined
	for rows.Next() {
		var q Quarantined
		var at int64
		if err := rows.Scan(&q.ID, &q.Sender, &q.Reason, &at); err != nil {
			return nil, err
		}
		q.ReceivedAt = time.Unix(at, 0)
		out = append(out, q)
	}
	return out, rows.Err()
}

// PeerKey is what this installation knows about a peer's keys.
type PeerKey struct {
	Known   bool   `json:"known"`
	Pinned  string `json:"pinned,omitempty"`  // fingerprint of the pinned key
	Pending string `json:"pending,omitempty"` // fingerprint of a changed key waiting for trust
}

// PeerKeyOf reports the pinned and any pending key of address, locally.
func (a *Agent) PeerKeyOf(address string) (PeerKey, error) {
	pinned, pending, found, err := a.store.peer(address)
	if err != nil || !found {
		return PeerKey{}, err
	}
	k := PeerKey{Known: true, Pinned: pinned.Fingerprint()}
	if pending != nil {
		k.Pending = pending.Fingerprint()
	}
	return k, nil
}

// MarkRead marks received messages as read.
func (a *Agent) MarkRead(ids []string) error {
	if err := a.store.markRead(ids); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}

// ConvUnread lists, per conversation, the received messages not yet read.
func (a *Agent) ConvUnread() (map[string][]string, error) {
	rows, err := a.store.db.Query(`SELECT conv, id FROM inbox i WHERE conv IS NOT NULL AND read_at IS NULL AND ref_id IS NULL AND NOT ` + erasedIn)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var conv, id string
		if err := rows.Scan(&conv, &id); err != nil {
			return nil, err
		}
		out[conv] = append(out[conv], id)
	}
	return out, rows.Err()
}
