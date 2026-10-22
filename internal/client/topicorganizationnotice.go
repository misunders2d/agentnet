package client

import (
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// Names are presentation. The signed Topic remains the destination identity.
func TopicOrganizationNotice(topic, title string) string {
	if topic == "" {
		return "Moved selected messages to Main flow."
	}
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return "Moved selected messages to topic " + topic + "."
	}
	return "Moved selected messages to “" + title + "”."
}

// NewTopicOrganizationNotice is the explicit new-topic name supplied by the
// current native action. Legacy notices can contain a host's private label,
// so only this distinct wording may supply a reader's automatic topic name.
func NewTopicOrganizationNotice(topic, title string) string {
	title = envelope.NormalizeSharedTopicTitle(title)
	if topic == "" || title == "" {
		return TopicOrganizationNotice(topic, "")
	}
	return "Moved selected messages to new topic “" + title + "”."
}

// topicOrganizationNoticeTitle is only a display fallback for an already
// admitted agent move. It never turns arbitrary prose into shared metadata.
func topicOrganizationNoticeTitle(topic, body string) string {
	const prefix, suffix = "Moved selected messages to new topic “", "”."
	if topic == "" || !strings.HasPrefix(body, prefix) || !strings.HasSuffix(body, suffix) {
		return ""
	}
	title := strings.TrimSuffix(strings.TrimPrefix(body, prefix), suffix)
	if title == "" || utf8.RuneCountInString(title) > TopicTitleMax || envelope.NormalizeSharedTopicTitle(title) != title || NewTopicOrganizationNotice(topic, title) != body {
		return ""
	}
	return title
}

func (a *Agent) TopicOrganizationNotice(conv, topic, title string) (string, error) {
	if topic == "" {
		return TopicOrganizationNotice(topic, ""), nil
	}
	if title == "" {
		err := a.store.db.QueryRow(`SELECT coalesce(title,'') FROM topic_state WHERE peer=? AND topic=?`, conv, topic).Scan(&title)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return TopicOrganizationNotice(topic, title), nil
}
