package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/identity"
)

// Live signals have no custody or history. Only routing and signed time
// are visible to the relay; scope and composer state are inside age.
const (
	FeatureSignals      = "signals1"
	CapTyping           = "typing1"
	SignalsHeader       = "Agentnet-Signals"
	SignalDomain        = "agentnet-live-signal-v1\n"
	SignalTTL           = 5 * time.Second
	TypingThrottle      = 3 * time.Second
	MaxSignalCiphertext = 2048
	MaxSignalBody       = 4096
)

type Signal struct {
	V       int    `json:"v"`
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	TS      int64  `json:"ts"`      // Unix milliseconds
	Session string `json:"session"` // exact recipient run; old signals cannot enter a restarted run
	CT      []byte `json:"ct"`
	Sig     []byte `json:"sig,omitempty"`
}
type TypingScope struct {
	Conv   string `json:"conv,omitempty"`
	Peer   string `json:"peer,omitempty"`   // local peer, never a relay field
	Thread string `json:"thread,omitempty"` // exact legacy thread root
}

func (s TypingScope) Validate() error {
	if s.Conv != "" {
		if !ValidHash(s.Conv) || s.Peer != "" || s.Thread != "" {
			return errors.New("typing: invalid conversation scope")
		}
		return nil
	}
	if _, _, err := SplitAddress(s.Peer); err != nil || !ValidID(s.Thread) {
		return errors.New("typing: exact peer and thread required")
	}
	return nil
}

type TypingPlain struct {
	V       int    `json:"v"`
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	TS      int64  `json:"ts"`
	Session string `json:"session"`
	Realm   string `json:"realm"`
	Conv    string `json:"conv,omitempty"`
	Thread  string `json:"thread,omitempty"`
	Origin  string `json:"origin"` // human composer assertion, not proof of a person at the keyboard
	Active  bool   `json:"active"`
}

func (s Signal) Canonical() []byte {
	s.Sig = nil
	raw, _ := json.Marshal(s)
	return append([]byte(SignalDomain), raw...)
}
func (s Signal) Verify(key ed25519.PublicKey, now time.Time) error {
	if s.V != 1 || !ValidID(s.ID) || !ValidID(s.Session) || len(s.CT) == 0 || len(s.CT) > MaxSignalCiphertext {
		return errors.New("signal: invalid shape")
	}
	if _, _, e := SplitAddress(s.From); e != nil {
		return errors.New("signal: invalid sender")
	}
	if _, _, e := SplitAddress(s.To); e != nil {
		return errors.New("signal: invalid recipient")
	}
	if s.TS <= 0 || s.TS <= now.Add(-SignalTTL).UnixMilli() || s.TS > now.Add(time.Second).UnixMilli() {
		return errors.New("signal: expired or future time")
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, s.Canonical(), s.Sig) {
		return errors.New("signal: signature invalid")
	}
	return nil
}
func ParseSignal(raw []byte) (Signal, error) {
	var s Signal
	if len(raw) > MaxSignalBody {
		return s, errors.New("signal: body too large")
	}
	err := decodeStrictJSON(raw, &s)
	return s, err
}
func SealTyping(in TypingPlain, sender ed25519.PrivateKey, to identity.Public) (Signal, error) {
	var out Signal
	if in.V != 1 || !ValidID(in.ID) || !ValidID(in.Session) || !ValidID(in.Realm) || in.Origin != "human" || in.To != to.Address {
		return out, errors.New("typing: invalid encrypted header")
	}
	scope := TypingScope{Conv: in.Conv, Peer: in.To, Thread: in.Thread}
	if in.Conv != "" {
		scope.Peer = ""
	}
	if err := scope.Validate(); err != nil {
		return out, err
	}
	if err := to.Verify(); err != nil {
		return out, err
	}
	r, err := to.Recipient()
	if err != nil {
		return out, err
	}
	plain, _ := json.Marshal(in)
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, r)
	if err != nil {
		return out, err
	}
	if _, err = w.Write(plain); err != nil {
		return out, err
	}
	if err = w.Close(); err != nil {
		return out, err
	}
	if ct.Len() > MaxSignalCiphertext {
		return out, errors.New("signal: ciphertext too large")
	}
	out = Signal{V: 1, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Session: in.Session, CT: ct.Bytes()}
	out.Sig = ed25519.Sign(sender, out.Canonical())
	return out, nil
}
func OpenTyping(s Signal, self *identity.Identity, address string, sender identity.Public, realm string, now time.Time) (TypingPlain, error) {
	var in TypingPlain
	if s.To != address || s.From != sender.Address {
		return in, errors.New("signal: wrong destination or sender")
	}
	if err := s.Verify(sender.SignKey, now); err != nil {
		return in, err
	}
	r, err := age.Decrypt(bytes.NewReader(s.CT), self.Box)
	if err != nil {
		return in, errors.New("signal: cannot decrypt")
	}
	plain, err := io.ReadAll(io.LimitReader(r, MaxSignalCiphertext+1))
	if err != nil || len(plain) > MaxSignalCiphertext {
		return in, errors.New("signal: invalid plaintext")
	}
	if err := decodeStrictJSON(plain, &in); err != nil {
		return in, err
	}
	if in.V != s.V || in.ID != s.ID || in.From != s.From || in.To != s.To || in.TS != s.TS || in.Session != s.Session || in.Realm != realm || !ValidID(realm) || in.Origin != "human" {
		return in, errors.New("signal: encrypted header mismatch")
	}
	scope := TypingScope{Conv: in.Conv, Peer: s.From, Thread: in.Thread}
	if in.Conv != "" {
		scope.Peer = ""
	}
	return in, scope.Validate()
}

// ReplayWindow never evicts unexpired evidence to make room. Saturation
// drops new signals. It is memory-only; caller serializes access.
type ReplayWindow map[string]time.Time

func (w ReplayWindow) Accept(key string, expires, now time.Time, limit int) bool {
	for k, e := range w {
		if !e.After(now) {
			delete(w, k)
		}
	}
	if _, ok := w[key]; ok || len(w) >= limit {
		return false
	}
	w[key] = expires
	return true
}
