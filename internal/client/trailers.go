package client

import (
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

const topicPromptText = "If this reply finishes the work of this topic and nothing more is expected, add a last line `topic: done`. Leave it out while the conversation may go on. The optional topic and reaction lines may appear in either order at the end.\n"

// splitTrailers accepts each optional trailer once, in either order. A close
// is a display hint on a successful nonempty reply, never work authority.
// A proposal loses its formatting trailers but never closes the topic.
func splitTrailers(text, status string) (string, *reactionChoice, bool) {
	text = strings.TrimRight(text, " \t\r\n")
	var reaction *reactionChoice
	done, topicSeen := false, false
	for n := 0; n < 2; n++ {
		i := strings.LastIndexByte(text, '\n')
		line := text[i+1:]
		rest := ""
		if i >= 0 {
			rest = strings.TrimSpace(text[:i])
		}
		if rest == "" {
			break
		}
		if strings.EqualFold(strings.TrimSpace(line), "topic: done") && !topicSeen && (status == envelope.StatusDone || status == envelope.StatusProposal) {
			text = rest
			topicSeen, done = true, status == envelope.StatusDone
			continue
		}
		if reaction == nil {
			next, choice := splitReaction(text)
			if choice != nil {
				text = next
				reaction = choice
				continue
			}
		}
		break
	}
	return text, reaction, done
}
