package protocol

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const MaxTopicTitles = 64
const MaxTopicTitleRevision int64 = 9007199254740991

// TopicTitle is a private display preference, never a room event or permission.
// An empty title is an explicit reset. Writer breaks concurrent revision ties;
// the current own-human carrier authenticates the preference, not this marker.
type TopicTitle struct {
	Scope  string `json:"scope"`
	Topic  string `json:"topic"`
	Title  string `json:"title"`
	Rev    int64  `json:"rev"`
	Writer string `json:"writer"`
}

type TopicSync struct {
	V      int          `json:"v"`
	Person string       `json:"person"`
	Roster string       `json:"roster"`
	Titles []TopicTitle `json:"titles"`
}

func ParseTopicSync(data []byte) (TopicSync, error) {
	var r TopicSync
	if len(data) > 65536 || decodeStrictJSON(data, &r) != nil || r.V != 1 || !ValidID(r.Person) || !ValidHash(r.Roster) || len(r.Titles) == 0 || len(r.Titles) > MaxTopicTitles {
		return r, errors.New("topic sync: invalid owner or titles")
	}
	seen := map[[2]string]bool{}
	var fields struct {
		Titles []map[string]json.RawMessage `json:"titles"`
	}
	_ = json.Unmarshal(data, &fields)
	for i, title := range r.Titles {
		_, _, addressErr := SplitAddress(title.Scope)
		key := [2]string{title.Scope, title.Topic}
		if len(fields.Titles[i]["title"]) == 0 || string(fields.Titles[i]["title"]) == "null" || !ValidHash(title.Scope) && addressErr != nil || !ValidID(title.Topic) || !ValidFingerprint(title.Writer) || title.Rev < 1 || title.Rev > MaxTopicTitleRevision || utf8.RuneCountInString(title.Title) > 120 || title.Title != strings.Join(strings.Fields(title.Title), " ") || seen[key] {
			return r, errors.New("topic sync: invalid or duplicate title")
		}
		seen[key] = true
	}
	return r, nil
}
