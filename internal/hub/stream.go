package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// streams tracks live push connections per agent so new messages wake them
// and revocation closes them.
type streams struct {
	mu    sync.Mutex
	subs  map[string]map[*subscriber]struct{}
	added uint64 // connections so far: orders one agent's connections
}

type subscriber struct {
	id      string // connection id, echoed back in ping acknowledgements
	n       uint64 // order of connection: the agent's newest has the highest
	wake    chan struct{}
	cancel  context.CancelFunc
	lastAck atomic.Int64 // unix nanoseconds of the last sign of life (ack, received)
}

func (s *streams) add(agent string, cancel context.CancelFunc) *subscriber {
	sub := &subscriber{id: protocol.NewID(), wake: make(chan struct{}, 1), cancel: cancel}
	sub.lastAck.Store(time.Now().UnixNano())
	s.mu.Lock()
	defer s.mu.Unlock()
	s.added++
	sub.n = s.added
	if s.subs == nil {
		s.subs = map[string]map[*subscriber]struct{}{}
	}
	if s.subs[agent] == nil {
		s.subs[agent] = map[*subscriber]struct{}{}
	}
	s.subs[agent][sub] = struct{}{}
	return sub
}

func (s *streams) remove(agent string, sub *subscriber) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs[agent], sub)
	if len(s.subs[agent]) == 0 {
		delete(s.subs, agent)
	}
}

func (s *streams) notify(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		select {
		case sub.wake <- struct{}{}:
		default: // already pending
		}
	}
}

// ack records that agent answered a ping on connection id. Acks for a
// closed or someone else's connection do nothing, so they can never keep a
// replacement connection (or another agent's) alive.
func (s *streams) ack(agent, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		if sub.id == id {
			sub.lastAck.Store(time.Now().UnixNano())
			return true
		}
	}
	return false
}

// received records that agent acknowledged a message pushed to it. A device
// storing a backlog answers a ping only after every message ahead of it
// (both clients handle the stream in order), yet acknowledges each message
// once stored: a sign of life as good as a ping's. It counts for the agent's
// newest connection only. A device reads one stream at a time (the browser
// holds its tab lock, the daemon its process lock) and connects again only
// after giving up the last, so an older connection still listed here is one
// it abandoned, perhaps half-open, and the lease must still close it.
func (s *streams) received(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var newest *subscriber
	for sub := range s.subs[agent] {
		if newest == nil || sub.n > newest.n {
			newest = sub
		}
	}
	if newest != nil {
		newest.lastAck.Store(time.Now().UnixNano())
	}
}

// notifyAll wakes every stream, e.g. to push a changed release.
func (s *streams) notifyAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, subs := range s.subs {
		for sub := range subs {
			select {
			case sub.wake <- struct{}{}:
			default:
			}
		}
	}
}

func (s *streams) disconnect(agent string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs[agent] {
		sub.cancel()
	}
}

// streamWriteTimeout bounds each write to a push stream, so a peer that
// stopped reading (asleep, half-open) cannot hold a handler forever. It is a
// variable only so tests can shorten it.
var streamWriteTimeout = 15 * time.Second

