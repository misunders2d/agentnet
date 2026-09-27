// Package identity owns an agent's key material and the public binding the Hub
// directory publishes for it.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"filippo.io/age"

	"github.com/misunders2d/agentnet/internal/secfile"
)

// Identity is an agent's private keys: Ed25519 signs, age X25519 decrypts.
type Identity struct {
	Sign ed25519.PrivateKey
	Box  *age.X25519Identity
}

// Public is the directory entry for an agent. BoxSig binds the encryption
// recipient to the address under the signing key.
type Public struct {
	Address      string `json:"address"`
	SignKey      []byte `json:"sign_key"`
	BoxRecipient string `json:"box_recipient"`
	BoxSig       []byte `json:"box_sig"`
}

type keyFile struct {
	V        int    `json:"v"`
	SignSeed []byte `json:"sign_seed"`
	Box      string `json:"box"`
}

// Generate creates fresh keys.
func Generate() (*Identity, error) {
	_, sign, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	box, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	return &Identity{Sign: sign, Box: box}, nil
}

// Save writes the private keys to an owner-only file.
func (id *Identity) Save(path string) error {
	data, err := json.Marshal(keyFile{V: 1, SignSeed: id.Sign.Seed(), Box: id.Box.String()})
	if err != nil {
		return err
	}
	return secfile.Write(path, data)
}

// Load reads private keys from an owner-only file.
func Load(path string) (*Identity, error) {
	data, err := secfile.Read(path)
	if err != nil {
		return nil, err
	}
	var kf keyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, err
	}
	if kf.V != 1 || len(kf.SignSeed) != ed25519.SeedSize {
		return nil, errors.New("unsupported identity file")
	}
	box, err := age.ParseX25519Identity(kf.Box)
	if err != nil {
		return nil, err
	}
	return &Identity{Sign: ed25519.NewKeyFromSeed(kf.SignSeed), Box: box}, nil
}

// Public returns the signed directory entry for address.
func (id *Identity) Public(address string) Public {
	recipient := id.Box.Recipient().String()
	return Public{
		Address:      address,
		SignKey:      id.Sign.Public().(ed25519.PublicKey),
		BoxRecipient: recipient,
		BoxSig:       ed25519.Sign(id.Sign, bindingMessage(address, recipient)),
	}
}

// Verify checks that the entry is well formed and self-consistently signed.
// It says nothing about whether the key is the one previously trusted.
func (p Public) Verify() error {
	if len(p.SignKey) != ed25519.PublicKeySize {
		return errors.New("bad signing key")
	}
	if _, err := age.ParseX25519Recipient(p.BoxRecipient); err != nil {
		return fmt.Errorf("bad encryption key: %w", err)
	}
	if !ed25519.Verify(p.SignKey, bindingMessage(p.Address, p.BoxRecipient), p.BoxSig) {
		return errors.New("encryption key not signed by signing key")
	}
	return nil
}

// Recipient parses the entry's age recipient.
func (p Public) Recipient() (*age.X25519Recipient, error) {
	return age.ParseX25519Recipient(p.BoxRecipient)
}

// Fingerprint is a short human-comparable digest of both public keys.
func (p Public) Fingerprint() string {
	h := sha256.New()
	h.Write(p.SignKey)
	h.Write([]byte(p.BoxRecipient))
	sum := hex.EncodeToString(h.Sum(nil)[:16])
	return fmt.Sprintf("%s-%s-%s-%s", sum[0:8], sum[8:16], sum[16:24], sum[24:32])
}

func bindingMessage(address, recipient string) []byte {
	return []byte("agentnet-box-binding-v1\n" + address + "\n" + recipient)
}
