package identity

import "testing"

// Remembering a success must never excuse different bytes, in any field.
func TestVerifyRemembersOnlyExactSuccess(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	p := id.Public("alice/laptop")
	for range 2 {
		if err := p.Verify(); err != nil {
			t.Fatal(err)
		}
	}
	other, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	q := other.Public("alice/laptop")
	for name, bad := range map[string]func(Public) Public{
		"address":   func(x Public) Public { x.Address = "mallory/laptop"; return x },
		"sign key":  func(x Public) Public { x.SignKey = q.SignKey; return x },
		"recipient": func(x Public) Public { x.BoxRecipient = q.BoxRecipient; return x },
		"signature": func(x Public) Public { x.BoxSig = q.BoxSig; return x },
	} {
		for range 2 { // a refusal is checked again, never remembered
			if err := bad(p).Verify(); err == nil {
				t.Fatalf("%s changed and Verify still passed", name)
			}
		}
	}
	verified.Lock()
	clear(verified.m)
	verified.Unlock()
	if err := p.Verify(); err != nil {
		t.Fatal(err)
	}
}
