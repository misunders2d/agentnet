package protocol

import (
	"errors"
	"net/url"
)

// Message lanes are signed request routing metadata, never message authority.
// An absent lane retains the historical live behavior for older clients.
const (
	MessageLaneQuery = "lane"
	MessageLaneLive  = "live"
	MessageLaneSync  = "sync"
)

func ParseMessageLane(query url.Values) (string, error) {
	values, present := query[MessageLaneQuery]
	if !present {
		return MessageLaneLive, nil
	}
	if len(values) != 1 || values[0] != MessageLaneLive && values[0] != MessageLaneSync {
		return "", errors.New("message lane must be live or sync, once")
	}
	return values[0], nil
}
