package hub

import (
	"net/http"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The member list lets any enrolled agent discover the others: every
// enrolled, unrevoked address with its presence (GET /v1/agents), and the
// same list pushed on each push stream when it connects and whenever it
// changes (an agent joins or is revoked, or its presence changes). It carries
// no keys and grants nothing; a sender still looks up and trusts a key per
// address as before.

// membersChanged moves the member list's generation on and wakes every push
// stream to send it.
func (h *Hub) membersChanged() {
	h.membersGen.Add(1)
	h.streams.notifyAll()
}

// members builds the current member list.
func (h *Hub) members() (protocol.Members, error) {
	rows, truncated, err := h.store.members(protocol.MaxMembers)
	if err != nil {
		return protocol.Members{}, err
	}
	out := protocol.Members{Members: make([]protocol.Member, 0, len(rows)), Truncated: truncated}
	for _, r := range rows {
		out.Members = append(out.Members, protocol.Member{Address: r.address, Presence: h.presence.state(r.address), Joined: r.joined, Person: r.person})
	}
	return out, nil
}

func (h *Hub) handleMembers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authenticate(w, r); !ok {
		return
	}
	m, err := h.members()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "", "storage error")
		return
	}
	writeJSON(w, http.StatusOK, m)
}
