package ui

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fixture is the demo Provider: invented peers and messages held in memory.
// Every action changes only this memory. It never opens an AgentNet home,
// contacts a Hub or runs a harness; "your responder" here is simulated.
type Fixture struct {
	mu      sync.Mutex
	me      string
	harness string
	dir     string
	peers   map[string]*fxPeer
	order   []string // peers in creation order
	nextID  int
	now     func() time.Time
	seq     uint64
	changed chan struct{}
	arrived int // simulated arrivals used
}

type fxPeer struct {
	addr     string
	presence string
	approved bool
	notice   *Notice
	msgs     []*Message
}

// Author descriptions. Only what is recorded is claimed.
var (
	authorThisComputer = Author{Label: "This computer",
		About: "Sent from this installation. Whether a person or one of your agent sessions ran the send command is not recorded."}
	authorByHand = Author{Label: "This computer, reply by hand",
		About: "Sent with the reply command. Whether a person or one of your agent sessions typed it is not recorded."}
	authorWindow = Author{Label: "Sent from this window",
		About: "Written and sent from this page."}
	authorSystem = Author{Label: "AgentNet on this computer",
		About: "A note from this installation, not a message from anyone."}
)

func authorResponder(harness, kind string) Author {
	if kind == KindResult {
		return Author{Label: "Your responder (" + harness + ")",
			About: harness + " ran the task after you accepted it, with its normal permissions, and sent this result automatically."}
	}
	return Author{Label: "Your responder (" + harness + ")",
		About: harness + " answered in question mode (no tools) and sent this automatically."}
}

func authorPeer(peer string) Author {
	return Author{Label: peer,
		About: "Signed with " + peer + "'s key. Whether a person or one of their agents wrote it is not recorded."}
}

