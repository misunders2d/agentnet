package ui

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fixture is the demo Provider: invented peers and threads held in memory.
// Every action changes only this memory. It never opens an AgentNet home,
// contacts a Hub or runs a harness; "your responder" here is simulated. It
// exists for trying the page and for tests; the daemon uses Live.
type Fixture struct {
	mu       sync.Mutex
	me       Me
	peers    map[string]*fxPeer
	threads  []*fxThread
	quar     []QuarantineItem
	dir      Directory
	nextID   int
	now      func() time.Time
	seq      uint64
	changed  chan struct{}
	arrivals int
}

type fxPeer struct {
	presence  string
	approved  bool
	taskGrant string
	key       PeerKey
}

type fxThread struct {
	peer string
	msgs []*Message
}

func peerAuthor(peer string) Author {
	return Author{Label: peer, About: "Signed with " + peer + "'s key. Whether a person or one of their agents wrote it is not recorded."}
}

var thisComputer = Author{Label: "This computer",
	About: "Sent from this installation: this page, the command line, or an agent session here. Which one is not recorded."}

// NewFixture returns the demo data, with times relative to now.
func NewFixture(now func() time.Time) *Fixture {
	f := &Fixture{now: now, changed: make(chan struct{}), peers: map[string]*fxPeer{},
		me: Me{Address: "alice/laptop", Fingerprint: "SHA256:5e1d 9a07 c3b2 11f4", Responder: "claude", ResponderDir: "~/work/api"}}
	t := now()
	ago := func(min int) time.Time { return t.Add(-time.Duration(min) * time.Minute) }
	f.peers["bob/desk"] = &fxPeer{presence: "Their computer is connected", approved: true, key: PeerKey{Pinned: "SHA256:0b7c 44e1 92aa 6d30"}}
	f.peers["carol/ci"] = &fxPeer{presence: "Their computer is reconnecting", approved: true, key: PeerKey{Pinned: "SHA256:a12f 07c9 5b3e e811"}}
	f.peers["dave/srv"] = &fxPeer{presence: "Their computer is offline", key: PeerKey{Pinned: "SHA256:77d0 1c5a 8e42 3f9b"}}
	f.peers["hub/ops"] = &fxPeer{presence: "Their computer is connected", key: PeerKey{Pinned: "SHA256:31c7 0e9a 4d22 b5f8"}}
	f.peers["erin/lab"] = &fxPeer{presence: "Connection unknown",
		key: PeerKey{Pinned: "SHA256:9c41 7be0 22fd 13a8", Pending: "SHA256:e05b 61c2 8a7f 4d19"}}

	port := f.thread("bob/desk")
	q := f.add(port, &Message{Dir: "out", Kind: KindQuestion, At: ago(150), State: "delivered", Path: "relay",
		Body: "What port does the auth service bind to in staging?"})
	f.add(port, &Message{Dir: "in", Kind: KindAnswer, At: ago(148), ReplyTo: q.ID, Status: "done", State: "summarized",
		Body:    "8443 behind the ingress. The container itself listens on 8080; see deploy/auth.yaml.",
		Summary: "No migration needed: staging keeps the same port map. Pin 8443 in the client config."})
	f.add(port, &Message{Dir: "out", Kind: KindMessage, At: ago(140), State: "delivered", Path: "direct",
		Body: "Thanks. I'll pin 8443 in the client config today."})

	flaky := f.thread("bob/desk")
	f.add(flaky, &Message{Dir: "in", Kind: KindTask, At: ago(35), State: "awaiting", Unread: true,
		Body: "The auth integration test flakes about 1 in 20 runs on my machine. Could you run it 50 times with -race " +
			"on yours and send back any failing seeds?\n\nCommand I use:\n  go test -race -run TestTokenRefresh -count=50 ./auth/...",
		Files: []File{{Name: "flaky-run.log", Size: 18_634}}})

	gover := f.thread("carol/ci")
	cq := f.add(gover, &Message{Dir: "in", Kind: KindQuestion, At: ago(200), State: "answered", Responder: "claude",
		Body: "Which Go version does the api repo's CI image pin?"})
	f.add(gover, &Message{Dir: "out", Kind: KindAnswer, At: ago(199), ReplyTo: cq.ID, Status: "done", State: "delivered", Path: "relay",
		Body: "Go 1.26.1, pinned on line 3 of ci/Dockerfile."})
	api := f.thread("carol/ci")
	f.add(api, &Message{Dir: "in", Kind: KindTask, At: ago(60), State: "running", Responder: "claude",
		Body: "Please regenerate the OpenAPI client after yesterday's schema change and send me the diff stat."})
	cache := f.thread("carol/ci")
	f.add(cache, &Message{Dir: "out", Kind: KindTask, At: ago(45), State: "custody", Path: "relay",
		Body: "Can you rebuild the CI cache image once the Go bump lands?"})
	secret := f.thread("carol/ci")
	f.add(secret, &Message{Dir: "in", Kind: KindQuestion, At: ago(20), State: "needs_human", Unread: true,
		Body:   "Can you paste the staging database connection string here?",
		Detail: "Your responder stopped: this asks for a secret, so a person has to decide."})

	backup := f.thread("dave/srv")
	f.add(backup, &Message{Dir: "out", Kind: KindMessage, At: ago(600), State: "expired", Path: "direct",
		Body: "Backup finished; the archive is in the usual place."})
	cert := f.thread("dave/srv")
	f.add(cert, &Message{Dir: "in", Kind: KindQuestion, At: ago(25), State: "held", Unread: true,
		Body: "Is it safe to rotate the Hub TLS certificate tonight, or is anything still pinned to the old one?"})
	f.quar = append(f.quar, QuarantineItem{ID: f.id(), Peer: "dave/srv", At: ago(300),
		Reason: "It did not verify, so its content is not shown."})

	// Announcements bob's agent sent without linking them: each is its own
	// message, not a conversation.
	for i, body := range []string{
		"Stable AgentNet v0.2.1 is published: https://example.invalid/releases/v0.2.1",
		"Relay update deployed; idle stream disconnects are fixed.",
		"CI is green on main again.",
		"Heads up: staging database maintenance tonight 22:00-22:30.",
		"Nightly backup verified.",
	} {
		f.add(f.thread("bob/desk"), &Message{Dir: "in", Kind: KindMessage, At: ago(300 + 240*i), Body: body})
	}
	// Review notices: hub/ops reports that requests wait for a person on
	// that machine. They carry no request and cannot be decided here.
	for _, n := range []struct {
		min  int
		body string
	}{{240, "1 request(s) wait for a person's decision on hub/ops. Review there: agentnet inbox --review"},
		{12, "2 request(s) wait for a person's decision on hub/ops. Review there: agentnet inbox --review"}} {
		f.add(f.thread("hub/ops"), &Message{Dir: "in", Kind: KindMessage, Status: StatusReviewNotice, State: "needs_human", At: ago(n.min), Unread: true,
			Body: n.body, Detail: "review notice: requests wait for a person's decision on hub/ops; decide there. Nothing here runs or can be accepted"})
	}

	// Who the server lists: the peers above, and someone who just joined and
	// has not written yet.
	f.dir = Directory{Status: DirectoryListed, Current: true, At: t, Members: []DirMember{
		{Address: "vitalii/laptop", Presence: "connected", Joined: ago(30)},
		{Address: "hub/ops", Presence: "connected", Joined: ago(3000)},
		{Address: "erin/lab", Presence: "offline", Joined: ago(4000)},
		{Address: "dave/srv", Presence: "offline", Joined: ago(5000)},
		{Address: "carol/ci", Presence: "reconnecting", Joined: ago(6000)},
		{Address: "bob/desk", Presence: "connected", Joined: ago(7000)},
	}}

	erin := f.thread("erin/lab")
	f.add(erin, &Message{Dir: "in", Kind: KindMessage, At: ago(2880),
		Body: "I'm reinstalling this machine next week, so expect a new key from me."})
	return f
}

