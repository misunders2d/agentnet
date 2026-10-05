package client

import (
	"context"
	"slices"
	"strings"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Agents see people, not devices (MEL-525): every agent-facing text names
// who asks as a person, from the verified person record (the device's exact
// key in that person's signed roster), never from a name someone typed.
// A device no verified record names, or whose person is frozen, is named
// as that device. The relation is worked out when the text is made and
// never stored: a device removed from its person, or a key that changed,
// reads differently at once. Labels are each person's own claim; the
// address and the short key stay in the text as the verified details.

// Sender relations.
const (
	SenderSelf       = "self"       // this device's own person, asking here
	SenderOwner      = "owner"      // this device's own person, from another of their devices
	SenderPerson     = "person"     // another person, verified
	SenderUnverified = "unverified" // a device no verified person record names
)

// Sender is who sent a request, as this device can prove it.
type Sender struct {
	Relation string
	Person   string // the person's id ("" when unverified, or self with no person)
	Label    string // the person's own name for themselves (a claim)
	Address  string // the sending device
	Device   string // that device in words ("Pixel")
	Key      string // the first group of the device key's fingerprint
	// NoSelf: this installation speaks for no person (a device such as a
	// shared agent server), so another person is "a person in this
	// workspace", not "another person".
	NoSelf bool
}

// shortKey is the first group of a fingerprint ("19c77bce").
func shortKey(fp string) string {
	k, _, _ := strings.Cut(fp, "-")
	return k
}

// deviceSpecial are device-name words with their own casing.
var deviceSpecial = map[string]string{"iphone": "iPhone", "ipad": "iPad", "imac": "iMac", "mac": "Mac", "macbook": "MacBook"}

// DeviceWords is a device address in words, as every screen shows it: the
// part after the "/", dashes as spaces, the first letter capital, and
// iPhone, iPad, iMac, Mac and MacBook as they are written
// ("bohdan/windows-laptop" → "Windows laptop", "admin/iphone" → "iPhone").
// The same rule runs in the pages (internal/ui/testdata/device_words.json).
func DeviceWords(address string) string {
	name := address
	if i := strings.IndexByte(address, '/'); i >= 0 {
		name = address[i+1:]
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return r == '-' })
	for i, w := range words {
		if s, ok := deviceSpecial[strings.ToLower(w)]; ok {
			words[i] = s
		} else if i == 0 && w[0] >= 'a' && w[0] <= 'z' {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

// sender works out who sent a request from the device at address, verified
// by the key with fingerprint fp (local: asked here, by this device's own
// person). Anything not proven fails closed into the unverified relation.
func (a *Agent) sender(ctx context.Context, address, fp string, local bool) Sender {
	return a.senderWith(ctx, address, fp, local, true)
}

// senderHere is sender from what this device holds, with no Hub request
// (for hook lines, which must stay fast and offline): a person not pinned
// here reads as an unverified device.
func (a *Agent) senderHere(address, fp string) Sender {
	return a.senderWith(context.Background(), address, fp, false, false)
}

func (a *Agent) senderWith(ctx context.Context, address, fp string, local, fetch bool) Sender {
	s := Sender{Relation: SenderUnverified, Address: address, Device: DeviceWords(address), Key: shortKey(fp)}
	self, hasSelf, err := a.store.selfPerson(a.Address)
	if err != nil {
		return s
	}
	s.NoSelf = !hasSelf
	if local || address == a.Address {
		s.Relation, s.Address, s.Device, s.Key = SenderSelf, a.Address, DeviceWords(a.Address), shortKey(a.Self().Fingerprint())
		if hasSelf {
			s.Person, s.Label = self.info.Person, self.info.Label
		}
		return s
	}
	if fp == "" {
		return s
	}
	// The key that verified the request must be the one pinned for the
	// device, with no key change waiting.
	pinned, pending, found, err := a.store.peer(address)
	if err != nil || !found || pending != nil || pinned.Fingerprint() != fp {
		return s
	}
	if hasSelf && self.has(address, fp) {
		s.Relation, s.Person, s.Label = SenderOwner, self.info.Person, self.info.Label
		return s
	}
	p, ok, err := a.store.personByAddress(address)
	if err != nil {
		return s
	}
	if !ok && !fetch {
		return s
	}
	if !ok {
		// Not pinned yet (a device with no person of its own asks rarely
		// meets the asker's person): fetch and verify the person's chain
		// from the Hub, as a conversation message from them would.
		if p, err = a.personOfKey(ctx, address, pinned); err != nil {
			return s
		}
	}
	if p.info.State != personPinned || !p.has(address, fp) {
		return s // frozen (conflict), or the record no longer lists this key
	}
	s.Relation, s.Person, s.Label = SenderPerson, p.info.Person, p.info.Label
	return s
}

// personSender words a member of a conversation whose device and key its
// verified person record (p) already names: the owner when p is this
// installation's own person.
func (a *Agent) personSender(p personRow, address, fp string) Sender {
	s := Sender{Relation: SenderPerson, Person: p.info.Person, Label: p.info.Label, Address: address, Device: DeviceWords(address), Key: shortKey(fp)}
	if p.info.State == personSelf {
		s.Relation = SenderOwner
		if address == a.Address {
			s.Relation = SenderSelf
		}
	} else if _, ok, err := a.store.selfPerson(a.Address); err == nil && !ok {
		s.NoSelf = true
	}
	return s
}

// label is the person's name, or a stand-in when they gave none.
func (s Sender) label() string {
	if s.Label != "" {
		return s.Label
	}
	if s.Relation == SenderSelf || s.Relation == SenderOwner {
		return "Your owner"
	}
	return "A person"
}

// details are the verified details: the address and the short key.
func (s Sender) details() string {
	if s.Key == "" {
		return s.Address
	}
	return s.Address + ", key " + s.Key
}

// Words is the long form, for the sentence that says who asks:
// "Sergey — your owner — writing from their device Pixel (admin/pixel, key 19c77bce)".
func (s Sender) Words() string {
	switch s.Relation {
	case SenderSelf:
		if s.Label == "" {
			return "your owner, on this device"
		}
		return s.Label + " — your owner — on this device"
	case SenderOwner:
		return s.label() + " — your owner — writing from their device " + s.Device + " (" + s.details() + ")"
	case SenderPerson:
		who := " (another person)"
		if s.NoSelf {
			who = " (a person in this workspace)"
		}
		return s.label() + who + " writing from their device " + s.Device + " (" + s.details() + ")"
	}
	if s.Key == "" {
		return "the device " + s.Address + ", which this device could not match to a verified person"
	}
	return "the device " + s.Address + " (key " + s.Key + "), which this device could not match to a verified person"
}

// Name is the short form, for lines of earlier messages: "Sergey (your
// owner) on Pixel", "Vitalii on Desk", "the device admin/pixel".
func (s Sender) Name() string {
	switch s.Relation {
	case SenderSelf:
		if s.Label == "" {
			return "your owner, on this device"
		}
		return s.Label + " (your owner) on this device"
	case SenderOwner:
		return s.label() + " (your owner) on " + s.Device
	case SenderPerson:
		return s.label() + " on " + s.Device
	}
	return "the device " + s.Address
}

// Ref is Name with the address: "Vitalii on Desk (vitalii/desk)".
func (s Sender) Ref() string {
	if s.Relation == SenderUnverified {
		return s.Name()
	}
	return s.Name() + " (" + s.Address + ")"
}

// Owner reports whether s is this device's own person.
func (s Sender) Owner() bool { return s.Relation == SenderSelf || s.Relation == SenderOwner }

// selfIntro tells the agent whose agent it is and where it runs.
func (a *Agent) selfIntro() string {
	self, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return "You are the agent on the AgentNet device " + a.Address + ", which is not linked to a person."
	}
	label := self.info.Label
	if label == "" {
		label = "your owner"
	}
	return "You are the agent of " + label + ", running on their device " + DeviceWords(a.Address) + " (" + a.Address + ")."
}

// PeerWords returns a namer of devices for people, as the page's sentences
// use them (MEL-525: people never see addresses): "your Pixel" for another
// device of this installation's own person, "Vitalii (Desk)" for a device a
// verified person record here names, and the device in words otherwise
// ("Bezos"). When that person's name is also this person's own or another
// known person's, the device key's first group follows the device, so a
// look-alike name cannot pass as someone else. What is not an address
// (e.g. "all devices") is returned as it is. The records are read once.
func (a *Agent) PeerWords() func(address string) string {
	self, hasSelf, _ := a.store.selfPerson(a.Address)
	known, _ := a.KnownPersons()
	labels := map[string]int{}
	if hasSelf {
		labels[strings.ToLower(self.info.Label)]++
	}
	for _, p := range known {
		if p.State == personPinned {
			labels[strings.ToLower(p.Label)]++
		}
	}
	return func(address string) string {
		if _, _, err := protocol.SplitAddress(address); err != nil {
			return address
		}
		if address == a.Address {
			return "this device"
		}
		device := DeviceWords(address)
		if hasSelf && slices.ContainsFunc(self.roster.Devices, func(d identity.Public) bool { return d.Address == address }) {
			return "your " + device
		}
		p, ok, err := a.store.personByAddress(address)
		if err != nil || !ok || p.info.State != personPinned || p.info.Label == "" {
			return device
		}
		if labels[strings.ToLower(p.info.Label)] > 1 {
			device += " · " + shortKey(p.info.Fingerprint)
		}
		return p.info.Label + " (" + device + ")"
	}
}
