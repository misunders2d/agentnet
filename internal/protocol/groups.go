package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"slices"

	"github.com/misunders2d/agentnet/internal/identity"
)

const GroupRootVersion = 3
const GroupRootDomain = "agentnet-conv-root-v3\n"
const ConvKindGroup = "group"
const MaxGroupMembers = 256
const MaxGroupRoot = 64 << 10
const MaxGroupHistory = 64
const MaxGroupState = 256 << 10
const MaxGroupCiphertext = 384 << 10
const GroupDomain = "agentnet-group-state-v1\n"
const GroupAdmissionDomain = "agentnet-group-admission-v1\n"
const GroupWithdrawalDomain = "agentnet-group-withdrawal-v1\n"
const GroupCommitDomain = "agentnet-group-commit-v1\n"

func ConvRootVersionLimit(version int) int {
	if version == GroupRootVersion {
		return MaxGroupRoot
	}
	return MaxConvRoot
}
func ConvRootSizeLimit(raw []byte) int {
	var header struct {
		V int `json:"v"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return MaxConvRoot
	}
	return ConvRootVersionLimit(header.V)
}
func ValidateGroupRoot(c ConvRoot) error {
	if c.V != GroupRootVersion || c.Kind != ConvKindGroup || !ValidID(c.Realm) || validLabel(c.Title) != nil || !ValidID(c.Nonce) || c.Created <= 0 {
		return errors.New("group root: invalid version, realm, title or nonce")
	}
	if !ValidID(c.Creator.Person) || !ValidHash(c.Creator.Roster) || !ValidFingerprint(c.Creator.Fingerprint) {
		return errors.New("group root: invalid creator")
	}
	if _, _, err := SplitAddress(c.Creator.Address); err != nil {
		return err
	}
	if len(c.Members) == 0 || len(c.Members) > MaxGroupMembers || len(c.Admins) == 0 || len(c.Admins) > len(c.Members) {
		return errors.New("group root: members and admin required")
	}
	previous := ""
	for _, m := range c.Members {
		if !ValidID(m.Person) || !ValidHash(m.Roster) || m.Person <= previous {
			return errors.New("group root: distinct sorted person members required")
		}
		previous = m.Person
	}
	if roster, ok := c.Member(c.Creator.Person); !ok || roster != c.Creator.Roster {
		return errors.New("group root: creator must be a bound member")
	}
	previous = ""
	for _, p := range c.Admins {
		if _, ok := c.Member(p); !ok || p <= previous {
			return errors.New("group root: distinct sorted member admins required")
		}
		previous = p
	}
	if !slices.Contains(c.Admins, c.Creator.Person) {
		return errors.New("group root: creator must be admin")
	}
	if len(c.Canonical()) > MaxGroupRoot {
		return errors.New("group root: too large")
	}
	return nil
}

// GroupRosterResolver resolves only independently verified pinned chain steps.
type GroupRosterResolver func(person, hash string) (PersonRoster, bool)
type GroupHistoryRef struct {
	LID    string `json:"lid"`
	Author string `json:"author"`
	Hash   string `json:"hash"`
}
type GroupAdmission struct {
	Conv    string            `json:"conv"`
	Realm   string            `json:"realm"`
	Person  string            `json:"person"`
	Roster  string            `json:"roster"`
	Seq     int64             `json:"seq"`
	Prev    string            `json:"prev"`
	History []GroupHistoryRef `json:"history"`
	By      string            `json:"by"`
	Sig     []byte            `json:"sig,omitempty"`
}

func (a GroupAdmission) Canonical() []byte {
	a.Sig = nil
	raw, _ := json.Marshal(a)
	return append([]byte(GroupAdmissionDomain), raw...)
}
func (a GroupAdmission) Hash() string                 { return hashHex(a.Canonical()) }
func (a *GroupAdmission) Sign(key ed25519.PrivateKey) { a.Sig = ed25519.Sign(key, a.Canonical()) }
func (a GroupAdmission) Validate() error {
	if !ValidHash(a.Conv) || !ValidID(a.Realm) || !ValidID(a.Person) || !ValidHash(a.Roster) || !ValidFingerprint(a.By) || a.Seq < 0 || a.Seq == 0 && a.Prev != "" || a.Seq > 0 && !ValidHash(a.Prev) || len(a.History) > MaxGroupHistory {
		return errors.New("group admission: invalid binding")
	}
	refs := map[string]bool{}
	for _, r := range a.History {
		k := r.Author + ":" + r.LID
		if !ValidID(r.LID) || !ValidFingerprint(r.Author) || !ValidHash(r.Hash) || refs[k] {
			return errors.New("group admission: invalid selected history")
		}
		refs[k] = true
	}
	return nil
}
func (a GroupAdmission) Verify(resolve GroupRosterResolver) error {
	if err := a.Validate(); err != nil {
		return err
	}
	r, ok := resolve(a.Person, a.Roster)
	if !ok || r.Person != a.Person || r.Hash() != a.Roster {
		return errors.New("group admission: verified person chain required")
	}
	signer, ok := r.Device(a.By)
	if !ok || !ed25519.Verify(signer.SignKey, a.Canonical(), a.Sig) {
		return errors.New("group admission: explicit member consent invalid")
	}
	return nil
}

type GroupMember struct {
	ConvMember
	Admin     bool           `json:"admin"`
	Admission GroupAdmission `json:"admission"`
}
type GroupState struct {
	V           int           `json:"v"`
	Conv        string        `json:"conv"`
	Realm       string        `json:"realm"`
	Seq         int64         `json:"seq"`
	Prev        string        `json:"prev"`
	Title       string        `json:"title"`
	Members     []GroupMember `json:"members"`
	Actor       string        `json:"actor"`
	ActorRoster string        `json:"actor_roster"`
	By          string        `json:"by"`
	Sig         []byte        `json:"sig,omitempty"`
}

func (s GroupState) Canonical() []byte {
	s.Sig = nil
	raw, _ := json.Marshal(s)
	return append([]byte(GroupDomain), raw...)
}
func (s GroupState) Hash() string                 { return hashHex(s.Canonical()) }
func (s *GroupState) Sign(key ed25519.PrivateKey) { s.Sig = ed25519.Sign(key, s.Canonical()) }
func (s GroupState) Member(person string) (GroupMember, bool) {
	for _, m := range s.Members {
		if m.Person == person {
			return m, true
		}
	}
	return GroupMember{}, false
}
func (s GroupState) Admins() []string {
	out := []string{}
	for _, m := range s.Members {
		if m.Admin {
			out = append(out, m.Person)
		}
	}
	return out
}
func (s GroupState) Validate() error {
	if s.V != 1 || !ValidHash(s.Conv) || !ValidID(s.Realm) || s.Seq < 0 || !ValidID(s.Actor) || !ValidHash(s.ActorRoster) || !ValidFingerprint(s.By) || validLabel(s.Title) != nil {
		return errors.New("group: invalid identity or title")
	}
	if s.Seq == 0 && s.Prev != "" || s.Seq > 0 && !ValidHash(s.Prev) {
		return errors.New("group: invalid chain predecessor")
	}
	if len(s.Members) == 0 || len(s.Members) > MaxGroupMembers || len(s.Admins()) == 0 {
		return errors.New("group: explicit remaining admin required")
	}
	previous := ""
	for _, m := range s.Members {
		if !ValidID(m.Person) || !ValidHash(m.Roster) || m.Person <= previous || m.Admission.Person != m.Person || m.Admission.Roster != m.Roster || m.Admission.Conv != s.Conv || m.Admission.Realm != s.Realm || m.Admission.Seq > s.Seq || m.Admission.Validate() != nil {
			return errors.New("group: invalid member admission")
		}
		previous = m.Person
	}
	if len(s.Canonical()) > MaxGroupState {
		return errors.New("group: state too large")
	}
	return nil
}

// Verify binds state to the original signed root and every pinned person chain.
// Accepted withdrawals are a monotone admission-scoped overlay; they never
// change the admin journal's sequence or grant authority to another person.
func (s GroupState) Verify(root ConvRoot, previous *GroupState, resolve GroupRosterResolver, withdrawals []GroupWithdrawal) error {
	if err := s.verifySigned(root, resolve); err != nil {
		return err
	}
	if previous == nil {
		if s.Seq != 0 || s.Title != root.Title || !slices.Equal(s.Admins(), root.Admins) || len(s.Members) != len(root.Members) {
			return errors.New("group: first state does not match signed root")
		}
		if s.Actor != root.Creator.Person || s.By != root.Creator.Fingerprint || s.ActorRoster != root.Creator.Roster {
			return errors.New("group: first state must be signed by root creator")
		}
		for i, m := range s.Members {
			if m.ConvMember != root.Members[i] || m.Admission.Seq != 0 || m.Admission.Prev != "" {
				return errors.New("group: root membership mismatch")
			}
		}
		return nil
	}
	if s.Conv != previous.Conv || s.Realm != previous.Realm || s.Seq != previous.Seq+1 || s.Prev != previous.Hash() {
		return errors.New("group: stale transition")
	}
	admin, ok := previous.Member(s.Actor)
	if !ok || !admin.Admin {
		return errors.New("group: actor not a previous admin")
	}
	for _, m := range s.Members {
		if m.Admin && s.Withdrawn(m, withdrawals) {
			return errors.New("group: withdrawn admission cannot become admin")
		}
		old, kept := previous.Member(m.Person)
		fresh := !kept || old.Admission.Hash() != m.Admission.Hash()
		if fresh {
			if m.Admission.Seq != s.Seq || m.Admission.Prev != s.Prev {
				return errors.New("group: new admission must consent to this exact transition")
			}
		}
		if kept && fresh && !previous.Withdrawn(old, withdrawals) {
			return errors.New("group: active member admission cannot be replaced silently")
		}
	}
	return nil
}
func (s GroupState) verifySigned(root ConvRoot, resolve GroupRosterResolver) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if root.V != GroupRootVersion || root.ID() != s.Conv || root.Realm != s.Realm {
		return errors.New("group: foreign root")
	}
	creator, ok := resolve(root.Creator.Person, root.Creator.Roster)
	if !ok {
		return errors.New("group: creator chain unavailable")
	}
	device, ok := creator.Device(root.Creator.Fingerprint)
	if !ok || device.Address != root.Creator.Address || root.Verify(device.SignKey) != nil {
		return errors.New("group: original root signature invalid")
	}
	for _, m := range s.Members {
		if err := m.Admission.Verify(resolve); err != nil {
			return err
		}
	}
	actor, ok := resolve(s.Actor, s.ActorRoster)
	if !ok {
		return errors.New("group: actor chain unavailable")
	}
	signer, ok := actor.Device(s.By)
	if !ok || !ed25519.Verify(signer.SignKey, s.Canonical(), s.Sig) {
		return errors.New("group: invalid admin signature")
	}
	return nil
}

// VerifyCurrent verifies a fresh reader's current snapshot against an already
// verified public authority chain. It does not validate hidden prior rosters.
func (s GroupState) VerifyCurrent(root ConvRoot, authority GroupCommit, resolve GroupRosterResolver, slot func(int64) (GroupCommit, bool), withdrawals []GroupWithdrawal) error {
	if err := s.verifySigned(root, resolve); err != nil {
		return err
	}
	if !authority.Matches(s) || authority.Bootstrap != root.Creator.Fingerprint {
		return errors.New("group: current authority mismatch")
	}
	if s.Seq == 0 {
		return s.Verify(root, nil, resolve, withdrawals)
	}
	for _, m := range s.Members {
		record, ok := slot(m.Admission.Seq)
		if !ok || record.Conv != s.Conv || record.Realm != s.Realm || record.Bootstrap != root.Creator.Fingerprint || record.Seq != m.Admission.Seq || record.Prev != m.Admission.Prev {
			return errors.New("group: admission lacks exact verified authority slot")
		}
		if m.Admission.Seq == 0 {
			if roster, ok := root.Member(m.Person); !ok || roster != m.Roster {
				return errors.New("group: initial admission absent from root")
			}
		}
		if m.Admin && s.Withdrawn(m, withdrawals) {
			return errors.New("group: withdrawn admission cannot become admin")
		}
	}
	return nil
}

func (s GroupState) Withdrawn(member GroupMember, withdrawals []GroupWithdrawal) bool {
	for _, w := range withdrawals {
		if w.Conv == s.Conv && w.Realm == s.Realm && w.Person == member.Person && w.Admission == member.Admission.Hash() {
			return true
		}
	}
	return false
}
func (s GroupState) EffectiveMembers(withdrawals []GroupWithdrawal) []GroupMember {
	out := []GroupMember{}
	for _, m := range s.Members {
		if !s.Withdrawn(m, withdrawals) {
			out = append(out, m)
		}
	}
	return out
}

// GroupWithdrawal authorizes only departure of the signing person's exact
// ordinary membership. Admin departures use CAS and explicit transfer instead.
type GroupWithdrawal struct {
	Conv      string `json:"conv"`
	Realm     string `json:"realm"`
	Person    string `json:"person"`
	Admission string `json:"admission"`
	Roster    string `json:"roster"`
	By        string `json:"by"`
	Sig       []byte `json:"sig,omitempty"`
}

func (w GroupWithdrawal) Canonical() []byte {
	w.Sig = nil
	raw, _ := json.Marshal(w)
	return append([]byte(GroupWithdrawalDomain), raw...)
}
func (w *GroupWithdrawal) Sign(key ed25519.PrivateKey) { w.Sig = ed25519.Sign(key, w.Canonical()) }
func (w GroupWithdrawal) Verify(state GroupState, resolve GroupRosterResolver) error {
	if w.Conv != state.Conv || w.Realm != state.Realm || !ValidHash(w.Admission) || !ValidID(w.Person) || !ValidHash(w.Roster) || !ValidFingerprint(w.By) {
		return errors.New("group withdrawal: invalid admission binding")
	}
	member, ok := state.Member(w.Person)
	if !ok || member.Admin || member.Admission.Hash() != w.Admission {
		return errors.New("group withdrawal: only own ordinary admission may leave")
	}
	roster, ok := resolve(w.Person, w.Roster)
	if !ok || roster.Person != w.Person || roster.Hash() != w.Roster {
		return errors.New("group withdrawal: verified own person required")
	}
	signer, ok := roster.Device(w.By)
	if !ok || !ed25519.Verify(signer.SignKey, w.Canonical(), w.Sig) {
		return errors.New("group withdrawal: invalid self signature")
	}
	return nil
}

// Public header is bound to exact ciphertext and encrypted state's hash/admins.
type GroupCommit struct {
	Bootstrap   string   `json:"bootstrap"`
	V           int      `json:"v"`
	Conv        string   `json:"conv"`
	Realm       string   `json:"realm"`
	Seq         int64    `json:"seq"`
	Prev        string   `json:"prev"`
	Hash        string   `json:"hash"`
	Admins      []string `json:"admins"`
	Writer      string   `json:"writer"`
	Actor       string   `json:"actor"`
	ActorRoster string   `json:"actor_roster"`
	Ciphertext  []byte   `json:"ciphertext"`
	Sig         []byte   `json:"sig,omitempty"`
}

func (c GroupCommit) Canonical() []byte {
	c.Sig = nil
	raw, _ := json.Marshal(c)
	return append([]byte(GroupCommitDomain), raw...)
}
func (c *GroupCommit) Sign(key ed25519.PrivateKey) { c.Sig = ed25519.Sign(key, c.Canonical()) }
func (c GroupCommit) Validate() error {
	if !ValidFingerprint(c.Bootstrap) || c.V != 1 || !ValidID(c.Actor) || !ValidHash(c.ActorRoster) || !ValidHash(c.Conv) || !ValidID(c.Realm) || !ValidHash(c.Hash) || c.Seq < 0 || c.Seq == 0 && c.Prev != "" || c.Seq > 0 && !ValidHash(c.Prev) || len(c.Admins) == 0 || len(c.Admins) > MaxGroupMembers || len(c.Ciphertext) == 0 || len(c.Ciphertext) > MaxGroupCiphertext {
		return errors.New("group commit: invalid identity or bounds")
	}
	if _, _, err := SplitAddress(c.Writer); err != nil {
		return err
	}
	previous := ""
	for _, p := range c.Admins {
		if !ValidID(p) || p <= previous {
			return errors.New("group commit: distinct sorted admin persons required")
		}
		previous = p
	}
	return nil
}
func (c GroupCommit) Verify(writer identity.Public) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if writer.Address != c.Writer || writer.Verify() != nil || !ed25519.Verify(writer.SignKey, c.Canonical(), c.Sig) {
		return errors.New("group commit: invalid writer signature")
	}
	return nil
}
func (c GroupCommit) Matches(s GroupState) bool {
	return c.Conv == s.Conv && c.Realm == s.Realm && c.Seq == s.Seq && c.Prev == s.Prev && c.Hash == s.Hash() && slices.Equal(c.Admins, s.Admins()) && c.Actor == s.Actor && c.ActorRoster == s.ActorRoster
}

// VerifyChain proves public append authority independently of the relay.
// Current ciphertext must still decrypt to a complete state matching header.
func (c GroupCommit) VerifyChain(root ConvRoot, previous *GroupCommit, resolve GroupRosterResolver) error {
	roster, ok := resolve(c.Actor, c.ActorRoster)
	if !ok {
		return errors.New("group commit: actor chain unavailable")
	}
	var writer identity.Public
	found := false
	for _, d := range roster.Devices {
		if d.Address == c.Writer {
			writer = d
			found = true
		}
	}
	if !found {
		return errors.New("group commit: writer absent from actor roster")
	}
	if err := c.Verify(writer); err != nil {
		return err
	}
	if c.Bootstrap != root.Creator.Fingerprint || c.Conv != root.ID() || c.Realm != root.Realm {
		return errors.New("group commit: foreign root")
	}
	if previous == nil {
		if c.Seq != 0 || c.Actor != root.Creator.Person || c.ActorRoster != root.Creator.Roster || c.Writer != root.Creator.Address || writer.Fingerprint() != root.Creator.Fingerprint || !slices.Equal(c.Admins, root.Admins) || root.Verify(writer.SignKey) != nil {
			return errors.New("group commit: root authority mismatch")
		}
	} else if c.Seq != previous.Seq+1 || c.Prev != previous.Hash || c.Conv != previous.Conv || c.Bootstrap != previous.Bootstrap || c.Realm != previous.Realm || !slices.Contains(previous.Admins, c.Actor) {
		return errors.New("group commit: stale or unauthorized append")
	}
	return nil
}

type GroupJournalResult struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
	Same bool   `json:"same"`
}
type GroupJournalPage struct {
	Records []GroupCommit `json:"records"`
	More    bool          `json:"more"`
}
type GroupHead struct {
	Bootstrap string `json:"bootstrap"`
	Conv      string `json:"conv"`
	Seq       int64  `json:"seq"`
	Hash      string `json:"hash"`
}

// AllowsHistory is an exact signed selected-history grant. Sequence numbers,
// titles, audience membership and a matching LID alone grant no old messages.
func (a GroupAdmission) AllowsHistory(ref GroupHistoryRef) bool {
	return slices.Contains(a.History, ref)
}
