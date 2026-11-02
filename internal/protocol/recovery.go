package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
)

const SuccessorDomain = "agentnet-person-successor-v1\n"

// PersonSuccessor records new-human-key consent. Only an authenticated Hub
// committed record establishes messaging continuity; this signature alone does not.
// It never grants execution permission or changes a signed roster or DM root.
type PersonSuccessor struct {
	Version    int    `json:"version"`
	Realm      string `json:"realm"`
	OldPerson  string `json:"old_person"`
	OldHead    string `json:"old_head"`
	NewPerson  string `json:"new_person"`
	NewGenesis string `json:"new_genesis"`
	Email      string `json:"email"`
	Created    int64  `json:"created"`
	Nonce      string `json:"nonce"`
	Signer     string `json:"signer"`
	Sig        []byte `json:"sig,omitempty"`
}

func (s PersonSuccessor) Canonical() []byte {
	s.Sig = nil
	b, _ := json.Marshal(s)
	return append([]byte(SuccessorDomain), b...)
}
func (s PersonSuccessor) Hash() string               { return hashHex(s.Canonical()) }
func (s *PersonSuccessor) Sign(k ed25519.PrivateKey) { s.Sig = ed25519.Sign(k, s.Canonical()) }

// Verify checks consent against an already verified new-person roster chain.
// Callers verify the chain before supplying current. For committed proofs,
// current may be the historical consent-time roster after a signer rotates out;
// current recipient revocation checks remain a separate send-time requirement.
func (s PersonSuccessor) Verify(realm string, genesis, current PersonRoster) error {
	email, e := NormalizeEmail(s.Email)
	if s.Version != 1 || realm == "" || s.Realm != realm || !ValidID(s.OldPerson) || !ValidID(s.NewPerson) || s.OldPerson == s.NewPerson || !ValidHash(s.OldHead) || !ValidHash(s.NewGenesis) || !ValidID(s.Nonce) || s.Created <= 0 || e != nil || email != s.Email {
		return errors.New("invalid successor record")
	}
	if genesis.VerifyFirst() != nil || genesis.Person != s.NewPerson || genesis.Hash() != s.NewGenesis || genesis.Email != email || current.Person != s.NewPerson || current.Email != email || !current.Human(s.Signer) {
		return errors.New("invalid successor person")
	}
	d, ok := current.Device(s.Signer)
	if !ok || !ed25519.Verify(d.SignKey, s.Canonical(), s.Sig) {
		return errors.New("invalid successor consent")
	}
	return nil
}

type RecoveryRequest struct {
	Google    GoogleRequest   `json:"google"`
	Successor PersonSuccessor `json:"successor"`
}
type RecoveryRecord struct {
	ID          string          `json:"id"`
	Successor   PersonSuccessor `json:"successor"`
	Status      string          `json:"status"`
	Actor       string          `json:"actor,omitempty"`
	ActorKey    string          `json:"actor_key,omitempty"`
	ActorPerson string          `json:"actor_person,omitempty"`
	Decided     int64           `json:"decided,omitempty"`
}
