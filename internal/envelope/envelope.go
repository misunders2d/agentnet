// Package envelope seals end-to-end encrypted, sender-signed messages.
//
// The outer Envelope is visible to the Hub and signed by the sender. The
// encrypted Inner repeats id, from and to so a member who strips the outer
// signature and re-signs someone else's ciphertext is detected by the
// recipient.
package envelope

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"filippo.io/age"

	"github.com/misunders2d/agentnet/internal/identity"
)

// Version is the envelope format version.
const Version = 1

// MaxCiphertext bounds a message body; files travel separately.
const MaxCiphertext = 256 << 10

// Envelope is the Hub-visible, sender-signed message wrapper.
type Envelope struct {
	V    int    `json:"v"`
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	CT   []byte `json:"ct"`
	Sig  []byte `json:"sig,omitempty"`
}

// Inner is the encrypted content.
type Inner struct {
	V       int    `json:"v"`
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	TS      int64  `json:"ts"`
	Kind    string `json:"kind"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// Kinds of messages.
const KindMessage = "message"

// ErrNotForMe means the envelope is addressed to another agent.
var ErrNotForMe = errors.New("envelope addressed to another agent")

func (e Envelope) signed() []byte {
	e.Sig = nil
	data, _ := json.Marshal(e)
	return append([]byte("agentnet-envelope-v1\n"), data...)
}

// Seal encrypts in to recipient and signs the envelope with the sender key.
func Seal(in Inner, sender ed25519.PrivateKey, recipient age.Recipient) (Envelope, error) {
	in.V = Version
	plain, err := json.Marshal(in)
	if err != nil {
		return Envelope{}, err
	}
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, recipient)
	if err != nil {
		return Envelope{}, err
	}
	if _, err := w.Write(plain); err != nil {
		return Envelope{}, err
	}
	if err := w.Close(); err != nil {
		return Envelope{}, err
	}
	if ct.Len() > MaxCiphertext {
		return Envelope{}, fmt.Errorf("message too large (%d bytes encrypted, max %d)", ct.Len(), MaxCiphertext)
	}
	env := Envelope{V: Version, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Kind: in.Kind, CT: ct.Bytes()}
	env.Sig = ed25519.Sign(sender, env.signed())
	return env, nil
}

// VerifySig checks the outer signature and shape; the Hub calls this.
func (e Envelope) VerifySig(senderKey ed25519.PublicKey) error {
	if e.V != Version {
		return fmt.Errorf("unsupported envelope version %d", e.V)
	}
	if e.ID == "" || e.From == "" || e.To == "" || e.Kind == "" || len(e.CT) == 0 {
		return errors.New("incomplete envelope")
	}
	if len(e.CT) > MaxCiphertext {
		return errors.New("envelope too large")
	}
	if !ed25519.Verify(senderKey, e.signed(), e.Sig) {
		return errors.New("envelope signature invalid")
	}
	return nil
}

// Open verifies the envelope against the trusted sender entry, decrypts it
// with self, and checks the encrypted fields match the signed outer ones.
func Open(e Envelope, self *identity.Identity, selfAddress string, sender identity.Public) (Inner, error) {
	var in Inner
	if e.To != selfAddress {
		return in, ErrNotForMe
	}
	if e.From != sender.Address {
		return in, errors.New("sender key does not belong to envelope sender")
	}
	if err := e.VerifySig(sender.SignKey); err != nil {
		return in, err
	}
	r, err := age.Decrypt(bytes.NewReader(e.CT), self.Box)
	if err != nil {
		return in, fmt.Errorf("decrypt: %w", err)
	}
	plain, err := io.ReadAll(io.LimitReader(r, MaxCiphertext))
	if err != nil {
		return in, fmt.Errorf("decrypt: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("inner: %w", err)
	}
	if in.V != e.V || in.ID != e.ID || in.From != e.From || in.To != e.To || in.TS != e.TS || in.Kind != e.Kind {
		return in, errors.New("encrypted header does not match signed envelope")
	}
	return in, nil
}
