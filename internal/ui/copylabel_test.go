package ui

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

// BUG-10: a sent DM message's state is said about the other person's
// device, or "all devices" when only copies to this person's own other
// devices lag; never as if one of those were the recipient.
func TestDMStateNamesPeerNotOwnDevice(t *testing.T) {
	phone := func(state string) client.ConvCopy { return client.ConvCopy{To: "admin/phone", State: state, Own: true} }
	bob := func(state string) client.ConvCopy { return client.ConvCopy{To: "bob/laptop", State: state} }
	for _, tc := range []struct {
		name  string
		state string
		cs    []client.ConvCopy
		want  string
	}{
		{"all delivered, own copy first", "delivered", []client.ConvCopy{phone("delivered"), bob("delivered")}, "bob/laptop"},
		{"own copy held", "custody", []client.ConvCopy{phone("custody"), bob("delivered")}, "all devices"},
		{"peer copy held", "custody", []client.ConvCopy{phone("delivered"), bob("custody")}, "bob/laptop"},
		{"both held, own copy first", "custody", []client.ConvCopy{phone("custody"), bob("custody")}, "bob/laptop"},
		{"sent from another device", "", nil, "bob/desk"},
	} {
		m := client.ConvMessage{Dir: "out", State: tc.state, Copies: tc.cs}
		if got := laggingCopy(m, "bob/desk"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	m := client.ConvMessage{Dir: "out", Kind: KindMessage, State: "delivered", Copies: []client.ConvCopy{phone("delivered"), bob("delivered")}}
	if got := DMStateText(m.Dir, m.Kind, m.State, laggingCopy(m, "bob/desk"), ""); got != "Delivered to bob/laptop" {
		t.Errorf("delivered: %q", got)
	}
}
