package envelope

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// These wire bounds retain the existing local context/follow-up bounds.
const (
	MaxReceiverSetup        = 64 << 10
	MaxReceiverInstructions = 4 << 10
	MaxReceiverSessions     = 32
	ReceiverDigestDomain    = "agentnet-receiver-delegation-v1\n"
)

type ReceiverRoute struct {
	Op            string `json:"op"`
	Host          string `json:"host"`
	HostKey       string `json:"host_key"`
	RequestRef    string `json:"request_ref,omitempty"`
	RequestDigest string `json:"request_digest,omitempty"`
	DelegationID  string `json:"delegation_id,omitempty"`
}
type ReceiverOperation struct {
	V        int               `json:"v"`
	Request  *ReceiverRequest  `json:"request,omitempty"`
	Receiver *ReceiverChoice   `json:"receiver,omitempty"`
	Sessions []ReceiverSession `json:"sessions,omitempty"`
	Detail   string            `json:"detail,omitempty"`
}
type ReceiverChoice struct {
	Kind          string           `json:"kind"`
	AgentID       string           `json:"agent_id,omitempty"`
	SessionHandle string           `json:"session_handle,omitempty"`
	Instructions  string           `json:"instructions,omitempty"`
	Mode          string           `json:"mode,omitempty"`
	OnClose       *ReceiverOnClose `json:"on_close,omitempty"`
}
type ReceiverOnClose struct {
	AgentID      string `json:"agent_id"`
	Instructions string `json:"instructions"`
	Mode         string `json:"mode"`
}
type ReceiverSession struct {
	Handle  string `json:"handle"`
	Harness string `json:"harness"`
	Label   string `json:"label"`
	Active  bool   `json:"active"`
}
type ReceiverRequest struct {
	ID             string             `json:"id"`
	LID            string             `json:"lid,omitempty"`
	From           string             `json:"from"`
	FromKey        string             `json:"from_key"`
	To             string             `json:"to,omitempty"`
	ToKey          string             `json:"to_key,omitempty"`
	TS             int64              `json:"ts"`
	Conv           string             `json:"conv,omitempty"`
	Root           json.RawMessage    `json:"root,omitempty"`
	Kind           string             `json:"kind"`
	Body           string             `json:"body"`
	ReplyTo        string             `json:"reply_to,omitempty"`
	Origin         string             `json:"origin,omitempty"`
	Emotion        string             `json:"emotion,omitempty"`
	Target         *Target            `json:"target,omitempty"`
	PID            string             `json:"pid,omitempty"`
	Attachments    []Attachment       `json:"attachments,omitempty"`
	GroupAdmission string             `json:"group_admission,omitempty"`
	GroupReplies   []ReceiverReplyKey `json:"group_replies,omitempty"`
}
type ReceiverReplyKey struct {
	Key       string `json:"key"`
	Admission string `json:"admission,omitempty"`
}

