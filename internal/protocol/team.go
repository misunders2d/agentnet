package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Teams are public workspace directory collections, not conversation members
// or grants. A Hub orders signed operations with compare-and-swap; clients
// verify the author roster chain and authority in the predecessor themselves.
const (
	TeamDomain        = "agentnet-team-v1\n"
	FeatureTeams      = "teams1"
	TeamsHeader       = "Agentnet-Teams"
	CodeTeamStale     = "team_stale"
	CodeTeamRefused   = "team_refused"
	MaxTeams          = 1000
	MaxTeamMembers    = 1000
	MaxTeamStep       = 2048
	TeamChainPage     = 100
	TeamCreate        = "create"
	TeamJoin          = "join"
	TeamLeave         = "leave"
	TeamRename        = "rename"
	TeamArchive       = "archive"
	TeamRestore       = "restore"
	TeamRemove        = "remove"
	TeamManagerAdd    = "manager-add"
	TeamManagerRemove = "manager-remove"
)

var (
	ErrTeamArchived    = errors.New("team is archived; a manager must explicitly restore it")
	ErrTeamManager     = errors.New("this team operation requires a manager")
	ErrTeamLastManager = errors.New("the last manager must retain membership, including while archived; explicitly add another member as manager first")
)

// Field order and JSON escaping are the canonical wire contract. Sig alone
// is excluded; the SHA-256 hash is over TeamDomain plus the compact JSON.
type TeamStep struct {
	V       int         `json:"v"`
	RealmID string      `json:"realm_id"`
	Team    string      `json:"team"`
	Seq     int64       `json:"seq"`
	Prev    string      `json:"prev"`
	Author  EventAuthor `json:"author"`
	Op      string      `json:"op"`
	Name    string      `json:"name,omitempty"`
	Target  string      `json:"target,omitempty"`
	TS      int64       `json:"ts"` // author claim; never the order or authority
	Sig     []byte      `json:"sig,omitempty"`
}

func (s TeamStep) Canonical() []byte {
	s.Sig = nil
	raw, _ := json.Marshal(s)
	return append([]byte(TeamDomain), raw...)
}
func (s TeamStep) Hash() string                 { return hashHex(s.Canonical()) }
func (s *TeamStep) Sign(key ed25519.PrivateKey) { s.Sig = ed25519.Sign(key, s.Canonical()) }

func (s TeamStep) Validate() error {
	if s.V != 1 || !ValidID(s.RealmID) || !ValidID(s.Team) || s.Seq < 0 || s.TS <= 0 {
		return errors.New("team: invalid version, identity, sequence or timestamp")
	}
	a := s.Author
	if !ValidID(a.Person) || !ValidHash(a.Roster) || !ValidFingerprint(a.Fingerprint) {
		return errors.New("team: invalid author")
	}
	if _, _, err := SplitAddress(a.Address); err != nil {
		return fmt.Errorf("team: %w", err)
	}
	if s.Seq == 0 {
		if s.Op != TeamCreate || s.Prev != "" {
			return errors.New("team: first step must create with no predecessor")
		}
	} else if s.Op == TeamCreate || !ValidHash(s.Prev) {
		return errors.New("team: later step must name its predecessor")
	}
	switch s.Op {
	case TeamCreate, TeamRename:
		if err := validLabel(s.Name); err != nil {
			return fmt.Errorf("team name: %w", err)
		}
		if s.Target != "" {
			return errors.New("team: name operation has no target")
		}
	case TeamRemove, TeamManagerAdd, TeamManagerRemove:
		if !ValidID(s.Target) || s.Name != "" {
			return errors.New("team: manager operation names a person only")
		}
	case TeamJoin, TeamLeave, TeamArchive, TeamRestore:
		if s.Name != "" || s.Target != "" {
			return errors.New("team: unexpected name or target")
		}
	default:
		return errors.New("team: unknown operation")
	}
	raw, _ := json.Marshal(s)
	if len(raw) > MaxTeamStep {
		return errors.New("team: step too large")
	}
	return nil
}

func ParseTeamStep(raw []byte) (TeamStep, error) {
	var s TeamStep
	if len(raw) > MaxTeamStep {
		return s, errors.New("team: step too large")
	}
	if err := decodeStrictJSON(raw, &s); err != nil {
		return s, err
	}
	return s, s.Validate()
}

