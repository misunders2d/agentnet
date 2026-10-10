// Package protocol defines the AgentNet Hub HTTP contract shared by the Hub and
// clients: addresses, invite codes, request authentication, and wire bodies.
// It is AgentNet's own directory/relay API, not A2A.
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
)

// Version is the program version, set at build time with
// -ldflags "-X github.com/misunders2d/agentnet/internal/protocol.Version=...".
var Version = "dev"

// ProtocolVersion is the Hub API generation. Clients and Hubs with the same
// value interoperate; a different value means one of them must be updated.
const ProtocolVersion = 1

// VersionInfo is the Hub's unauthenticated GET /v1/version answer.
type VersionInfo struct {
	Version  string   `json:"version"`
	Protocol int      `json:"protocol"`
	Features []string `json:"features,omitempty"` // Feature*; absent on older relays
	RealmID  string   `json:"realm_id,omitempty"` // opaque workspace namespace; not authority
}

// MaxBody bounds every request body the Hub reads.
const MaxBody = 1 << 20

// ClockSkew is the accepted difference between request and Hub clocks.
const ClockSkew = 5 * time.Minute

// MaxName is the longest valid label or agent name (namePattern).
const MaxName = 32

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidName reports whether s is a valid person label or agent name.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// Address joins a person label and agent name as "label/agent".
func Address(label, agent string) string { return label + "/" + agent }

// SplitAddress validates and splits "label/agent".
func SplitAddress(addr string) (label, agent string, err error) {
	label, agent, ok := strings.Cut(addr, "/")
	if !ok || !ValidName(label) || !ValidName(agent) {
		return "", "", fmt.Errorf("invalid address %q (want person/agent)", addr)
	}
	return label, agent, nil
}

// NewID returns a random 128-bit hex identifier.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Invite is what an invite code carries. CertPEM pins a self-signed Hub
// certificate; when empty the system trust store is used.
type Invite struct {
	Hub     string `json:"hub"`
	Label   string `json:"label"`
	Secret  string `json:"secret"`
	CertPEM string `json:"cert,omitempty"`
	// Name, From and Workspace are what the inviter wrote on the
	// invitation (invitehints.go): the invited person's name, the inviter's
	// name and the workspace's name. Nobody signs them: a page shows them
	// as what the invitation says, next to the Hub's own address, and they
	// grant nothing.
	Name      string `json:"name,omitempty"`
	From      string `json:"from,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

const invitePrefix = "agentnet-invite-v1:"

// Encode renders the invite as a single copyable token.
func (inv Invite) Encode() string {
	data, _ := json.Marshal(inv)
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

// DecodeInvite parses a token produced by Invite.Encode.
func DecodeInvite(code string) (Invite, error) {
	var inv Invite
	raw, ok := strings.CutPrefix(strings.TrimSpace(code), invitePrefix)
	if !ok {
		return inv, errors.New("not an AgentNet invite code")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return inv, err
	}
	if err := json.Unmarshal(data, &inv); err != nil {
		return inv, err
	}
	if inv.Secret == "" || !ValidName(inv.Label) {
		return inv, errors.New("incomplete invite code")
	}
	// A hint that is not readable text is dropped, never shown: it is only
	// the inviter's words, and the invitation works without it.
	inv.Name, inv.From, inv.Workspace = cleanHint(inv.Name, MaxInviteHint), cleanHint(inv.From, MaxInviteHint), cleanHint(inv.Workspace, MaxWorkspaceHint)
	inv.Hub, err = NormalizeHubURL(inv.Hub)
	return inv, err
}

// HashSecret is how the Hub stores invite secrets.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// JoinRequest enrolls a new agent. Sig proves possession of the signing key
// over every other field.
type JoinRequest struct {
	Secret string          `json:"secret"`
	Public identity.Public `json:"public"`
	Link   *JoinLink       `json:"link,omitempty"` // required with a device invite (link.go)
	Sig    []byte          `json:"sig"`
}

func (j JoinRequest) signed() []byte {
	j.Sig = nil
	data, _ := json.Marshal(j)
	return append([]byte("agentnet-join-v1\n"), data...)
}

// SignJoin fills in the proof-of-possession signature.
func SignJoin(j *JoinRequest, key ed25519.PrivateKey) { j.Sig = ed25519.Sign(key, j.signed()) }

// VerifyJoin checks the proof-of-possession signature and key binding.
func VerifyJoin(j JoinRequest) error {
	if err := j.Public.Verify(); err != nil {
		return err
	}
	if !ed25519.Verify(j.Public.SignKey, j.signed(), j.Sig) {
		return errors.New("join request signature invalid")
	}
	return nil
}

// MaxInviteTTL is the longest invite lifetime the Hub keeps; it gives an
// out-of-range TTL its default week instead.
const MaxInviteTTL = 30 * 24 * time.Hour

// InviteRequest asks the Hub (as admin) to mint an invite.
type InviteRequest struct {
	// Label is the invited person's label (the first half of their
	// addresses). It may be left out when Name is given: the Hub then
	// makes a free one from the name (hub/invites.go).
	Label string        `json:"label,omitempty"`
	TTL   time.Duration `json:"ttl"`
	Admin bool          `json:"admin"`
	// Browser asks for an invite a browser can use: the Hub refuses it,
	// creating nothing, unless it serves the browser messenger over HTTPS
	// that browsers trust (no certificate pin).
	Browser bool `json:"browser,omitempty"`
	// Name, From and Workspace are written on the invitation as its
	// unsigned hints (Invite); each must pass ValidInviteHint.
	Name      string `json:"name,omitempty"`
	From      string `json:"from,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// InviteCreated is the Hub's answer to an InviteRequest: the code and the
// label the invited person will have.
type InviteCreated struct {
	Code  string `json:"code"`
	Label string `json:"label,omitempty"` // absent from older Hubs
}

// PendingInvite is one unused invitation an admin made (GET
// /v1/admin/invites). ID is the stored secret hash, never the secret.
type PendingInvite struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Name      string `json:"name,omitempty"`
	Admin     bool   `json:"admin"`
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at,omitempty"` // unix seconds; 0 for invites made before it was kept
	Expires   int64  `json:"expires"`              // unix seconds
}

