package client

import (
	"errors"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// DirectDestination is an original exchange's execution identity. Personal
// topic placement and the current default never change it.
type DirectDestination = protocol.DirectTopicDestination

func directGroupDestination(ids []string, rows map[string]threadRow) (DirectDestination, error) {
	var destination DirectDestination
	for _, id := range ids {
		d := rows[id].destination
		if d.Host == "" || !protocol.ValidFingerprint(d.Fingerprint) {
			return destination, errors.New("the original exchange's exact agent host key is unavailable")
		}
		if destination.Host != "" && d != destination {
			return destination, errors.New("messages belong to different agents or host keys")
		}
		destination = d
	}
	if destination.Host == "" {
		return destination, errors.New("the original exchange is unavailable")
	}
	return destination, nil
}

func resolveDirectDestinations(rows map[string]threadRow) {
	// Older sent rows can lack recipient_fp. An admitted exact reply may
	// establish that original exchange's host key; current pins never do.
	for id, r := range rows {
		if r.destination.Host == "" || r.destination.Fingerprint != "" {
			continue
		}
		candidate := ""
		ambiguous := false
		for _, child := range rows {
			if child.replyTo != id || child.destination.Host != r.destination.Host || child.destination.AgentID != r.destination.AgentID || child.destination.Fingerprint == "" || !(child.kind == "answer" || child.kind == "result" || child.status == "progress") {
				continue
			}
			if candidate != "" && candidate != child.destination.Fingerprint {
				ambiguous = true
			}
			candidate = child.destination.Fingerprint
		}
		if !ambiguous {
			r.destination.Fingerprint = candidate
			rows[id] = r
		}
	}
	var resolve func(string, map[string]bool) DirectDestination
	resolve = func(id string, seen map[string]bool) DirectDestination {
		r, ok := rows[id]
		if !ok || seen[id] {
			return DirectDestination{}
		}
		seen[id] = true
		if r.destination.Host != "" {
			return r.destination
		}
		r.destination = resolve(r.replyTo, seen)
		rows[id] = r
		return r.destination
	}
	for id := range rows {
		resolve(id, map[string]bool{})
	}
}
