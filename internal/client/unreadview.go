package client

import "github.com/misunders2d/agentnet/internal/envelope"

// UnreadConversationCandidates is a conservative prefilter, not an unread
// projection. The normal admitted message/control/read-generation projection
// must still decide which rows are unread. False positives are harmless;
// completely read conversations need no body, file or participation work.
func (a *Agent) UnreadConversationCandidates() (map[string]bool, error) {
	rows, err := a.store.db.Query(`SELECT DISTINCT conv FROM inbox
		WHERE conv IS NOT NULL AND local=0 AND ref_id IS NULL AND read_at IS NULL
		AND ((coalesce(sub,'')='' AND kind IN ('message','question','task','answer','result')) OR sub=?)
		UNION SELECT m.conv FROM attention_marks m JOIN persons p ON p.person=m.owner AND p.state=?
		WHERE m.conv<>'' AND m.state='unread'`, envelope.SubExcerpt, personSelf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var conv string
		if err := rows.Scan(&conv); err != nil {
			return nil, err
		}
		out[conv] = true
	}
	return out, rows.Err()
}

// UnreadDeviceRefs reuses the canonical thread/placement/read projection,
// without building cards or hydrating titles for unrelated read topics.
func (a *Agent) UnreadDeviceRefs() ([]DeviceUnreadRef, error) {
	peers, err := a.store.conversationPeers()
	if err != nil {
		return nil, err
	}
	var out []DeviceUnreadRef
	for _, peer := range peers {
		start := len(out)
		topics, err := a.peerTopics(peer, storeNow().Unix(), &out)
		if err != nil {
			return nil, err
		}
		var selected []*ThreadSummary
		for i := range topics {
			if topics[i].Unread > 0 {
				selected = append(selected, &topics[i])
			}
		}
		if err := a.topicText(peer, selected); err != nil {
			return nil, err
		}
		titles := make(map[string]string, len(selected))
		for _, topic := range selected {
			titles[topic.ID] = topic.Title
		}
		for i := start; i < len(out); i++ {
			out[i].TopicTitle = titles[out[i].Thread]
		}
	}
	return out, nil
}