// PendingInvites answers GET /v1/admin/invites for any member: whether the
// caller may invite people (its device is an admin on this Hub), and, for
// an admin only, the unused invitations admins made.
type PendingInvites struct {
	CanInvite bool            `json:"can_invite"`
	Invites   []PendingInvite `json:"invites"`
}

// InviteRevokeRequest asks the Hub (as admin) to withdraw an unused
// invitation by its ID.
type InviteRevokeRequest struct {
	ID string `json:"id"`
}

// RevokeRequest asks the Hub (as admin) to revoke an agent.
type RevokeRequest struct {
	Address string `json:"address"`
}

// Receipt reports what the Hub can prove about a message.
const StreamReceipts = "receipts"
const ReceiptBatch = 100

type ReceiptEvent struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Seq   int64  `json:"seq"`
}

type Receipt struct {
	ID    string `json:"id"`
	State string `json:"state"`          // one of the State constants
	Path  string `json:"path,omitempty"` // PathDirect or PathRelay, when known
}

// Delivery paths.
const (
	PathDirect = "direct" // straight to the recipient's daemon
	PathRelay  = "relay"  // through the Hub
)

// Message states the Hub can attest. A recipient moves a message from
// custody to delivered or quarantined; quarantined may later become
// delivered when the recipient explicitly trusts a changed sender key.
const (
	StateCustody     = "custody"     // Hub persisted the envelope
	StateDelivered   = "delivered"   // recipient verified, decrypted and stored it
	StateQuarantined = "quarantined" // recipient received it but could not verify it
	StateExpired     = "expired"     // addressed to a session that ended before delivery
)

// AckRequest is the recipient's disposition of a pushed message.
type AckRequest struct {
	State string `json:"state"` // StateDelivered or StateQuarantined
}