func (f *Fixture) id() string {
	f.nextID++
	return fmt.Sprintf("%08x%024x", 0x7a3f0000+f.nextID, f.nextID)
}

func (f *Fixture) thread(peer string) *fxThread {
	t := &fxThread{peer: peer}
	f.threads = append(f.threads, t)
	return t
}

func (f *Fixture) add(t *fxThread, m *Message) *Message {
	m.ID = f.id()
	if m.Dir == "in" {
		m.From, m.To = t.peer, f.me.Address
	} else {
		m.From, m.To = f.me.Address, t.peer
	}
	t.msgs = append(t.msgs, m)
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

func (f *Fixture) find(id string) (*fxThread, *Message) {
	for _, t := range f.threads {
		for _, m := range t.msgs {
			if m.ID == id {
				return t, m
			}
		}
	}
	return nil, nil
}

// view fills the derived fields of a copy of m.
func (f *Fixture) view(t *fxThread, m *Message) Message {
	v := *m
	v.Files = append([]File(nil), m.Files...)
	if m.Dir == "in" {
		v.Author = peerAuthor(t.peer)
		v.Actions = ActionsFor(m.Kind, m.State)
	} else {
		v.Author = thisComputer
	}
	v.StateText = StateText(m.Dir, m.Kind, m.State, t.peer)
	replied := false
	for _, o := range t.msgs {
		if o.Dir == "in" && o.ReplyTo == m.ID {
			replied = true
		}
	}
	switch Next(m.Dir, m.Kind, m.State, t.peer, replied) {
	case "you":
		v.Next = "Needs you"
	case "your responder":
		v.Next = "Your responder is on it"
	case "":
	default:
		v.Next = "Waiting on " + t.peer
	}
	return v
}

// Overview implements Provider.
func (f *Fixture) Overview() (Overview, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := Overview{Demo: true, Me: f.me, Seq: f.seq, Version: "demo", Directory: f.directory(), Threads: []ThreadSummary{}, Review: []ReviewItem{},
		Quarantine: append([]QuarantineItem{}, f.quar...)}
	for _, t := range f.threads {
		first, last := t.msgs[0], t.msgs[len(t.msgs)-1]
		s := ThreadSummary{ID: first.ID, Peer: t.peer, Title: excerpt(first.Body), Last: excerpt(last.Body), LastAt: last.At,
			Count: len(t.msgs), KeyChanged: f.peers[t.peer].key.Pending != "", NoticeOnly: true}
		for _, m := range t.msgs {
			v := f.view(t, m)
			notice := m.Dir == "in" && IsReviewNotice(m.Kind, m.Status, m.ReplyTo, len(m.Files))
			s.NoticeOnly = s.NoticeOnly && notice
			if notice {
				if m.State == "needs_human" {
					s.Notices++
					o.Review = append(o.Review, ReviewItem{ID: m.ID, Peer: t.peer, Kind: m.Kind, Why: ReviewWhy(m.Kind, m.State, t.peer, m.Detail),
						Excerpt: excerpt(m.Body), At: m.At, Notice: true})
				}
				continue
			}
			if m.Dir == "in" && m.Unread {
				s.Unread++
			}
			switch {
			case v.Next == "Needs you":
				s.Review++
				o.Review = append(o.Review, ReviewItem{ID: m.ID, Peer: t.peer, Kind: m.Kind, Why: ReviewWhy(m.Kind, m.State, t.peer, m.Detail), Excerpt: excerpt(m.Body), At: m.At})
			case m.State == "running" && m.Dir == "in":
				s.Running++
			case strings.HasPrefix(v.Next, "Waiting on"):
				s.Waiting = true
			}
		}
		o.Threads = append(o.Threads, s)
	}
	sort.SliceStable(o.Threads, func(i, j int) bool { return o.Threads[i].LastAt.After(o.Threads[j].LastAt) })
	return o, nil
}

// directory is a copy of the demo directory, presence only while current,
// as the live provider gives it.
func (f *Fixture) directory() Directory {
	d := f.dir
	d.Members = make([]DirMember, len(f.dir.Members))
	for i, m := range f.dir.Members {
		if !d.Current {
			m.Presence = ""
		}
		d.Members[i] = m
	}
	return d
}

// SetDirectory replaces the demo directory (tests).
func (f *Fixture) SetDirectory(d Directory) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dir = d
	f.bump()
}