func (r ReceiverRoute) Validate() error {
	if _, _, e := protocol.SplitAddress(r.Host); e != nil || !protocol.ValidFingerprint(r.HostKey) {
		return errors.New("receiver: exact host address and key required")
	}
	switch r.Op {
	case "catalog":
		if r.RequestRef != "" || r.RequestDigest != "" || r.DelegationID != "" {
			return errors.New("receiver: catalog has no request commitment")
		}
	case "delegate", "ready", "request":
		if !protocol.ValidID(r.RequestRef) || !protocol.ValidID(r.DelegationID) || !protocol.ValidHash(r.RequestDigest) {
			return errors.New("receiver: invalid request commitment")
		}
	default:
		return errors.New("receiver: unknown operation")
	}
	return nil
}
func receiverHandle(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func receiverInstructions(s, mode string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= MaxReceiverInstructions && utf8.ValidString(s) && (mode == KindQuestion || mode == KindTask)
}
func (r ReceiverChoice) Validate() error {
	switch r.Kind {
	case "human":
		if r.AgentID != "" || r.SessionHandle != "" || r.Instructions != "" || r.Mode != "" || r.OnClose != nil {
			return errors.New("receiver: human choice has no executor")
		}
	case "managed_agent":
		if !protocol.ValidID(r.AgentID) || r.SessionHandle != "" || !receiverInstructions(r.Instructions, r.Mode) || r.OnClose != nil {
			return errors.New("receiver: managed choice requires exact agent, instructions and mode")
		}
	case "live_session":
		if !receiverHandle(r.SessionHandle) || r.AgentID != "" || r.Instructions != "" || r.Mode != "" {
			return errors.New("receiver: live choice requires only an opaque handle")
		}
		if b := r.OnClose; b != nil && (!protocol.ValidID(b.AgentID) || !receiverInstructions(b.Instructions, b.Mode)) {
			return errors.New("receiver: invalid explicit closed-session backup")
		}
	default:
		return errors.New("receiver: unknown choice")
	}
	return nil
}

// Validate checks only wire shape. Current signatures, own-person association,
// membership epochs, local approval and installed executor remain caller authority.
func (r ReceiverRequest) Validate() error {
	if !protocol.ValidID(r.ID) || !protocol.ValidFingerprint(r.FromKey) || r.TS <= 0 || !utf8.ValidString(r.Body) || r.Kind != KindMessage && r.Kind != KindQuestion && r.Kind != KindTask {
		return errors.New("receiver: invalid original request")
	}
	if _, _, e := protocol.SplitAddress(r.From); e != nil {
		return e
	}
	if r.ReplyTo != "" && !protocol.ValidID(r.ReplyTo) {
		return errors.New("receiver: invalid original reply reference")
	}
	in := Inner{V: Version, ID: r.ID, From: r.From, To: r.From, TS: r.TS, Kind: r.Kind, Body: r.Body, ReplyTo: r.ReplyTo, Conv: r.Conv, LID: r.LID, Root: r.Root, Origin: r.Origin, Emotion: r.Emotion, Target: r.Target, PID: r.PID}
	group := false
	if r.Conv != "" {
		if r.To != "" || r.ToKey != "" {
			return errors.New("receiver: conversation recipient derives from verified root and target")
		}
		in.V = Version2
		root, e := protocol.ParseConvRoot(r.Root)
		if e != nil {
			return e
		}
		normalized, e := json.Marshal(root)
		if e != nil || !bytes.Equal(normalized, r.Root) || root.ID() != r.Conv {
			return errors.New("receiver: original root must use exact typed signed encoding")
		}
		group = root.Kind == protocol.ConvKindGroup
		if r.Target != nil && r.ID != r.LID {
			return errors.New("receiver: targeted conversation copy must use its committed logical ID")
		}
	} else {
		if _, _, e := protocol.SplitAddress(r.To); e != nil || !protocol.ValidFingerprint(r.ToKey) {
			return errors.New("receiver: direct original requires exact recipient address and key")
		}
		in.To = r.To
		if r.Target != nil && (r.Target.Address != r.To || r.Target.Fingerprint != r.ToKey) {
			return errors.New("receiver: direct original target differs from committed recipient")
		}
	}
	if e := checkVersion2(in); e != nil {
		return e
	}
	if in.V == Version2 && AgentOrigin(r.Origin) && r.Emotion == "" {
		return errors.New("receiver: agent request requires emotion")
	}
	if len(r.Attachments) > MaxAttachments {
		return errors.New("receiver: too many original files")
	}
	for _, a := range r.Attachments {
		if a.Blob != (Blob{}) || a.Name == "" || !utf8.ValidString(a.Name) || a.Size < 0 || !protocol.ValidHash(a.SHA256) {
			return errors.New("receiver: original files require plaintext-only manifests")
		}
	}
	if !group {
		if r.GroupAdmission != "" || len(r.GroupReplies) != 0 {
			return errors.New("receiver: group authority on non-group original")
		}
	} else {
		if !protocol.ValidHash(r.GroupAdmission) || len(r.GroupReplies) == 0 || len(r.GroupReplies) > protocol.MaxGroupMembers*protocol.MaxPersonDevices {
			return errors.New("receiver: group original requires bounded admission and reply keys")
		}
		previous := ""
		for _, k := range r.GroupReplies {
			if !protocol.ValidFingerprint(k.Key) || k.Key <= previous || k.Admission != "" && !protocol.ValidHash(k.Admission) {
				return errors.New("receiver: group reply keys must be sorted, unique and exact")
			}
			if k.Admission == "" && (r.PID == "" || r.Target == nil || k.Key != r.Target.Fingerprint) {
				return errors.New("receiver: only an exact PID target may carry a visitor reply key")
			}
			previous = k.Key
		}
	}
	b, e := json.Marshal(r)
	if e != nil || len(b) > MaxReceiverSetup {
		return errors.New("receiver: original snapshot too large")
	}
	return nil
}

// ReceiverDigest commits the exact selected host, frozen request and receiver.
// Only Op and the digest itself are normalized; no other route field is rebuilt.
func ReceiverDigest(route ReceiverRoute, request ReceiverRequest, receiver ReceiverChoice) (string, error) {
	if route.Op != "request" && route.Op != "delegate" && route.Op != "ready" || route.RequestDigest != "" && !protocol.ValidHash(route.RequestDigest) {
		return "", errors.New("receiver: invalid commitment operation or digest")
	}
	route.Op = "request"
	route.RequestDigest = strings.Repeat("0", 64)
	if e := route.Validate(); e != nil {
		return "", e
	}
	if e := request.Validate(); e != nil {
		return "", e
	}
	if e := receiver.Validate(); e != nil {
		return "", e
	}
	ref := request.ID
	if request.Conv != "" {
		ref = request.LID
	}
	if route.RequestRef != ref {
		return "", errors.New("receiver: route reference does not match original")
	}
	route.RequestDigest = ""
	b, e := json.Marshal(struct {
		Route    ReceiverRoute   `json:"route"`
		Request  ReceiverRequest `json:"request"`
		Receiver ReceiverChoice  `json:"receiver"`
	}{route, request, receiver})
	if e != nil {
		return "", e
	}
	if len(b) > MaxReceiverSetup {
		return "", errors.New("receiver: delegation commitment too large")
	}
	sum := sha256.Sum256(append([]byte(ReceiverDigestDomain), b...))
	return hex.EncodeToString(sum[:]), nil
}

// ParseReceiverOperation is strict setup parsing. Original request bodies are
// never passed here. Delegate attachments retain existing encrypted manifests.
func ParseReceiverOperation(body []byte, route ReceiverRoute, replyTo string, attachments []Attachment) (ReceiverOperation, error) {
	var o ReceiverOperation
	if len(body) > MaxReceiverSetup {
		return o, errors.New("receiver: setup body too large")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if e := d.Decode(&o); e != nil {
		return o, e
	}
	if d.Decode(new(any)) != io.EOF {
		return o, errors.New("receiver: trailing setup JSON")
	}
	if e := route.Validate(); e != nil {
		return o, e
	}
	if o.V != 1 {
		return o, errors.New("receiver: unsupported setup version")
	}
	switch route.Op {
	case "catalog":
		if o.Request != nil || o.Receiver != nil || o.Detail != "" || len(attachments) != 0 || len(o.Sessions) > MaxReceiverSessions || replyTo == "" && o.Sessions != nil || replyTo != "" && !protocol.ValidID(replyTo) {
			return o, errors.New("receiver: invalid catalog fields")
		}
		previous := ""
		for _, s := range o.Sessions {
			if !receiverHandle(s.Handle) || s.Handle <= previous || len(s.Label) > 160 || !utf8.ValidString(s.Label) || (s.Harness != "pi" && s.Harness != "omp" && s.Harness != "codex" && s.Harness != "claude") {
				return o, errors.New("receiver: invalid catalog session")
			}
			previous = s.Handle
		}
	case "delegate":
		if o.Request == nil || o.Receiver == nil || o.Sessions != nil || o.Detail != "" || replyTo != "" {
			return o, errors.New("receiver: invalid delegate fields")
		}
		digest, e := ReceiverDigest(route, *o.Request, *o.Receiver)
		if e != nil {
			return o, e
		}
		if digest != route.RequestDigest {
			return o, errors.New("receiver: delegation commitment mismatch")
		}
		if len(attachments) != len(o.Request.Attachments) {
			return o, errors.New("receiver: delegated file count mismatch")
		}
		for i, a := range attachments {
			p := o.Request.Attachments[i]
			if a.Name != p.Name || a.Size != p.Size || a.SHA256 != p.SHA256 || !protocol.ValidID(a.Blob.ID) || a.Blob.Size <= 0 || !protocol.ValidHash(a.Blob.SHA256) {
				return o, errors.New("receiver: delegated encrypted file manifest mismatch")
			}
		}
	case "ready":
		if o.Request != nil || o.Receiver != nil || o.Sessions != nil || len(attachments) != 0 || replyTo != route.DelegationID || len(o.Detail) > MaxDetailBytes || !utf8.ValidString(o.Detail) {
			return o, errors.New("receiver: invalid ready fields")
		}
	default:
		return o, errors.New("receiver: original request body is not setup JSON")
	}
	return o, nil
}

// ValidateReceiverRoute is a shape guard, not consent or membership admission.
func ValidateReceiverRoute(in Inner) error {
	r := in.ReceiverRoute
	if r == nil {
		return nil
	}
	if e := r.Validate(); e != nil {
		return e
	}
	if in.AgentID != "" || in.Status != "" || in.Ref != nil || in.Session != "" || in.Fallback || in.Sub != "" || in.V != Version && in.V != Version2 {
		return errors.New("receiver: route only belongs on setup or original requests")
	}
	if r.Op == "request" {
		if in.Kind != KindMessage && in.Kind != KindQuestion && in.Kind != KindTask {
			return errors.New("receiver: answers cannot redirect replies")
		}
		ref := in.ID
		if in.V == Version2 {
			ref = in.LID
		}
		if r.RequestRef != ref {
			return errors.New("receiver: original route reference mismatch")
		}
		if in.V == Version2 && in.Target != nil && in.Target.Address == in.To && !in.Replica && in.ID != in.LID {
			return errors.New("receiver: executable conversation copy must use its committed logical ID")
		}
		return nil // Current-own replica provenance is checked by history admission.
	}
	if in.V != Version || in.Target != nil || in.PID != "" || in.Replica || in.Fan != nil || in.Origin != "" || in.Emotion != "" {
		return errors.New("receiver: setup requires a dedicated direct envelope")
	}
	kind := KindMessage
	if r.Op == "delegate" {
		kind = KindTask
	}
	if in.Kind != kind {
		return errors.New("receiver: invalid setup kind")
	}
	if !protocol.ValidID(in.ID) || in.TS <= 0 {
		return errors.New("receiver: invalid setup identity")
	}
	if r.Op == "delegate" && in.ID != r.DelegationID {
		return errors.New("receiver: delegation envelope ID differs from commitment")
	}
	if r.Op == "delegate" || r.Op == "catalog" && in.ReplyTo == "" {
		if r.Host != in.To {
			return errors.New("receiver: setup must address its committed host")
		}
	} else if r.Host != in.From {
		return errors.New("receiver: response must originate at its committed host")
	}
	o, e := ParseReceiverOperation([]byte(in.Body), *r, in.ReplyTo, in.Attachments)
	if e != nil {
		return e
	}
	if o.Request != nil && o.Request.From != in.From {
		return errors.New("receiver: delegate must originate at original author")
	}
	return nil
}
