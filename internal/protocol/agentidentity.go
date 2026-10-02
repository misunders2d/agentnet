package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/identity"
)

const CapAgentIdentity = "agi1"
const AgentDomain = "agentnet-agent-v1\n"
const MaxAgentCatalog = 32

// AgentRecord identifies an agent, not its program or permissions. The exact
// host device key signs the random ID and display label; names confer no trust.
type AgentRecord struct {
	V       int    `json:"v"`
	ID      string `json:"id"`
	Host    string `json:"host"`
	HostKey string `json:"host_key"`
	Label   string `json:"label"`
	TS      int64  `json:"ts"`
	Sig     []byte `json:"sig,omitempty"`
}

func ValidAgentID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
func (r AgentRecord) Canonical() []byte {
	r.Sig = nil
	raw, _ := json.Marshal(r)
	return append([]byte(AgentDomain), raw...)
}
func (r AgentRecord) Hash() string                 { return hashHex(r.Canonical()) }
func (r *AgentRecord) Sign(key ed25519.PrivateKey) { r.Sig = ed25519.Sign(key, r.Canonical()) }
func (r AgentRecord) Validate() error {
	if r.V != 1 || !ValidAgentID(r.ID) || !ValidFingerprint(r.HostKey) || r.TS <= 0 {
		return errors.New("agent: invalid identity, host key or timestamp")
	}
	if _, _, err := SplitAddress(r.Host); err != nil {
		return errors.New("agent: invalid host")
	}
	if err := validLabel(r.Label); err != nil {
		return err
	}
	return nil
}
func (r AgentRecord) Verify(host identity.Public) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if host.Verify() != nil || host.Address != r.Host || host.Fingerprint() != r.HostKey || !ed25519.Verify(host.SignKey, r.Canonical(), r.Sig) {
		return errors.New("agent: identity is not signed by its exact host device")
	}
	return nil
}
func ParseAgentRecord(raw []byte) (AgentRecord, error) {
	var r AgentRecord
	if len(raw) > 8192 {
		return r, errors.New("agent: record too large")
	}
	if err := decodeStrictJSON(raw, &r); err != nil {
		return r, err
	}
	return r, r.Validate()
}