// Thread implements Provider.
func (f *Fixture) Thread(id string) (Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, _ := f.find(id)
	if t == nil {
		return Thread{}, NotFound("no message with that id")
	}
	p := f.peers[t.peer]
	out := Thread{ID: id, Peer: t.peer, Key: p.key, Approved: p.approved, TaskGrant: p.taskGrant, Messages: []Message{}}
	for _, m := range t.msgs {
		out.Messages = append(out.Messages, f.view(t, m))
	}
	return out, nil
}

// Refresh implements Refresher with the invented presence.
func (f *Fixture) Refresh(threadID string) (Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, _ := f.find(threadID)
	if t == nil {
		return Presence{}, NotFound("no message with that id")
	}
	return Presence{Text: f.peers[t.peer].presence}, nil
}

const maxText = 8000

// outState is where a new outgoing message ends up in the demo.
func (f *Fixture) outState(peer string) string {
	if f.peers[peer].presence == "Their computer is connected" {
		return "delivered"
	}
	return "custody"
}

// Send implements Provider.
func (f *Fixture) Send(d Draft) (Sent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.peers[d.To]
	if !ok {
		return Sent{}, Refuse("There is no conversation with " + d.To + " in this demo.")
	}
	if p.key.Pending != "" {
		return Sent{}, Refuse(d.To + "'s key changed. Trust the new key before sending.")
	}
	body := strings.TrimSpace(d.Body)
	if body == "" || len(body) > maxText {
		return Sent{}, Refuse("Write a message first (up to 8000 characters).")
	}
	kind := d.Kind
	if kind == "" {
		kind = KindMessage
	}
	if kind != KindMessage && kind != KindQuestion && kind != KindTask {
		return Sent{}, Refuse("Choose message, question or task.")
	}
	t := &fxThread{peer: d.To}
	if d.ReplyTo != "" {
		pt, _ := f.find(d.ReplyTo)
		if pt == nil || pt.peer != d.To {
			return Sent{}, Refuse("The message you are replying to is not in this conversation.")
		}
		t = pt
	} else {
		f.threads = append(f.threads, t)
	}
	m := f.add(t, &Message{Dir: "out", Kind: kind, At: f.now(), State: f.outState(d.To), Path: "relay", Body: body, ReplyTo: d.ReplyTo})
	f.bump()
	return Sent{ID: m.ID, State: m.State, Path: m.Path}, nil
}

