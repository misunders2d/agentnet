package ui

import (
	"context"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// ConversationResourceEditor exposes the single human-member group link edit.
type ConversationResourceEditor interface {
	SetConversationResources(context.Context, ConversationResourcesChange) (client.ConversationResources, error)
}

type ConversationResourcesChange struct {
	Conv   string                  `json:"conv"`
	Links  []protocol.ResourceLink `json:"links"`
	Rev    int64                   `json:"rev"`
	Writer string                  `json:"writer"`
}

func (l *Live) SetConversationResources(ctx context.Context, c ConversationResourcesChange) (client.ConversationResources, error) {
	ctx, cancel := context.WithTimeout(ctx, l.timeout)
	defer cancel()
	resources, err := l.a.SetConversationResources(ctx, c.Conv, c.Links, c.Rev, c.Writer)
	if err != nil {
		return resources, Refuse(sentence(err))
	}
	return resources, nil
}