// NewFixture returns the demo conversations, with times relative to now.
func NewFixture(now func() time.Time) *Fixture {
	f := &Fixture{me: "alice/laptop", harness: "Claude", dir: "~/work/api",
		peers: map[string]*fxPeer{}, now: now, changed: make(chan struct{})}
	t := now()
	ago := func(min int) time.Time { return t.Add(-time.Duration(min) * time.Minute) }

	bob := f.peer("bob/desk", "Their computer is connected", true)
	q := f.add(bob, &Message{Dir: "out", Kind: KindQuestion, At: ago(150), State: "delivered", Path: "relay",
		Author: authorThisComputer, Body: "What port does the auth service bind to in staging?"})
	f.add(bob, &Message{Dir: "in", Kind: KindAnswer, At: ago(148), ReplyTo: q.ID, Status: "done",
		Author: authorPeer("bob/desk"), State: "summarized",
		Body: "8443 behind the ingress. The container itself listens on 8080; see deploy/auth.yaml.",
		Note: &Note{Label: "Summary written on this computer by your responder (Claude), from your follow-up request",
			Text: "No migration needed: staging keeps the same port map. Pin 8443 in the client config."}})
	f.add(bob, &Message{Dir: "out", Kind: KindMessage, At: ago(140), State: "delivered", Path: "direct",
		Author: authorThisComputer, Body: "Thanks. I'll pin 8443 in the client config today."})
	f.add(bob, &Message{Dir: "in", Kind: KindTask, At: ago(35), State: "awaiting", Author: authorPeer("bob/desk"),
		Body: "The auth integration test flakes about 1 in 20 runs on my machine. Could you run it 50 times with -race " +
			"on yours and send back any failing seeds?\n\nCommand I use:\n  go test -race -run TestTokenRefresh -count=50 ./auth/...",
		Files: []File{{Name: "flaky-run.log", Size: 18_634}}})
	f.add(bob, &Message{Dir: "in", Kind: KindMessage, At: ago(12),
		Author: Author{Label: "bob/desk · their Codex session", Future: true,
			About: "Illustration only: the protocol has no author field today. A future version could carry the sender's own " +
				"statement of who wrote a message; it would still be their claim, not verified identity."},
		Body: "Heads-up: I pushed the ingress change to staging, so the old 9443 route is gone."})

	carol := f.peer("carol/ci", "Their computer is reconnecting", true)
	cq := f.add(carol, &Message{Dir: "in", Kind: KindQuestion, At: ago(200), State: "answered", Author: authorPeer("carol/ci"),
		Body: "Which Go version does the api repo's CI image pin?"})
	f.add(carol, &Message{Dir: "out", Kind: KindAnswer, At: ago(199), ReplyTo: cq.ID, Status: "done", State: "delivered",
		Path: "relay", Author: authorResponder("Claude", KindAnswer), Body: "Go 1.26.1, pinned on line 3 of ci/Dockerfile."})
	f.add(carol, &Message{Dir: "in", Kind: KindTask, At: ago(60), State: "running", Author: authorPeer("carol/ci"),
		Body: "Please regenerate the OpenAPI client after yesterday's schema change and send me the diff stat."})
	f.add(carol, &Message{Dir: "out", Kind: KindTask, At: ago(45), State: "custody", Path: "relay", Author: authorThisComputer,
		Body: "Can you rebuild the CI cache image once the Go bump lands?"})
	f.add(carol, &Message{Dir: "in", Kind: KindQuestion, At: ago(20), State: "needs_human", Author: authorPeer("carol/ci"),
		Body:   "Can you paste the staging database connection string here?",
		Detail: "Your responder stopped: this asks for a secret, so a person has to decide."})

	dave := f.peer("dave/srv", "Their computer is offline", false)
	f.add(dave, &Message{Dir: "out", Kind: KindMessage, At: ago(600), State: "expired", Path: "direct", Author: authorThisComputer,
		Body: "Backup finished; the archive is in the usual place."})
	f.add(dave, &Message{Dir: "system", Kind: KindSystem, At: ago(300), State: "quarantined", Author: authorSystem,
		Body: "A message from dave/srv was set aside because its signature did not match. Its content is not shown."})
	f.add(dave, &Message{Dir: "in", Kind: KindQuestion, At: ago(25), State: "held", Author: authorPeer("dave/srv"),
		Body: "Is it safe to rotate the Hub TLS certificate tonight, or is anything still pinned to the old one?"})

	erin := f.peer("erin/lab", "Connection unknown", true)
	erin.notice = &Notice{Kind: "key_changed",
		Text: "erin/lab's key changed. Sending is paused and new messages from erin/lab are held until you confirm the new key.",
		Old:  "SHA256:9c41 7be0 22fd 13a8", New: "SHA256:e05b 61c2 8a7f 4d19"}
	f.add(erin, &Message{Dir: "in", Kind: KindMessage, At: ago(2880), Author: authorPeer("erin/lab"),
		Body: "I'm reinstalling this machine next week, so expect a new key from me."})
	return f
}

func (f *Fixture) peer(addr, presence string, approved bool) *fxPeer {
	p := &fxPeer{addr: addr, presence: presence, approved: approved}
	f.peers[addr] = p
	f.order = append(f.order, addr)
	return p
}

func (f *Fixture) add(p *fxPeer, m *Message) *Message {
	f.nextID++
	m.ID = fmt.Sprintf("%08x%024x", 0x7a3f0000+f.nextID, f.nextID)
	m.Peer = p.addr
	p.msgs = append(p.msgs, m)
	return m
}

// bump records a change and wakes event streams. Callers hold mu.
func (f *Fixture) bump() {
	f.seq++
	close(f.changed)
	f.changed = make(chan struct{})
}

// Changed implements Provider.
func (f *Fixture) Changed() (uint64, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq, f.changed
}

// view fills the derived fields of a copy of m.
func (f *Fixture) view(p *fxPeer, m *Message) Message {
	v := *m
	v.Files = append([]File(nil), m.Files...)
	v.StateText = StateText(m.Dir, m.Kind, m.State, p.addr)
	answered := false
	for _, o := range p.msgs {
		if o.ReplyTo == m.ID && o.Dir == "in" {
			answered = true
		}
	}
	switch Next(m.Dir, m.Kind, m.State, p.addr, answered) {
	case "you":
		v.Next = "Needs you"
	case "your responder":
		v.Next = "Your responder is on it"
	case "":
	default:
		v.Next = "Waiting on " + p.addr
	}
	if m.Dir == "in" {
		switch m.State {
		case "held":
			v.Actions = []string{"reply", "approve", "decline"}
		case "awaiting":
			v.Actions = []string{"accept", "decline"}
		case "needs_human":
			v.Actions = []string{"reply", "accept", "resolve"}
		case "running":
			v.Actions = []string{"cancel"}
		}
	}
	return v
}

