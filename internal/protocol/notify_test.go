package protocol

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Channel vectors (computed independently): one conversation, two
// recipient devices; the same recipient in another conversation differs.
func TestNotifyChannelVectors(t *testing.T) {
	conv := "e0758d3e1872da6abc62304e16423c9ae8782d39be3517e4503df4b6ac88b75a"
	for fp, want := range map[string]string{
		"01234567-89abcdef-01234567-89abcdef": "CRTkM8HtV3rVNaVsnOnalw",
		"fedcba98-76543210-fedcba98-76543210": "bs5zEfoANj9lBOw3SnQxAQ",
	} {
		if got := NotifyChannel(conv, fp); got != want || !ValidNotifyChannel(got) {
			t.Fatalf("channel for %s: %q, want %q", fp, got, want)
		}
	}
	other := strings.Repeat("1", 64)
	if NotifyChannel(other, "01234567-89abcdef-01234567-89abcdef") == "CRTkM8HtV3rVNaVsnOnalw" {
		t.Fatal("two conversations share a channel")
	}
	for _, bad := range []string{"", "CRTkM8HtV3rVNaVsnOnal", "CRTkM8HtV3rVNaVsnOnalw=", "CRTkM8HtV3rVNaVsnOna+w", "CRTkM8HtV3rVNaVsnOnalx"} {
		if ValidNotifyChannel(bad) {
			t.Errorf("%q accepted as a channel", bad)
		}
	}
}

func key(n int, first byte) string {
	b := make([]byte, n)
	b[0] = first
	return base64.RawURLEncoding.EncodeToString(b)
}

// A push subscription is refused unless it names an allowed push service
// over plain https on 443 and has well-formed keys; the error never carries
// the endpoint or keys.
func TestPushSubscriptionValidate(t *testing.T) {
	good := PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/SECRETTOKEN", P256DH: key(65, 4), Auth: key(16, 1)}
	if err := good.Validate(DefaultPushHosts); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"https://web.push.apple.com/QSECRET", "https://updates.push.services.mozilla.com/wpush/v2/SECRET",
		"https://wns2-par02p.notify.windows.com/w/?token=SECRET", "https://FCM.googleapis.com:443/fcm/send/SECRET"} {
		s := good
		s.Endpoint = ep
		if err := s.Validate(DefaultPushHosts); err != nil {
			t.Errorf("%s refused: %v", ep, err)
		}
	}
	for name, s := range map[string]PushSubscription{
		"http":           {Endpoint: "http://fcm.googleapis.com/fcm/send/SECRET"},
		"port":           {Endpoint: "https://fcm.googleapis.com:8443/SECRET"},
		"userinfo":       {Endpoint: "https://SECRET@fcm.googleapis.com/x"},
		"fragment":       {Endpoint: "https://fcm.googleapis.com/x#SECRET"},
		"ip literal":     {Endpoint: "https://169.254.169.254/SECRET"},
		"ipv6 literal":   {Endpoint: "https://[::1]/SECRET"},
		"other host":     {Endpoint: "https://evil.example/SECRET"},
		"suffix trick":   {Endpoint: "https://evilfcm.googleapis.com.evil.example/SECRET"},
		"no label break": {Endpoint: "https://xfcm.googleapis.com/SECRET"},
		"trailing dot":   {Endpoint: "https://fcm.googleapis.com./SECRET"},
		"long":           {Endpoint: "https://fcm.googleapis.com/" + strings.Repeat("S", MaxPushEndpoint) + "SECRET"},
		"short p256dh":   {Endpoint: good.Endpoint, P256DH: key(33, 2), Auth: good.Auth},
		"bad auth":       {Endpoint: good.Endpoint, P256DH: good.P256DH, Auth: "SECRET!"},
	} {
		if s.P256DH == "" {
			s.P256DH, s.Auth = good.P256DH, good.Auth
		}
		err := s.Validate(DefaultPushHosts)
		if err == nil {
			t.Errorf("%s accepted", name)
		} else if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: the error carries the endpoint: %v", name, err)
		}
	}
}

func TestNotifyPrefsAndSeenValidate(t *testing.T) {
	ch := NotifyChannel(strings.Repeat("a", 64), "01234567-89abcdef-01234567-89abcdef")
	ok := NotifyPrefs{Enabled: true, Senders: []NotifySender{{"admin/laptop", "01234567-89abcdef-01234567-89abcdef"}}, Mutes: []string{ch}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]NotifyPrefs{
		"repeated sender": {Senders: []NotifySender{ok.Senders[0], ok.Senders[0]}},
		"bad fingerprint": {Senders: []NotifySender{{"admin/laptop", "x"}}},
		"bad address":     {Senders: []NotifySender{{"admin", "01234567-89abcdef-01234567-89abcdef"}}},
		"bad mute":        {Mutes: []string{"nope"}},
		"repeated mute":   {Mutes: []string{ch, ch}},
		"too many mutes":  {Mutes: make([]string, MaxNotifyMutes+1)},
	} {
		if p.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	id := strings.Repeat("0", 32)
	if (NotifySeen{Channel: ch, IDs: []string{id}}).Validate() != nil {
		t.Fatal("seen refused")
	}
	for name, s := range map[string]NotifySeen{
		"no ids":      {Channel: ch},
		"bad channel": {Channel: "x", IDs: []string{id}},
		"bad id":      {Channel: ch, IDs: []string{"x"}},
		"too many":    {Channel: ch, IDs: make([]string, MaxNotifySeenIDs+1)},
	} {
		if s.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestValidPushHost(t *testing.T) {
	for _, h := range []string{"push.example.org", "a-b.push.example.org", "web.push.apple.com"} {
		if !ValidPushHost(h) {
			t.Errorf("%q refused", h)
		}
	}
	for _, h := range []string{"", "localhost", "10.0.0.1", "::1", "https://x.org", "x.org:443", "x.org/p", "-x.org", "x-.org", "X.org", "x..org", "x.org."} {
		if ValidPushHost(h) {
			t.Errorf("%q accepted", h)
		}
	}
}
