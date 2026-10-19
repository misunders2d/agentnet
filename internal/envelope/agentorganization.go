package envelope

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const OriginAgentOrganization = "agent:organization"
const TopicRequestDomain = "agentnet-topic-request-v1\n"

// TopicRequest binds the organization to a genuine admitted human request.
// Proof is only the native original author's compact local fallback, never
// another host's assertion about a foreign requester. No body/files are carried.
type TopicRequest struct {
	LID    string             `json:"lid"`
	Author string             `json:"author"`
	Hash   string             `json:"hash"`
	Proof  *TopicRequestProof `json:"proof,omitempty"`
}

type TopicRequestProof struct {
	V      int                  `json:"v"`
	Conv   string               `json:"conv"`
	LID    string               `json:"lid"`
	PID    string               `json:"pid"`
	Author protocol.EventAuthor `json:"author"`
	Target Target               `json:"target"`
	Hash   string               `json:"hash"`
	Kind   string               `json:"kind"`
	Topic  string               `json:"topic,omitempty"`
	Sig    []byte               `json:"sig,omitempty"`
}

func (p TopicRequestProof) Canonical() []byte {
	p.Sig = nil
	raw, _ := json.Marshal(p)
	return append([]byte(TopicRequestDomain), raw...)
}
func (p *TopicRequestProof) Sign(key ed25519.PrivateKey) { p.Sig = ed25519.Sign(key, p.Canonical()) }
func (p TopicRequestProof) Validate() error {
	if p.V != 1 || !protocol.ValidHash(p.Conv) || !protocol.ValidID(p.LID) || !protocol.ValidID(p.PID) || !protocol.ValidID(p.Author.Person) || !protocol.ValidHash(p.Author.Roster) || !protocol.ValidFingerprint(p.Author.Fingerprint) || p.Author.GroupAdmission != "" && !protocol.ValidHash(p.Author.GroupAdmission) || !protocol.ValidFingerprint(p.Target.Fingerprint) || !protocol.ValidHash(p.Hash) || p.Target.AgentID != "" && !protocol.ValidID(p.Target.AgentID) || p.Target.GroupAdmission != "" && !protocol.ValidHash(p.Target.GroupAdmission) || p.Topic != "" && !protocol.ValidID(p.Topic) || p.Kind != KindQuestion && p.Kind != KindTask || len(p.Sig) != ed25519.SignatureSize {
		return errors.New("invalid compact local topic request proof")
	}
	if _, _, err := protocol.SplitAddress(p.Author.Address); err != nil {
		return err
	}
	if _, _, err := protocol.SplitAddress(p.Target.Address); err != nil {
		return err
	}
	// Only a same-device native original can use this fallback. A foreign
	// author's actual original signature can never be substituted by its host.
	if p.Author.Address != p.Target.Address || p.Author.Fingerprint != p.Target.Fingerprint {
		return errors.New("compact topic proof is only for its native original author")
	}
	return nil
}
func (p TopicRequestProof) Verify(key identity.Public) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if key.Address != p.Author.Address || key.Fingerprint() != p.Author.Fingerprint || !ed25519.Verify(key.SignKey, p.Canonical(), p.Sig) {
		return errors.New("compact topic request author signature differs")
	}
	return nil
}

func AgentTopicOrganization(in Inner) bool {
	return TopicOrganization(in.TopicEvent) && in.TopicEvent.Request != nil && in.TopicEvent.Action == "move" && in.PID != "" && in.Origin == OriginAgentOrganization
}

// Main admits presentation moves and the conversation-wide links register.
// Neither changes participation scope or execution authority.
func MainTopicOrganization(in Inner) bool {
	return in.Topic == "" && (TopicOrganization(in.TopicEvent) && in.TopicEvent.Action == "move" || TopicResources(in.TopicEvent))
}
func OrganizationReaderExtension(in Inner) bool {
	return AgentTopicOrganization(in) || MainTopicOrganization(in) && TopicOrganization(in.TopicEvent)
}
func (r TopicRequest) Validate(in Inner) error {
	if !protocol.ValidID(r.LID) || !protocol.ValidFingerprint(r.Author) || !protocol.ValidHash(r.Hash) {
		return errors.New("invalid organization request binding")
	}
	if r.Proof != nil {
		p := r.Proof
		if err := p.Validate(); err != nil {
			return err
		}
		if p.Conv != in.Conv || p.PID != in.PID || p.LID != r.LID || p.Author.Fingerprint != r.Author || p.Hash != r.Hash || p.Target.AgentID != in.AgentID {
			return errors.New("compact proof differs from exact organization binding")
		}
	}
	return nil
}
