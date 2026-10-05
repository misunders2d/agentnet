package protocol

import (
	"strings"
	"testing"
)

func TestInviteHints(t *testing.T) {
	for s, ok := range map[string]bool{
		"": true, "Sergey": true, "Анна-Марія": true, "O'Neil & Co.": true,
		" Sergey": false, "Sergey ": false, "a\nb": false, "a‮b": false, "a\x00": false,
		strings.Repeat("я", MaxInviteHint): true, strings.Repeat("я", MaxInviteHint+1): false,
	} {
		if ValidInviteHint(s, MaxInviteHint) != ok {
			t.Errorf("ValidInviteHint(%q) = %v", s, !ok)
		}
	}
	// Hints travel in the code and come back as written; older codes have none.
	code := Invite{Hub: "https://hub.example.test", Label: "bohdan", Secret: "s", Name: "Bohdan", From: "Sergey", Workspace: "Mellanni"}.Encode()
	inv, err := DecodeInvite(code)
	if err != nil || inv.Name != "Bohdan" || inv.From != "Sergey" || inv.Workspace != "Mellanni" {
		t.Fatalf("%+v %v", inv, err)
	}
	if strings.Contains(Invite{Hub: "https://hub.example.test", Label: "b", Secret: "s"}.Encode(), "name") {
		t.Fatal("empty hints written into the code")
	}
}

func TestInviteLinkFragment(t *testing.T) {
	code := Invite{Hub: "https://hub.example.test", Label: "bohdan", Secret: "s", Name: "Bohdan"}.Encode()
	link, err := InviteLink(code)
	if err != nil || link != "https://hub.example.test/#"+code {
		t.Fatalf("link %q %v", link, err)
	}
	if _, err := InviteLink(Invite{Hub: "https://hub.example.test", Label: "b", Secret: "s", CertPEM: "PEM"}.Encode()); err == nil {
		t.Fatal("a pinned invitation became a browser link")
	}
	if got := AppOpenURL(code); got != "agentnet://open#"+code {
		t.Fatalf("app link %q", got)
	}
	if got := AppOpenURL(""); got != "agentnet://open" {
		t.Fatalf("app link %q", got)
	}
}