// NormalizeHubURL reduces a Hub URL to its https origin. Paths other than
// "/", queries, fragments and credentials are rejected: the Hub API is
// always served from the origin root.
func NormalizeHubURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid Hub URL %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("Hub URL must be https://host[:port], got %q", raw)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || (u.Path != "" && u.Path != "/") || u.Opaque != "" {
		return "", fmt.Errorf("Hub URL must be a bare origin (no path, query, fragment or credentials), got %q", raw)
	}
	return "https://" + u.Host, nil
}

// Error is the JSON body of every non-2xx Hub response.
type Error struct {
	Code  string `json:"code"`
	Error string `json:"error"`
}

// Error codes clients act on.
const (
	CodeRevoked          = "revoked"           // the caller is revoked
	CodeRecipientRevoked = "recipient_revoked" // the addressed agent is revoked
	CodeSessionExpired   = "session_expired"   // the addressed session is not live
	CodeAddressTaken     = "address_taken"     // join: the address is enrolled already; the invite stays unused
)

// DirectoryEntry is the Hub's answer for one address.
type DirectoryEntry struct {
	Public  identity.Public `json:"public"`
	Revoked bool            `json:"revoked"`
}

// PingAck acknowledges a push-stream ping; Conn is the id the ping carried.
type PingAck struct {
	Conn string `json:"conn"`
}

// HeartbeatInterval is how often an idle push stream carries a ping, which
// the client acknowledges. Clients treat three missed pings as a dead
// connection; the Hub closes a stream after two unacknowledged intervals.
const HeartbeatInterval = 90 * time.Second

// Request authentication headers.
const (
	HeaderAgent = "X-Agentnet-Agent"
	HeaderTime  = "X-Agentnet-Time"
	HeaderNonce = "X-Agentnet-Nonce"
	HeaderSig   = "X-Agentnet-Sig"
)

func requestMessage(agent, method, target string, ts int64, nonce string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return fmt.Appendf(nil, "agentnet-request-v1\n%s\n%s\n%s\n%d\n%s\n%x", agent, method, target, ts, nonce, sum)
}

// SignRequest authenticates r as agent. body must be the exact request body.
func SignRequest(r *http.Request, agent string, key ed25519.PrivateKey, body []byte) {
	ts := time.Now().Unix()
	nonce := NewID()
	sig := ed25519.Sign(key, requestMessage(agent, r.Method, r.URL.RequestURI(), ts, nonce, body))
	r.Header.Set(HeaderAgent, agent)
	r.Header.Set(HeaderTime, strconv.FormatInt(ts, 10))
	r.Header.Set(HeaderNonce, nonce)
	r.Header.Set(HeaderSig, base64.StdEncoding.EncodeToString(sig))
}

// SignedRequest is the verified-shape part of an authenticated request.
type SignedRequest struct {
	Agent string
	Nonce string
	Time  time.Time
	Body  []byte
}

// ReadSignedRequest reads the bounded body and checks the signature with the
// key looked up for the claimed agent. Nonce replay is the caller's job.
func ReadSignedRequest(r *http.Request, keyFor func(agent string) (ed25519.PublicKey, error)) (SignedRequest, error) {
	var sr SignedRequest
	sr.Agent = r.Header.Get(HeaderAgent)
	sr.Nonce = r.Header.Get(HeaderNonce)
	ts, err := strconv.ParseInt(r.Header.Get(HeaderTime), 10, 64)
	if err != nil || len(sr.Nonce) != 32 {
		return sr, errors.New("missing request authentication")
	}
	sr.Time = time.Unix(ts, 0)
	if d := time.Since(sr.Time); d > ClockSkew || d < -ClockSkew {
		return sr, errors.New("request time outside allowed clock skew")
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(HeaderSig))
	if err != nil {
		return sr, errors.New("bad request signature encoding")
	}
	sr.Body, err = io.ReadAll(http.MaxBytesReader(nil, r.Body, MaxBody))
	if err != nil {
		return sr, errors.New("request body too large")
	}
	key, err := keyFor(sr.Agent)
	if err != nil {
		return sr, err
	}
	if !ed25519.Verify(key, requestMessage(sr.Agent, r.Method, r.URL.RequestURI(), ts, sr.Nonce, sr.Body), sig) {
		return sr, errors.New("request signature invalid")
	}
	return sr, nil
}