// State implements Provider.
func (f *Fixture) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := State{Demo: true, Me: f.me, Seq: f.seq, Conversations: []ConvSummary{}, Review: []ReviewItem{},
		Machine: Machine{Hub: "Connected to the Hub (simulated)", Responder: f.harness, ResponderDir: f.dir}}
	for _, addr := range f.order {
		p := f.peers[addr]
		c := ConvSummary{Peer: addr, Presence: p.presence, Paused: p.notice != nil}
		you, responder, waiting := false, false, false
		for _, m := range p.msgs {
			v := f.view(p, m)
			switch {
			case v.Next == "Needs you":
				you = true
				c.Review++
				why := v.StateText
				if m.Detail != "" {
					why = m.Detail
				}
				s.Review = append(s.Review, ReviewItem{ID: m.ID, Peer: addr, Kind: m.Kind,
					Why: strings.TrimPrefix(why, "Needs you: "), Excerpt: excerpt(m.Body)})
			case strings.HasPrefix(v.Next, "Your responder"):
				responder = true
			case v.Next != "":
				waiting = true
			}
		}
		switch {
		case you:
			c.Next = "Needs you"
		case responder:
			c.Next = "Your responder is working"
		case waiting:
			c.Next = "Waiting on " + addr
		}
		if n := len(p.msgs); n > 0 {
			last := p.msgs[n-1]
			c.Last, c.LastAt = excerpt(last.Body), last.At
		}
		s.Conversations = append(s.Conversations, c)
	}
	sort.SliceStable(s.Conversations, func(i, j int) bool { return s.Conversations[i].LastAt.After(s.Conversations[j].LastAt) })
	return s
}

// Conversation implements Provider.
func (f *Fixture) Conversation(peer string) (Conversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.peers[peer]
	if !ok {
		return Conversation{}, NotFound("no conversation with " + peer)
	}
	c := Conversation{Peer: peer, Presence: p.presence, Notice: p.notice, Messages: []Message{}}
	for _, m := range p.msgs {
		c.Messages = append(c.Messages, f.view(p, m))
	}
	return c, nil
}

func (f *Fixture) find(id string) (*fxPeer, *Message) {
	for _, p := range f.peers {
		for _, m := range p.msgs {
			if m.ID == id {
				return p, m
			}
		}
	}
	return nil, nil
}

const maxText = 8000

// Send implements Provider. Replies take over the item they answer, as
// `agentnet reply` does.
func (f *Fixture) Send(d Draft) (Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.peers[d.Peer]
	if !ok {
		return Message{}, NotFound("no conversation with " + d.Peer)
	}
	if p.notice != nil {
		return Message{}, Refuse("Sending to " + p.addr + " is paused until you confirm their new key.")
	}
	body := strings.TrimSpace(d.Body)
	if body == "" || len(body) > maxText {
		return Message{}, Refuse("Write a message first (up to 8000 characters).")
	}
	if len(d.Files) > 8 {
		return Message{}, Refuse("Attach at most 8 files.")
	}
	kind := d.Kind
	var parent *Message
	if d.ReplyTo != "" {
		pp, m := f.find(d.ReplyTo)
		if m == nil || pp != p {
			return Message{}, NotFound("the message you are replying to is not in this conversation")
		}
		parent = m
		kind = KindMessage
		if m.Dir == "in" && m.Kind == KindQuestion {
			kind = KindAnswer
		} else if m.Dir == "in" && m.Kind == KindTask {
			kind = KindResult
		}
	} else if kind != KindMessage && kind != KindQuestion && kind != KindTask {
		return Message{}, Refuse("Choose message, question or task.")
	}
	if parent != nil && (kind == KindAnswer || kind == KindResult) {
		switch parent.State {
		case "held", "awaiting", "needs_human", "pending", "failed", "interrupted":
			parent.State, parent.Detail = "manual", ""
		default:
			return Message{}, Refuse("That item is already answered or being worked on.")
		}
	}
	state := "delivered"
	if !strings.Contains(p.presence, "connected") {
		state = "custody"
	}
	m := &Message{Dir: "out", Kind: kind, At: f.now(), State: state, Path: "relay", Author: authorWindow,
		Body: body, ReplyTo: d.ReplyTo, Files: d.Files}
	if kind == KindAnswer || kind == KindResult {
		m.Status = "done"
	}
	f.add(p, m)
	f.bump()
	return f.view(p, m), nil
}

