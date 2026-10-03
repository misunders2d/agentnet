package client

import (
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// Append only: captured signed audience survives retry/restart without widening.
const humanScopeSchema = `
ALTER TABLE inbox ADD COLUMN human TEXT;
ALTER TABLE outbox ADD COLUMN human TEXT;
`

func storedHuman(q dbq, dir, id string) (*envelope.HumanTurn, error) {
	table := "inbox"
	if dir == "out" {
		table = "outbox"
	} else if dir != "in" {
		return nil, errors.New("invalid human scope source")
	}
	var raw string
	if err := q.QueryRow(`SELECT coalesce(human,'') FROM `+table+` WHERE id=?`, id).Scan(&raw); err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, nil
	}
	var h envelope.HumanTurn
	if err := json.Unmarshal([]byte(raw), &h); err != nil {
		return nil, err
	}
	return &h, nil
}
func humanJSON(h *envelope.HumanTurn) string {
	if h == nil {
		return ""
	}
	b, _ := json.Marshal(h)
	return string(b)
}