// File transfer limits and wire bodies.
const (
	// ChunkSize is the largest upload chunk; it stays under MaxBody.
	ChunkSize = 512 << 10
	// DefaultMaxFileSize is the default plaintext limit per attachment.
	DefaultMaxFileSize = 100 << 20
	// DefaultStorageQuota is the default total ciphertext the Hub holds.
	DefaultStorageQuota = 1 << 30
)

// CiphertextBound is the largest age ciphertext for plain bytes of input:
// header allowance, 16-byte nonce, and a 16-byte tag per 64 KiB chunk.
func CiphertextBound(plain int64) int64 {
	return plain + (plain/(64<<10)+1)*16 + 16 + 4096
}

// BlobReserve asks the Hub to accept an upload of Size ciphertext bytes for
// Recipient. Repeating it with identical fields is harmless.
type BlobReserve struct {
	ID        string `json:"id"`
	Recipient string `json:"recipient"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// BlobStatus reports an upload's progress.
type BlobStatus struct {
	ID       string `json:"id"`
	Size     int64  `json:"size"`
	Received int64  `json:"received"`
	State    string `json:"state"` // BlobUploading or BlobStored
}

// Blob states.
const (
	BlobUploading = "uploading"
	BlobStored    = "stored" // complete, verified, durable
)

// SessionAd announces one running daemon ("session") of an agent, and
// optionally an HTTPS endpoint where it accepts direct deliveries. It is
// signed by the agent's key, so the Hub cannot redirect direct traffic; the
// endpoint is routing information only and proves nothing about reachability.
type SessionAd struct {
	Address  string `json:"address"`
	Session  string `json:"session"`
	Endpoint string `json:"endpoint,omitempty"` // https origin
	CertPEM  string `json:"cert,omitempty"`     // the endpoint's exact TLS certificate
	Sig      []byte `json:"sig"`
}

func (s SessionAd) signed() []byte {
	s.Sig = nil
	data, _ := json.Marshal(s)
	return append([]byte("agentnet-session-v1\n"), data...)
}

// SignAd fills in the ad's signature.
func SignAd(s *SessionAd, key ed25519.PrivateKey) { s.Sig = ed25519.Sign(key, s.signed()) }

// Verify checks the ad's shape and its signature by the agent's key.
func (s SessionAd) Verify(signKey ed25519.PublicKey) error {
	if _, _, err := SplitAddress(s.Address); err != nil {
		return err
	}
	if !ValidID(s.Session) {
		return errors.New("invalid session id")
	}
	if (s.Endpoint == "") != (s.CertPEM == "") {
		return errors.New("endpoint and certificate go together")
	}
	if s.Endpoint != "" {
		if origin, err := NormalizeHubURL(s.Endpoint); err != nil || origin != s.Endpoint {
			return errors.New("endpoint must be a bare https origin")
		}
	}
	if !ed25519.Verify(signKey, s.signed(), s.Sig) {
		return errors.New("session ad signature invalid")
	}
	return nil
}

// Encode renders the ad for a URL query parameter.
func (s SessionAd) Encode() string {
	data, _ := json.Marshal(s)
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeAd parses SessionAd.Encode output.
func DecodeAd(raw string) (SessionAd, error) {
	var s SessionAd
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err == nil {
		err = json.Unmarshal(data, &s)
	}
	return s, err
}

// SessionInfo is a live (or briefly reconnecting) session in the directory.
type SessionInfo struct {
	Ad        SessionAd `json:"ad"`
	Connected bool      `json:"connected"` // false during the reconnect grace period
}

// Member presence, as the Hub sees the agent's daemons: at least one
// connected; none connected but one within its reconnect grace; none.
const (
	PresenceConnected    = "connected"
	PresenceReconnecting = "reconnecting"
	PresenceOffline      = "offline"
)

// Member is one enrolled, unrevoked agent in the Hub's member list. The
// label part of the address is the person label an admin chose for the
// invite, not a verified identity, and being listed grants nothing: keys
// still come from the per-address directory and are trusted as before.
type Member struct {
	Address  string `json:"address"`
	Presence string `json:"presence"` // PresenceConnected, PresenceReconnecting or PresenceOffline
	Joined   int64  `json:"joined"`   // enrollment time, unix seconds
	// Person names the person roster this device speaks for, if any: the
	// newest step the Hub holds. A receiver fetches and verifies the chain
	// (GET /v1/persons/{id}/chain) only when it is newer than its own pin.
	Person *PersonRef `json:"person,omitempty"`
	// Agent is the relay's reading of the device's newest signed capability
	// record: it lists CapAgent, so the device says its program runs an
	// agent. A hint for display and offers only: it grants nothing and no
	// send decision may use it.
	Agent bool `json:"agent,omitempty"`
	// Version is the AgentNet version the device's push stream last
	// reported (VersionHeader, unsigned): for display only. Suspended says
	// the Hub refuses that version until the device updates
	// (UpdateRequired): its copies wait in custody, so senders need not
	// wait for it. Availability only: neither grants or withdraws anything.
	Version   string `json:"version,omitempty"`
	Suspended bool   `json:"suspended,omitempty"`
}

// PersonRef names one step of a person's roster chain.
type PersonRef struct {
	ID   string `json:"id"`
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// Members is the Hub's member list (GET /v1/agents, and the "members" push
// event), most recently joined first. Truncated means more agents are
// enrolled than MaxMembers: the list is then not complete.
type Members struct {
	Members   []Member `json:"members"`
	Truncated bool     `json:"truncated"`
	// Workspace is the name the Hub's admin gave the workspace, if any (a
	// label, never identity: the realm id is that). It is not checked by
	// Valid: a receiver ignores a name ValidWorkspaceName refuses and keeps
	// the list.
	Workspace string `json:"workspace,omitempty"`
}

// MaxMembers bounds one member list, keeping it below MaxBody (a member
// entry is at most a few hundred bytes: person records are not in it).
const MaxMembers = 1000

// MembersHeader, set to "1" on a push stream, says the Hub sends the member
// list on that stream (first on connect, then on every change). Older Hubs
// neither send it nor list members.
const MembersHeader = "Agentnet-Members"

// Valid reports whether m is a member list a Hub may send: bounded, with
// well-formed unique addresses and known presence values.
func (m Members) Valid() error {
	if len(m.Members) > MaxMembers {
		return fmt.Errorf("%d members exceed the limit of %d", len(m.Members), MaxMembers)
	}
	seen := make(map[string]bool, len(m.Members))
	for _, e := range m.Members {
		if _, _, err := SplitAddress(e.Address); err != nil {
			return err
		}
		if seen[e.Address] {
			return fmt.Errorf("%s listed twice", e.Address)
		}
		seen[e.Address] = true
		switch e.Presence {
		case PresenceConnected, PresenceReconnecting, PresenceOffline:
		default:
			return fmt.Errorf("%s: unknown presence %q", e.Address, e.Presence)
		}
		if p := e.Person; p != nil && (!ValidID(p.ID) || p.Seq < 0 || !ValidHash(p.Hash)) {
			return fmt.Errorf("%s: malformed person reference", e.Address)
		}
	}
	return nil
}

// ValidID reports whether s is a 128-bit lowercase hex identifier.
func ValidID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// SplitTarget splits "person/agent" or "person/agent#session".
func SplitTarget(target string) (address, session string, err error) {
	address, session, _ = strings.Cut(target, "#")
	if _, _, err := SplitAddress(address); err != nil {
		return "", "", err
	}
	if session != "" && !ValidID(session) {
		return "", "", fmt.Errorf("invalid session id in %q", target)
	}
	return address, session, nil
}