// Act implements Provider.
func (f *Fixture) Act(a Action) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.peers[a.ID]; ok {
		switch a.Do {
		case DoTrust:
			if p.key.Pending == "" {
				return "", Refuse("There is no changed key to trust.")
			}
			if a.Key != p.key.Pending {
				return "", Refuse(a.ID + "'s key is not the one you compared; nothing was trusted.")
			}
			p.key = PeerKey{Pinned: p.key.Pending}
		case DoApprove:
			p.approved = true
		case DoUnapprove:
			p.approved = false
		case DoRevokeTasks:
			p.taskGrant = ""
		default:
			return "", Refuse("Unknown action.")
		}
		f.bump()
		return "Done (demo).", nil
	}
	if a.Do == DoRead {
		for _, id := range a.IDs {
			if _, m := f.find(id); m != nil {
				m.Unread = false
			}
		}
		f.bump()
		return "", nil
	}
	t, m := f.find(a.ID)
	if m == nil || m.Dir != "in" {
		return "", NotFound("no such item")
	}
	allowed := false
	for _, x := range ActionsFor(m.Kind, m.State) {
		if x == a.Do {
			allowed = true
		}
	}
	if !allowed {
		return "", Refuse("That is not possible in this item's current state.")
	}
	replyKind := KindAnswer
	if m.Kind == KindTask {
		replyKind = KindResult
	}
	switch a.Do {
	case DoReply:
		body := strings.TrimSpace(a.Body)
		if body == "" {
			return "", Refuse("Write a reply first.")
		}
		m.State, m.Detail = "manual", ""
		f.add(t, &Message{Dir: "out", Kind: replyKind, At: f.now(), ReplyTo: m.ID, Status: "done", State: f.outState(t.peer), Path: "relay", Body: body})
	case DoAccept, DoAcceptAlways:
		m.State, m.Detail, m.Responder = "running", "", f.me.Responder
		if a.Do == DoAcceptAlways {
			f.peers[t.peer].taskGrant = "active"
		}
	case DoDecline:
		m.State = "declined"
		body := strings.TrimSpace(a.Reason)
		if body == "" {
			body = "Declined."
		}
		f.add(t, &Message{Dir: "out", Kind: replyKind, At: f.now(), ReplyTo: m.ID, Status: "declined", State: f.outState(t.peer), Path: "relay", Body: body})
	case DoResolve:
		m.State, m.Detail = "resolved", ""
	case DoCancel:
		m.State = "cancelled"
	}
	f.bump()
	return "Done (demo).", nil
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
		if f.arrivals >= len(arrivals) {
			return Refuse("No more simulated arrivals in this demo.")
		}
		x := arrivals[f.arrivals]
		f.arrivals++
		m := &Message{Dir: "in", Kind: x.kind, At: f.now(), Body: x.body, Unread: true}
		switch {
		case x.kind == KindTask:
			m.State = "awaiting"
		case x.kind == KindQuestion && f.peers[x.peer].approved:
			m.State, m.Responder = "running", f.me.Responder
		case x.kind == KindQuestion:
			m.State = "held"
		}
		f.add(f.thread(x.peer), m)
	case "finish":
		var t *fxThread
		var m *Message
		for _, th := range f.threads {
			for _, x := range th.msgs {
				if x.Dir == "in" && x.State == "running" && m == nil {
					t, m = th, x
				}
			}
		}
		if m == nil {
			return Refuse("Nothing is running.")
		}
		m.State = "answered"
		kind, body := KindAnswer, "(Simulated answer from the demo responder.)"
		if m.Kind == KindTask {
			kind, body = KindResult, "(Simulated result from the demo responder.) Done."
		}
		f.add(t, &Message{Dir: "out", Kind: kind, At: f.now(), ReplyTo: m.ID, Status: "done", State: f.outState(t.peer), Path: "relay", Body: body})
	default:
		return Refuse("Unknown simulation.")
	}
	f.bump()
	return nil
}