// Act implements Provider.
func (f *Fixture) Act(a Action) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a.Do == "trust" {
		p, ok := f.peers[a.ID]
		if !ok || p.notice == nil {
			return Refuse("There is no changed key to confirm.")
		}
		p.notice = nil
		f.bump()
		return nil
	}
	p, m := f.find(a.ID)
	if m == nil || m.Dir != "in" {
		return NotFound("no such item")
	}
	from := func(states ...string) bool {
		for _, s := range states {
			if m.State == s {
				return true
			}
		}
		return false
	}
	switch a.Do {
	case "accept":
		if !from("awaiting", "held", "needs_human") {
			return Refuse("This item cannot be accepted now.")
		}
		m.State, m.Detail = "running", ""
	case "approve":
		if !from("held") {
			return Refuse("Only a held question can be approved from here.")
		}
		p.approved = true
		m.State = "running"
	case "decline":
		if !from("held", "awaiting") {
			return Refuse("Only a held question or a waiting task can be declined.")
		}
		m.State = "declined"
		kind := KindAnswer
		if m.Kind == KindTask {
			kind = KindResult
		}
		body := strings.TrimSpace(a.Reason)
		if body == "" {
			body = "Declined."
		}
		f.add(p, &Message{Dir: "out", Kind: kind, At: f.now(), ReplyTo: m.ID, Status: "declined",
			State: "delivered", Path: "relay", Author: authorWindow, Body: body})
	case "resolve":
		if !from("needs_human") {
			return Refuse("Only an item that needs you can be closed.")
		}
		m.State, m.Detail = "resolved", ""
	case "cancel":
		if !from("running") {
			return Refuse("Nothing is running for this item.")
		}
		m.State = "cancelled"
	default:
		return Refuse("Unknown action.")
	}
	f.bump()
	return nil
}

// Simulate implements Simulator: "arrival" delivers the next invented
// message; "finish" makes the simulated responder complete running work.
func (f *Fixture) Simulate(what string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch what {
	case "arrival":
		arrivals := []struct{ peer, kind, body string }{
			{"bob/desk", KindMessage, "Staging deploy finished. Auth is on 8443 behind the ingress now."},
			{"carol/ci", KindQuestion, "Is the api repo still on the v2 lint config, or did you move to v3?"},
			{"bob/desk", KindTask, "When you have a minute: bump the auth client's retry budget from 3 to 5 and run the unit tests."},
		}
		if f.arrived >= len(arrivals) {
			return Refuse("No more simulated arrivals in this demo.")
		}
		a := arrivals[f.arrived]
		f.arrived++
		p := f.peers[a.peer]
		m := &Message{Dir: "in", Kind: a.kind, At: f.now(), Author: authorPeer(a.peer), Body: a.body}
		switch {
		case a.kind == KindTask:
			m.State = "awaiting"
		case a.kind == KindQuestion && p.approved:
			m.State = "running"
		case a.kind == KindQuestion:
			m.State = "held"
		}
		f.add(p, m)
	case "finish":
		var p *fxPeer
		var m *Message
		for _, addr := range f.order {
			for _, x := range f.peers[addr].msgs {
				if x.Dir == "in" && x.State == "running" && m == nil {
					p, m = f.peers[addr], x
				}
			}
		}
		if m == nil {
			return Refuse("Nothing is running.")
		}
		m.State = "answered"
		kind, body, files := KindAnswer, "(Simulated answer from the demo responder.)", []File(nil)
		if m.Kind == KindTask {
			kind = KindResult
			body = "(Simulated result from the demo responder.) Done; details attached."
			files = []File{{Name: "result.txt", Size: 2_048}}
		}
		f.add(p, &Message{Dir: "out", Kind: kind, At: f.now(), ReplyTo: m.ID, Status: "done", State: "delivered",
			Path: "relay", Author: authorResponder(f.harness, kind), Body: body, Files: files})
	default:
		return Refuse("Unknown simulation.")
	}
	f.bump()
	return nil
}