// handleStream pushes every unacknowledged message, then new ones as they
// arrive. Unacknowledged messages are pushed again on the next connection.
//
// Liveness: every ping carries this connection's id, and the client answers
// with a signed acknowledgement (one small request per ping interval). A
// connection with no acknowledgement for two ping intervals is closed, so a
// silently vanished laptop's session ends within about three ping intervals
// plus the session grace period. A message acknowledgement from the device
// counts too (streams.received): a busy device answers pings late. Pings
// keep their interval while a backlog is pushed (between frames).
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	caller, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	ad, err := protocol.DecodeAd(r.URL.Query().Get("ad"))
	if err == nil && ad.Address != caller {
		err = errors.New("session ad is for another agent")
	}
	var a agent
	if err == nil {
		if a, err = h.store.agent(caller); err == nil {
			err = ad.Verify(a.Public.SignKey)
		}
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "", "stream needs a valid signed session ad: "+err.Error())
		return
	}
	var receiptCursor int64
	receiptPush := r.URL.Query().Has(protocol.StreamReceipts)
	if receiptPush {
		value := r.URL.Query().Get(protocol.StreamReceipts)
		receiptCursor, err = strconv.ParseInt(value, 10, 64)
		if err != nil || receiptCursor < 0 || strings.Trim(value, "0123456789") != "" {
			writeError(w, http.StatusBadRequest, "", "invalid receipt cursor")
			return
		}
		max, e := h.store.receiptMax(caller)
		if e != nil {
			writeError(w, http.StatusInternalServerError, "", "storage error")
			return
		}
		if receiptCursor > max {
			receiptCursor = 0
		}
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sub := h.streams.add(caller, cancel)
	defer h.streams.remove(caller, sub)
	// What this device runs, as its stream says (update.go). A pending
	// device is no member: no version, session or presence.
	version := reportedVersion(r)
	if !a.Pending {
		if err := h.noteVersion(caller, version); err != nil {
			writeError(w, http.StatusInternalServerError, "", "storage error")
			return
		}
		if err := h.store.setLastSession(caller, ad.Session); err != nil {
			writeError(w, http.StatusInternalServerError, "", "storage error")
			return
		}
		h.presence.connect(caller, ad)
		defer h.presence.disconnect(caller, ad.Session)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	if !a.Pending {
		w.Header().Set(protocol.MembersHeader, "1")
		w.Header().Set(protocol.TeamsHeader, "1") // the team directory rides on the members generation (teams.go)
		w.Header().Set(protocol.SignalsHeader, "1")
	}
	w.WriteHeader(http.StatusOK)
	http.NewResponseController(w).Flush()

	rc := http.NewResponseController(w)
	// The deadline covers one write and is cleared after it: a deadline left
	// in place would reset an idle HTTP/2 stream when it expires.
	write := func(format string, args ...any) bool {
		rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		if rc.Flush() != nil {
			return false
		}
		rc.SetWriteDeadline(time.Time{})
		return true
	}
	ping := time.NewTicker(h.heartbeat)
	defer ping.Stop()
	lease := 2 * h.heartbeat
	if a.Pending {
		h.holdPending(ctx, caller, sub, ping, lease, write)
		return
	}
	// pinged closes a connection that showed no sign of life for the lease,
	// or asks for one with this connection's ping.
	pinged := func() bool {
		if time.Since(time.Unix(0, sub.lastAck.Load())) > lease {
			h.cfg.Logf("closing silent stream of %s#%s", caller, ad.Session)
			return false
		}
		return write("event: ping\ndata: {\"conn\":%q}\n\n", sub.id)
	}
	// between runs after each frame of a backlog. A long push (a large queue
	// to a peer that reads slowly, so each write waits for it) never reaches
	// the wait below: without this it would send no ping, and the first tick
	// after it would find the lease expired without having asked. A due ping
	// goes between two whole frames, so nothing is reordered; clients handle
	// it in stream order like any other.
	between := func() bool {
		select {
		case <-ping.C:
			return pinged()
		default:
			return true
		}
	}
	signals := h.signals.subscribe(caller, sub)
	defer h.signals.unsubscribe(caller, sub)
	var lastSeq, adminNoticeCursor int64
	sentRelease := int64(-1) // the release is sent on connect and when it changes
	sentMembers := int64(-1) // so is the member list
	sentAgain := int64(-1)   // as of this look-again generation
	var sentMembersSum, sentTeamsSum [sha256.Size]byte
	sentLinks := int64(-1) // and the devices waiting for this one's approval
	linked := map[string]bool{}
	sentGroupHeads := "" // the caller's group journal heads, sent when they change (groups.go)
	for {
		if _, required := h.updateRequired(version); required { // on connect, or its grace ended meanwhile
			h.holdSuspended(ctx, sub, version, ping, lease, write)
			return
		}
		if rel, gen := h.currentRelease(); gen != sentRelease {
			data, _ := json.Marshal(rel)
			if !write("event: release\ndata: %s\n\n", data) {
				return
			}
			sentRelease = gen
		}
		// The generation is read before the list is built: a change made
		// meanwhile moves it on, so the next pass sends the list again. A
		// list or directory the same as the last one sent here is not sent
		// again, unless something receivers look again for on each list
		// changed meanwhile (lookAgain: capabilities, a catalog, a role).
		if gen := h.membersGen.Load(); gen != sentMembers {
			again := h.againGen.Load()
			m, err := h.members()
			if err != nil {
				return
			}
			data, _ := json.Marshal(m)
			if sum := sha256.Sum256(data); sum != sentMembersSum || again != sentAgain {
				if !write("event: members\ndata: %s\n\n", data) {
					return
				}
				sentMembersSum = sum
			}
			if dir, err := h.teamDirectoryVersion(r.URL.Query().Get("teams") == "2"); err == nil { // the same generation moves both
				data, _ := json.Marshal(dir)
				if sum := sha256.Sum256(data); sum != sentTeamsSum || again != sentAgain {
					if !write("event: teams\ndata: %s\n\n", data) {
						return
					}
					sentTeamsSum = sum
				}
			}
			sentMembers, sentAgain = gen, again
		}
		if heads, err := h.store.groupHeads(caller); err != nil {
			return
		} else if !writeGroupHeads(heads, &sentGroupHeads, write) {
			return
		}
		if gen := h.linksGen.Load(); gen != sentLinks {
			links, err := h.store.pendingLinks(caller)
			if err != nil {
				return
			}
			for device, ev := range links {
				if !linked[device] {
					if !write("event: link\ndata: %s\n\n", ev) {
						return
					}
					linked[device] = true
				}
			}
			sentLinks = gen
		}
		notices, err := h.store.deviceAdminNotices(caller, adminNoticeCursor)
		if err != nil {
			return
		}
		for _, n := range notices {
			data, _ := json.Marshal(n)
			if !write("event: device_admin\ndata: %s\n\n", data) {
				return
			}
			adminNoticeCursor = n.Seq
			if !between() {
				return
			}
		}
		if len(notices) == 100 {
			continue
		}
		if receiptPush {
			receipts, err := h.store.receiptsFor(caller, receiptCursor)
			if err != nil {
				return
			}
			for _, receipt := range receipts {
				data, _ := json.Marshal(receipt)
				if !write("event: receipt\ndata: %s\n\n", data) {
					return
				}
				receiptCursor = receipt.Seq
				if !between() {
					return
				}
			}
			if len(receipts) == protocol.ReceiptBatch {
				continue
			}
		}
		msgs, err := h.store.pendingFor(caller, ad.Session, lastSeq)
		if err != nil {
			return
		}
		for _, m := range msgs {
			if !write("event: message\ndata: %s\n\n", m.Envelope) {
				return
			}
			lastSeq = m.Seq
			if !between() {
				return
			}
		}
		if len(msgs) == 100 {
			continue // more backlog
		}
		select {
		case signal := <-signals:
			if signal.Session != ad.Session || signal.TS <= time.Now().Add(-protocol.SignalTTL).UnixMilli() {
				continue // never move a stale signal or another recipient run into this stream
			}
			data, _ := json.Marshal(signal)
			if !write("event: signal\ndata: %s\n\n", data) {
				return
			}
		case <-sub.wake:
		case <-ping.C:
			if !pinged() {
				return
			}
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}

const groupHeadsFrameLimit = 512 << 10
const groupHeadsFrame = "event: groups\ndata: %s\n\n"

// writeGroupHeads keeps the existing independent-head arrays, bounding the
// complete frame. The snapshot is marked sent only after every batch flushes.
func writeGroupHeads(heads []protocol.GroupHead, sent *string, write func(string, ...any) bool) bool {
	if heads == nil {
		heads = []protocol.GroupHead{}
	}
	all, err := json.Marshal(heads)
	if err != nil {
		return false
	}
	if string(all) == *sent {
		return true
	}
	const overhead = len("event: groups\ndata: \n\n")
	batch := make([]json.RawMessage, 0)
	size := overhead + 2 // brackets
	flush := func() bool {
		data, err := json.Marshal(batch)
		return err == nil && len(data)+overhead <= groupHeadsFrameLimit && write(groupHeadsFrame, data)
	}
	for _, head := range heads {
		raw, err := json.Marshal(head)
		if err != nil || len(raw)+overhead+2 > groupHeadsFrameLimit {
			return false
		}
		extra := len(raw)
		if len(batch) != 0 {
			extra++ // comma
		}
		if size+extra > groupHeadsFrameLimit {
			if !flush() {
				return false
			}
			batch = batch[:0]
			size = overhead + 2
			extra = len(raw)
		}
		batch = append(batch, raw)
		size += extra
	}
	if !flush() {
		return false
	}
	*sent = string(all)
	return true
}

func (h *Hub) handleStreamAck(w http.ResponseWriter, r *http.Request) {
	caller, body, ok := h.authenticateBody(w, r)
	if !ok {
		return
	}
	var req protocol.PingAck
	if err := decodeStrict(body, &req); err != nil || !h.streams.ack(caller, req.Conn) {
		writeError(w, http.StatusNotFound, "", "unknown stream")
		return
	}
	h.stats.Acks.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

// holdPending keeps the stream of a device waiting for its person's
// approval: pings only (it has nothing else to receive). When a device of
// the person admits it, it hears "linked" and the stream ends, so it
// reconnects as a member; when it is refused or expires, the stream ends
// and its next request says why.
func (h *Hub) holdPending(ctx context.Context, caller string, sub *subscriber, ping *time.Ticker, lease time.Duration, write func(string, ...any) bool) {
	for {
		// Subscribe first, then read: a decision between authentication and
		// streams.add had nobody to notify. Check it before waiting, too.
		a, err := h.store.agent(caller)
		if err != nil || a.Revoked {
			return
		}
		if !a.Pending {
			write("event: linked\ndata: {}\n\n")
			return
		}
		if a.PendingUntil <= time.Now().Unix() {
			return
		}
		select {
		case <-sub.wake:
		case <-ping.C:
			if time.Since(time.Unix(0, sub.lastAck.Load())) > lease {
				return
			}
			if !write("event: ping\ndata: {\"conn\":%q}\n\n", sub.id) {
				return
			}
		case <-ctx.Done():
			return
		case <-h.done:
			return
		}
	}
}
