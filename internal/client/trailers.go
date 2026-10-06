package client

import (
	"strings"

	"github.com/misunders2d/agentnet/internal/envelope"
)

const topicClosurePromptText = "Answering one request or finishing its task does not close this conversation topic. Never infer topic closure from a successful answer or from having nothing more to report. Add `topic: done` only when the requester explicitly asks to close or end the topic; otherwise leave it open for follow-up.\n"
const topicPromptText = topicClosurePromptText + "The optional topic and reaction lines may appear in either order at the end.\n"

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
