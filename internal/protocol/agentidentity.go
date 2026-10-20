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
const AgentHarnessDomain = "agentnet-agent-harness-v1\n"
const FeatureAgentHarness = "agent-harness1"
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
	// This optional extension is separately signed; the v1 identity is unchanged.
	Harness    string `json:"harness,omitempty"`
	HarnessSig []byte `json:"harness_sig,omitempty"`
}

func ValidAgentID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id
}
func (r AgentRecord) Canonical() []byte {
	r.Sig = nil
	r.Harness, r.HarnessSig = "", nil
	raw, _ := json.Marshal(r)
	return append([]byte(AgentDomain), raw...)
}
func (r AgentRecord) Hash() string { return hashHex(r.Canonical()) }
func (r *AgentRecord) Sign(key ed25519.PrivateKey) {
	r.Sig = ed25519.Sign(key, r.Canonical())
	r.HarnessSig = nil
	if r.Harness != "" {
		r.HarnessSig = ed25519.Sign(key, r.HarnessCanonical())
	}
}
func (r AgentRecord) HarnessCanonical() []byte {
	raw, _ := json.Marshal(struct {
		Identity string `json:"identity"`
		Harness  string `json:"harness"`
	}{r.Hash(), r.Harness})
	return append([]byte(AgentHarnessDomain), raw...)
}
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
	if r.Harness != "" && !ValidName(r.Harness) || (r.Harness == "") != (len(r.HarnessSig) == 0) {
		return errors.New("agent: invalid harness extension")
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
	if r.Harness != "" && !ed25519.Verify(host.SignKey, r.HarnessCanonical(), r.HarnessSig) {
		return errors.New("agent: harness is not signed by its exact host device")
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