type TeamState struct {
	RealmID  string   `json:"realm_id"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Seq      int64    `json:"seq"`
	Hash     string   `json:"hash"`
	Managers []string `json:"managers"`
	Members  []string `json:"members"`
	Archived bool     `json:"archived"`
}

func (t TeamState) Member(person string) bool  { return slices.Contains(t.Members, person) }
func (t TeamState) Manager(person string) bool { return slices.Contains(t.Managers, person) }

// Apply verifies this signature using a roster already verified as part of
// its person chain. It then applies only the operation the predecessor
// authorizes, never an author-supplied membership or manager snapshot.
func (s TeamStep) Apply(prev *TeamState, roster PersonRoster) (TeamState, error) {
	var out TeamState
	if err := s.Validate(); err != nil {
		return out, err
	}
	if roster.Person != s.Author.Person || roster.Hash() != s.Author.Roster {
		return out, errors.New("team: author roster does not match")
	}
	dev, ok := roster.Device(s.Author.Fingerprint)
	if !ok || dev.Address != s.Author.Address || !ed25519.Verify(dev.SignKey, s.Canonical(), s.Sig) {
		return out, errors.New("team: author signature invalid")
	}
	if prev == nil {
		if s.Seq != 0 {
			return out, errors.New("team: first step missing")
		}
		out = TeamState{RealmID: s.RealmID, ID: s.Team, Name: s.Name, Managers: []string{s.Author.Person}, Members: []string{s.Author.Person}}
	} else {
		if prev.RealmID != s.RealmID || prev.ID != s.Team || s.Seq != prev.Seq+1 || s.Prev != prev.Hash {
			return out, errors.New("team: predecessor mismatch")
		}
		out = *prev
		out.Managers = slices.Clone(prev.Managers)
		out.Members = slices.Clone(prev.Members)
		person := s.Author.Person
		if s.Op == TeamJoin || s.Op == TeamLeave {
			if out.Archived {
				return TeamState{}, ErrTeamArchived
			}
		} else if !out.Manager(person) {
			return TeamState{}, ErrTeamManager
		}
		switch s.Op {
		case TeamJoin:
			if !out.Member(person) {
				out.Members = append(out.Members, person)
			}
		case TeamLeave:
			if out.Manager(person) {
				if len(out.Managers) == 1 {
					return TeamState{}, ErrTeamLastManager
				}
				return TeamState{}, errors.New("remove your manager role explicitly before leaving the team")
			}
			out.Members = slices.DeleteFunc(out.Members, func(x string) bool { return x == person })
		case TeamRename:
			out.Name = s.Name
		case TeamArchive:
			out.Archived = true
		case TeamRestore:
			out.Archived = false
		case TeamRemove:
			if out.Manager(s.Target) {
				return TeamState{}, errors.New("remove the manager role explicitly before removing membership")
			}
			out.Members = slices.DeleteFunc(out.Members, func(x string) bool { return x == s.Target })
		case TeamManagerAdd:
			if !out.Member(s.Target) {
				return TeamState{}, errors.New("a new manager must already be a current member")
			}
			if !out.Manager(s.Target) {
				out.Managers = append(out.Managers, s.Target)
			}
		case TeamManagerRemove:
			if out.Manager(s.Target) && len(out.Managers) == 1 {
				return TeamState{}, ErrTeamLastManager
			}
			out.Managers = slices.DeleteFunc(out.Managers, func(x string) bool { return x == s.Target })
		}
	}
	if len(out.Members) > MaxTeamMembers {
		return TeamState{}, errors.New("team member limit reached")
	}
	slices.Sort(out.Members)
	slices.Sort(out.Managers)
	out.Seq, out.Hash = s.Seq, s.Hash()
	return out, nil
}

type TeamRef struct {
	ID   string `json:"id"`
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}
type TeamDirectory struct {
	RealmID   string    `json:"realm_id"`
	Teams     []TeamRef `json:"teams"`
	Truncated bool      `json:"truncated"`
}

func (d TeamDirectory) Validate() error {
	if !ValidID(d.RealmID) || len(d.Teams) > MaxTeams {
		return errors.New("team directory: invalid realm or size")
	}
	seen := map[string]bool{}
	for _, t := range d.Teams {
		if !ValidID(t.ID) || t.Seq < 0 || !ValidHash(t.Hash) || seen[t.ID] {
			return errors.New("team directory: invalid or repeated reference")
		}
		seen[t.ID] = true
	}
	return nil
}

type TeamChain struct {
	RealmID string            `json:"realm_id"`
	Team    string            `json:"team"`
	Records []json.RawMessage `json:"records"`
	More    bool              `json:"more"`
}
type TeamSnapshot struct {
	RealmID string      `json:"realm_id"`
	Sources []TeamRef   `json:"sources"`
	Persons []PersonRef `json:"persons"`
	At      int64       `json:"at"`
}
