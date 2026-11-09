package client

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// agentMentionLabel is how an agent is named in a mention (owner, 2026-11-09):
// whose agent, which agent and on which device, "Sergey's Pi · Linux laptop",
// so two agents of one person never read alike. A label is a claim; the
// mention's participation ID is what identifies the agent.
func agentMentionLabel(host PersonInfo, name string) string {
	owner := strings.TrimSpace(host.Label)
	if owner == "" {
		owner = "Someone"
	}
	if name = strings.TrimSpace(name); name == "" || strings.HasPrefix(name, "name unavailable") {
		name = "agent"
	}
	return owner + "'s " + name + " · " + DeviceWords(host.Address)
}

// mentionRoster gives a running agent the exact mention of every current
// member person and every present agent and guest of its chat (or topic),
// labeled, so it can tag anyone here exactly instead of writing a name
// (owner, 2026-11-09: agents addressed each other as plain text). Labels
// are claims; only the mention's ID identifies whom it tags.
func (a *Agent) mentionRoster(contexts []context.Context, j job, info ParticipationInfo, m dmMembers) string {
	parts, err := a.Participations(j.Conv)
	if err != nil {
		return ""
	}
	ctx := context.Background()
	if len(contexts) != 0 {
		ctx = contexts[0]
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var present []ParticipationInfo
	for _, p := range parts {
		if p.Claimable() && (p.Topic == nil || sameParticipationTopic(p.Topic, info.Topic)) {
			present = append(present, p)
		}
	}
	names := a.roomAgentNames(ctx, present)
	var lines []string
	others := false
	persons := make([]personRow, 0, len(m.persons))
	for _, p := range m.persons {
		persons = append(persons, p)
	}
	slices.SortFunc(persons, func(x, y personRow) int { return strings.Compare(x.info.Person, y.info.Person) })
	for _, p := range persons {
		who := "another person"
		if p.info.Person == info.Host.Person {
			who = "your owner"
		}
		if anchor := mentionAnchor("person", p.info.Person, p.info.Label); anchor != "" {
			lines = append(lines, fmt.Sprintf("- %s, %s: %s", who, promptLabel(p.info.Label), anchor))
		}
	}
	slices.SortFunc(present, func(x, y ParticipationInfo) int { return strings.Compare(x.PID, y.PID) })
	for _, p := range present {
		switch {
		case p.Role == protocol.RoleHuman && p.HumanActive():
			if anchor := mentionAnchor("guest", p.PID, p.Host.Label); anchor != "" {
				lines = append(lines, fmt.Sprintf("- a guest, %s on %s: %s", promptLabel(p.Host.Label), DeviceWords(p.Host.Address), anchor))
			}
		case p.Role == "":
			whose := "another person's agent"
			if p.Host.Person == info.Host.Person {
				whose = "another agent of your owner"
			}
			if p.PID == info.PID {
				whose = "you (this agent)"
			} else {
				others = true
			}
			label := agentMentionLabel(p.Host, names[p.PID])
			if anchor := mentionAnchor("agent", p.PID, label); anchor != "" {
				lines = append(lines, fmt.Sprintf("- %s, %s (PID %s, host device %s): %s", whose, promptLabel(label), p.PID, p.Host.Address, anchor))
			}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	tags := ""
	if others {
		tags = agentTagPromptText
	}
	return "\n## Who is here: exact mentions\nTo address someone here, write their exact mention below, as written; a plain name is not a tag. Names are claims; the mention's ID says whom it tags.\n" + strings.Join(lines, "\n") + "\n" + tags
}
