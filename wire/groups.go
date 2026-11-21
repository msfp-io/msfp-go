package wire

import (
	"cmp"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Groups (MSFP groups part): events, the state they give, and mail.
// Nothing here stores or calls anything: the engine does.

const (
	groupEventLabel = "msfp-group-event-1\n"
	groupMailLabel  = "msfp-group-mail-1\n"
	groupMailInfo   = "msfp-group-mail-1"

	// MaxGroupEvent and MaxGroupMail are a payload's size at most (groups
	// sections 3.1 and 5).
	MaxGroupEvent = 4096
	MaxGroupMail  = 2048
	// MaxGroupReason is a ban's reason at most, in code points.
	MaxGroupReason = 280
	// MailLifetime is how long a mail is carried at most.
	MailLifetime = 30 * 24 * time.Hour
	// HeldFor is how long an event waits for its predecessors.
	HeldFor = 30 * 24 * time.Hour
)

// The event types (groups section 3.3).
const (
	EventFounded   = "founded"
	EventProfile   = "profile"
	EventJoined    = "joined"
	EventAdmin     = "admin"
	EventHeir      = "heir"
	EventOwner     = "owner"
	EventSettings  = "settings"
	EventBanned    = "banned"
	EventUnbanned  = "unbanned"
	EventLeft      = "left"
	EventDissolved = "dissolved"
	EventRotated   = "rotated"
	EventRevoked   = "revoked"
)

// Envelope is an event or a mail as it travels: the payload's bytes in
// base64url, and the signature.
type Envelope struct {
	Payload   string `json:"event,omitempty"`
	Mail      string `json:"mail,omitempty"`
	Signature string `json:"signature"`
}

// EventSettings are a group's settings.
type GroupSettings struct {
	WaitDays  int64 `json:"waitDays"`
	CapPerDay int64 `json:"capPerDay"`
}

// DefaultGroupSettings are a group's settings by default: 3 days before
// automatic acceptance, 5 invitations a day per inviter.
var DefaultGroupSettings = GroupSettings{WaitDays: 3, CapPerDay: 5}

// UnmarshalJSON reads settings, an absent member taking its default
// (groups section 3.3: 3 days, 5 a day). Members are matched by their
// exact names, as every MSFP object; a value that isn't an integer fails.
func (g *GroupSettings) UnmarshalJSON(b []byte) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	out := DefaultGroupSettings
	for name, field := range map[string]*int64{"waitDays": &out.WaitDays, "capPerDay": &out.CapPerDay} {
		if raw, ok := obj[name]; ok {
			if err := json.Unmarshal(raw, field); err != nil {
				return err
			}
		}
	}
	*g = out
	return nil
}

// Event is an event's payload, read.
type Event struct {
	Group  string `json:"group,omitempty"`
	Type   string `json:"type"`
	Signer string `json:"signer"`
	Member string `json:"member"`
	Seq    int64  `json:"seq"`
	Date   int64  `json:"date"`

	OwnerSeq int64 `json:"ownerSeq,omitempty"`
	AsOf     int64 `json:"asOf,omitempty"`

	Name           string          `json:"name,omitempty"`
	Settings       *GroupSettings  `json:"settings,omitempty"`
	Nonce          string          `json:"nonce,omitempty"`
	EncryptionKey  string          `json:"encryptionKey,omitempty"`
	NextCommitment string          `json:"nextCommitment,omitempty"`
	NewMember      string          `json:"newMember,omitempty"`
	Target         string          `json:"target,omitempty"`
	On             *bool           `json:"on,omitempty"`
	LastSeq        *int64          `json:"lastSeq,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Ban            string          `json:"ban,omitempty"`
	KeepPairings   *bool           `json:"keepPairings,omitempty"`
	Statement      json.RawMessage `json:"statement,omitempty"`
}

// SignEvent signs an event's payload with key, and gives its envelope and
// ID (SHA-256 of the payload, base64url).
func SignEvent(key Key, ev Event) (Envelope, string, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return Envelope{}, "", err
	}
	if len(b) > MaxGroupEvent {
		return Envelope{}, "", errors.New("msfp: an event over 4,096 bytes")
	}
	sig := key.sign(append([]byte(groupEventLabel), b...))
	id := sha256.Sum256(b)
	return Envelope{Payload: Encode64(b), Signature: Encode64(sig)}, Encode64(id[:]), nil
}

// readEnvelope checks an event's envelope: the payload, its signature
// under its signer, and gives the event, its bytes and its ID.
func readEnvelope(e Envelope) (Event, []byte, string, error) {
	b, ok := Decode64(e.Payload, -1)
	if !ok || len(b) == 0 || len(b) > MaxGroupEvent {
		return Event{}, nil, "", errors.New("not a payload of at most 4,096 bytes")
	}
	var ev Event
	if err := DecodeJSON(b, &ev); err != nil {
		return Event{}, nil, "", errors.New("not an event")
	}
	key, ok := Decode64(ev.Signer, 32)
	sig, ok2 := Decode64(e.Signature, 64)
	if !ok || !ok2 || !ValidKey(key) || !verify(key, append([]byte(groupEventLabel), b...), sig) {
		return Event{}, nil, "", errors.New("the signature doesn't verify under signer")
	}
	id := sha256.Sum256(b)
	return ev, b, Encode64(id[:]), nil
}

// ---- the state (groups section 3.5) -----------------------------------------

// GroupMember is a member as the state knows it.
type GroupMember struct {
	ID        string // its member ID: the key it joined with, base64url
	Key       string // its current key
	Name      string
	Admin     bool
	InvitedBy string
	Joined    int64  // its joined event's date (the founder: founded's)
	joinedID  string // its joined event's ID (the founder: founded's), for "oldest"
	// EncryptionKey and NextCommitment, from its latest profile.
	EncryptionKey  []byte
	NextCommitment []byte
	adminAt        int64 // the ownerSeq that named it admin, for "oldest"
}

// GroupState is what a copy's events give.
type GroupState struct {
	ID       string
	Name     string
	Settings GroupSettings
	Owner    string // member ID
	Heir     string
	Members  map[string]*GroupMember
	Banned   map[string]string // member ID -> the banned event's ID
	Reasons  map[string]string // member ID -> its ban's reason
	// WasBanned is a banned server as it was a member, for the panels.
	WasBanned map[string]GroupMember
	Excluded  []string // equivocation
	Dissolved bool
	Keep      bool // dissolved with keepPairings
	OwnerSeq  int64

	seq        map[string]int64            // member -> last kept seq
	payloads   map[string]map[int64]string // member -> seq -> payload
	adminLog   []adminChange
	removedAt  map[string]int64     // admin -> the last seq that counts
	bannedAt   map[string]int64     // banned -> the last seq that counts
	revoked    map[string]keyFrom   // member -> the key it revoked, and its last good seq
	keys       map[string][]keyFrom // member -> keys in force from a seq
	capRank    map[string]int       // joined event ID -> its rank in its inviter's UTC day
	everMember map[string]bool      // members that were named by a counted founded or joined
	highest    map[string]int64     // member -> its highest seq kept, whatever its verdict
	ids        map[string]bool      // event IDs replayed
}

type adminChange struct {
	ownerSeq int64
	target   string
	on       bool
}

type keyFrom struct {
	seq int64
	key string
}

// NewGroupState is an empty copy, before its founded event.
func NewGroupState() *GroupState {
	return &GroupState{Members: map[string]*GroupMember{}, Banned: map[string]string{}, Reasons: map[string]string{}, WasBanned: map[string]GroupMember{},
		Settings: DefaultGroupSettings, seq: map[string]int64{}, payloads: map[string]map[int64]string{},
		removedAt: map[string]int64{}, bannedAt: map[string]int64{}, revoked: map[string]keyFrom{},
		keys: map[string][]keyFrom{}, capRank: map[string]int{}, everMember: map[string]bool{}, highest: map[string]int64{}, ids: map[string]bool{}}
}

// Seq is a member's last kept seq.
func (s *GroupState) Seq(member string) int64 { return s.seq[member] }

// Highest is a member's highest seq kept, counted or not: the member signs
// its next event with the one after it, never twice the same.
func (s *GroupState) Highest(member string) int64 { return max(s.highest[member], s.seq[member]) }

// Have is every member's last kept seq, for group-sync.
func (s *GroupState) Have() map[string]int64 { return maps2(s.seq) }

func maps2(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// IsAdmin says whether a member is the owner or an admin now.
func (s *GroupState) IsAdmin(member string) bool {
	m := s.Members[member]
	return member == s.Owner || (m != nil && m.Admin)
}

func (s *GroupState) adminAsOf(member string, k int64) bool {
	on := false
	for _, c := range s.adminLog {
		if c.target == member && c.ownerSeq <= k {
			on = c.on
		}
	}
	return on
}

// memberKey says whether key is one of a member's keys: the one it joined
// with, or one it rotated to.
func (s *GroupState) memberKey(member, key string) bool {
	if key == member {
		return true
	}
	return slices.ContainsFunc(s.keys[member], func(f keyFrom) bool { return f.key == key })
}

func (s *GroupState) keyAt(member string, seq int64) string {
	k := member
	for _, f := range s.keys[member] {
		if f.seq <= seq {
			k = f.key
		}
	}
	return k
}

// ErrHeld is an event that waits: its predecessors, or the owner event its
// asOf names, haven't arrived.
var ErrHeld = errors.New("msfp: an event waiting for earlier ones")

// ErrKept is an event this copy has already.
var ErrKept = errors.New("msfp: an event already kept")

// Verdict is what the state makes of an event a member keeps (groups
// sections 3.2 and 3.5).
type Verdict int

const (
	// Counted: it counts in the state.
	Counted Verdict = iota
	// Refused: its rules don't hold. It is kept and passed on all the
	// same, changing nothing: with events that arrive later, it may count.
	Refused
	// Pending: what it needs isn't there yet: its member's earlier
	// events, the owner event it names, its member's joined. Kept, and
	// dropped after HeldFor.
	Pending
	// Forged: not signed by its member's key in force. Not kept.
	Forged
)

// EventResult is one event's verdict, with why for Refused and Forged.
type EventResult struct {
	Event   Event
	Verdict Verdict
	Reason  string
}

// CheckEvent reads an envelope as a member keeps it (groups section 3.2):
// well formed, signed under its signer, of the group (founded: no group,
// and its ID is group), dated at most 5 minutes after now. Whether it
// counts is ComputeGroup's.
func CheckEvent(e Envelope, group string, now time.Time) (Event, string, error) {
	ev, _, id, err := readEnvelope(e)
	if err != nil {
		return ev, id, err
	}
	if ev.Seq < 1 || ev.Date < 1 || ev.Type == "" || ev.Member == "" {
		return ev, id, errors.New("a common member is missing")
	}
	if ev.Date > now.Unix()+300 {
		return ev, id, errors.New("dated more than 5 minutes in the future")
	}
	if ev.Type == EventFounded {
		if id != group {
			return ev, id, errors.New("another group's founded event")
		}
	} else if ev.Group != group {
		return ev, id, errors.New("another group's event")
	}
	return ev, id, nil
}

// kept is an event in a replay.
type kept struct {
	ev   Event
	b    []byte
	id   string
	done bool
}

// cuts are what bans, removals of admins and revocations take away from
// a member's events after a seq: applied from the start of a replay, so
// that events a cut covers never count, whenever the cutting event comes.
type cuts struct {
	banned  map[string]int64
	removed map[string]int64
	revoked map[string]keyFrom
}

func (c cuts) equal(d cuts) bool {
	return maps.Equal(c.banned, d.banned) && maps.Equal(c.removed, d.removed) && maps.Equal(c.revoked, d.revoked)
}

// ComputeGroup is the state a set of kept events gives (groups section
// 3.5), the same whatever order they arrived in, with each event's
// verdict by its ID. group is the group's ID (its founded event's).
// Envelopes that don't read are left out (CheckEvent says why).
//
// The events are replayed in one order: at each step, the ready event
// with the lowest ID. An event is ready once its member's earlier events,
// the owner event it follows or names, and its member's joined have been
// replayed. Bans, removals of admins and revocations take away events of
// their target after a seq whenever they come: the replay starts again
// with them applied from the start, until they no longer change.
func ComputeGroup(group string, envs []Envelope) (*GroupState, map[string]EventResult) {
	var events []*kept
	seen := map[string]bool{}
	for _, e := range envs {
		ev, b, id, err := readEnvelope(e)
		if err != nil || seen[id] || (ev.Type == EventFounded && id != group) || (ev.Type != EventFounded && ev.Group != group) {
			continue
		}
		seen[id] = true
		events = append(events, &kept{ev: ev, b: b, id: id})
	}
	slices.SortFunc(events, func(a, b *kept) int { return strings.Compare(a.id, b.id) })
	// The daily cap: each inviter's joined events of a UTC day, in date
	// then ID order (section 3.5).
	capRank := map[string]int{}
	byDay := map[string][]*kept{}
	for _, k := range events {
		if k.ev.Type == EventJoined {
			day := k.ev.Member + " " + time.Unix(k.ev.Date, 0).UTC().Format(time.DateOnly)
			byDay[day] = append(byDay[day], k)
		}
	}
	for _, list := range byDay {
		slices.SortStableFunc(list, func(a, b *kept) int { return cmp.Compare(a.ev.Date, b.ev.Date) })
		for i, k := range list {
			capRank[k.id] = i
		}
	}
	var c cuts
	var s *GroupState
	var results map[string]EventResult
	for range 8 {
		var next cuts
		s, results, next = replay(events, c, capRank)
		if next.equal(c) {
			break
		}
		c = next
	}
	return s, results
}

// replay replays the events once, with the cuts applied from the start,
// and gives the cuts its counted events make.
func replay(events []*kept, c cuts, capRank map[string]int) (*GroupState, map[string]EventResult, cuts) {
	s := NewGroupState()
	s.capRank = capRank
	maps.Copy(s.bannedAt, c.banned)
	maps.Copy(s.removedAt, c.removed)
	maps.Copy(s.revoked, c.revoked)
	next := cuts{banned: map[string]int64{}, removed: map[string]int64{}, revoked: map[string]keyFrom{}}
	bySeq := map[string][]*kept{}
	for _, k := range events {
		k.done = false
		key := k.ev.Member + " " + strconv.FormatInt(k.ev.Seq, 10)
		bySeq[key] = append(bySeq[key], k)
	}
	results := map[string]EventResult{}
	for final := false; ; {
		progressed := false
		for _, k := range events {
			if k.done {
				continue
			}
			v, reason := s.step(k, bySeq, final)
			if v == Pending || v == failing {
				continue
			}
			final = false
			k.done, progressed = true, true
			results[k.id] = EventResult{Event: k.ev, Verdict: v, Reason: reason}
			if v == Counted {
				switch ev := k.ev; ev.Type {
				case EventBanned:
					if old, ok := next.banned[ev.Target]; !ok || *ev.LastSeq < old {
						next.banned[ev.Target] = *ev.LastSeq
					}
				case EventAdmin:
					if !*ev.On && ev.LastSeq != nil {
						if old, ok := next.removed[ev.Target]; !ok || *ev.LastSeq < old {
							next.removed[ev.Target] = *ev.LastSeq
						}
					}
				case EventRevoked:
					next.revoked[ev.Member] = s.revoked[ev.Member]
				}
			}
			break // again from the lowest ID
		}
		if !progressed {
			if final {
				break
			}
			// No ready event can count: the lowest ready one is refused.
			final = true
		}
	}
	for _, k := range events {
		if !k.done {
			results[k.id] = EventResult{Event: k.ev, Verdict: Pending, Reason: "waiting for events it needs"}
		}
		if results[k.id].Verdict != Forged && k.ev.Seq > s.highest[k.ev.Member] {
			s.highest[k.ev.Member] = k.ev.Seq
		}
	}
	return s, results, next
}

// signedByMember says whether an event is its member's: signed with the
// member's key in force at its seq, or, for rotated, a statement from that
// key to the signer that verifies.
func (s *GroupState) signedByMember(ev Event) bool {
	cur := s.keyAt(ev.Member, ev.Seq)
	if ev.Type != EventRotated {
		return ev.Signer == cur
	}
	var st struct {
		OldKey, NewKey, NextCommitment string
		Date                           int64
		OldSignature, NewSignature     string
	}
	if json.Unmarshal(ev.Statement, &st) != nil || st.OldKey != cur || st.NewKey != ev.Signer {
		return false
	}
	old, _ := Decode64(st.OldKey, 32)
	nu, _ := Decode64(st.NewKey, 32)
	var commitment []byte
	if st.NextCommitment != "" {
		commitment, _ = Decode64(st.NextCommitment, 32)
	}
	os, ok1 := Decode64(st.OldSignature, 64)
	ns, ok2 := Decode64(st.NewSignature, 64)
	msg := rotationStatement(old, nu, commitment, st.Date)
	return ok1 && ok2 && ValidKey(nu) && verify(old, msg, os) && verify(nu, msg, ns)
}

// failing is an event that is ready but whose rules don't hold now: it
// waits while other events can count, and is refused only when none can.
const failing Verdict = -1

// step replays one event: Pending when it isn't ready yet; failing when
// its rules don't hold now (with final, Refused instead).
func (s *GroupState) step(k *kept, bySeq map[string][]*kept, final bool) (Verdict, string) {
	ev, id := k.ev, k.id
	if ev.Type == EventFounded {
		if s.ID != "" {
			return Refused, "a second founded event"
		}
		if ev.Seq != 1 || ev.Member != ev.Signer || ev.OwnerSeq != 1 || ev.Group != "" {
			return Forged, "founded: seq 1, ownerSeq 1, by the founder's own key"
		}
		s.ID, s.Owner, s.OwnerSeq = id, ev.Member, 1
		s.Name = CleanText(ev.Name, MaxGroupName)
		s.applySettings(ev.Settings)
		s.Members[ev.Member] = &GroupMember{ID: ev.Member, Key: ev.Member, Joined: ev.Date, joinedID: id}
		s.everMember[ev.Member] = true
		s.keep(ev, k.b, id)
		return Counted, ""
	}
	if s.ID == "" {
		return Pending, ""
	}
	if ev.Seq > s.seq[ev.Member]+1 {
		return Pending, ""
	}
	if !s.signedByMember(ev) {
		return Forged, "not signed by its member's key in force at its seq"
	}
	if ev.Seq <= s.seq[ev.Member] {
		// Its seq was replayed: another payload of the member's with
		// that seq, met below.
		return Refused, "equivocation: two events with one seq"
	}
	if !s.everMember[ev.Member] {
		return Pending, "" // its joined hasn't counted yet
	}
	refuse := func(reason string) (Verdict, string) {
		s.keep(ev, k.b, id)
		return Refused, reason
	}
	// What may hold later: the event waits while others can count.
	fail := func(reason string) (Verdict, string) {
		if final {
			return refuse(reason)
		}
		return failing, reason
	}
	// Equivocation (section 3.2): another payload of the member's own
	// with this seq: both are dropped, the member excluded.
	for _, other := range bySeq[ev.Member+" "+strconv.FormatInt(ev.Seq, 10)] {
		if other.id != id && s.signedByMember(other.ev) {
			if !slices.Contains(s.Excluded, ev.Member) {
				s.Excluded = append(s.Excluded, ev.Member)
				s.remove(ev.Member)
			}
			return refuse("equivocation: two events with one seq")
		}
	}
	if s.Dissolved {
		return refuse("the group is dissolved")
	}
	if slices.Contains(s.Excluded, ev.Member) {
		return refuse("its member was excluded")
	}
	if s.Members[ev.Member] == nil && ev.Type != EventLeft {
		return fail("its signer isn't a member")
	}
	if last, ok := s.removedAt[ev.Member]; ok && ev.Seq > last && isAdminEvent(ev.Type) {
		return refuse("signed after its signer's role was removed")
	}
	if last, ok := s.bannedAt[ev.Member]; ok && ev.Seq > last {
		return refuse("signed after its signer was banned")
	}
	if r, ok := s.revoked[ev.Member]; ok && ev.Signer == r.key && ev.Seq > r.seq && ev.Type != EventRevoked {
		return refuse("signed with a revoked key after its last good event")
	}
	owner := ev.Member == s.Owner
	if owner && isOwnerEvent(ev.Type) {
		if ev.OwnerSeq > s.OwnerSeq+1 {
			return Pending, ""
		}
		if ev.OwnerSeq != s.OwnerSeq+1 {
			return refuse("an owner event whose ownerSeq was taken")
		}
	}
	if !owner && ev.AsOf > s.OwnerSeq {
		return Pending, ""
	}
	// A ban and the removal of an admin wait for their target's events
	// up to their lastSeq: those count (section 3.5).
	if (ev.Type == EventBanned || (ev.Type == EventAdmin && ev.On != nil && !*ev.On)) && ev.LastSeq != nil && s.seq[ev.Target] < *ev.LastSeq {
		return Pending, ""
	}
	admin := !owner && ev.AsOf > 0 && ev.AsOf <= s.OwnerSeq && s.adminAsOf(ev.Member, ev.AsOf)
	if err := s.applyType(ev, id, owner, admin); err != nil {
		return fail(err.Error())
	}
	if owner && isOwnerEvent(ev.Type) {
		s.OwnerSeq = ev.OwnerSeq
	}
	s.keep(ev, k.b, id)
	return Counted, ""
}

func isOwnerEvent(t string) bool {
	switch t {
	case EventProfile, EventRotated, EventRevoked, EventLeft:
		return false
	}
	return true
}

func isAdminEvent(t string) bool { return t == EventJoined || t == EventBanned }

func (s *GroupState) keep(ev Event, b []byte, id string) {
	s.seq[ev.Member] = ev.Seq
	if s.payloads[ev.Member] == nil {
		s.payloads[ev.Member] = map[int64]string{}
	}
	s.payloads[ev.Member][ev.Seq] = string(b)
	s.ids[id] = true
}

func (s *GroupState) remove(member string) {
	delete(s.Members, member)
	if s.Heir == member {
		s.Heir = ""
	}
}

func (s *GroupState) applySettings(st *GroupSettings) {
	if st == nil {
		return
	}
	if st.WaitDays >= 0 && st.WaitDays <= 30 {
		s.Settings.WaitDays = st.WaitDays
	}
	if st.CapPerDay >= 1 && st.CapPerDay <= 100 {
		s.Settings.CapPerDay = st.CapPerDay
	}
}

func (s *GroupState) applyType(ev Event, id string, owner, admin bool) error {
	member := func(m string) bool { _, ok := s.Members[m]; return ok }
	switch ev.Type {
	case EventProfile:
		m := s.Members[ev.Member]
		m.Name = CleanText(ev.Name, MaxGroupName)
		if k, ok := Decode64(ev.EncryptionKey, 32); ok {
			m.EncryptionKey = k
		}
		if c, ok := Decode64(ev.NextCommitment, 32); ok {
			m.NextCommitment = c
		}
	case EventJoined:
		if !owner && !admin {
			return errors.New("joined by a member without the right to invite")
		}
		if k, ok := Decode64(ev.NewMember, 32); !ok || !ValidKey(k) {
			return errors.New("newMember isn't a key")
		}
		if _, banned := s.Banned[ev.NewMember]; banned || slices.Contains(s.Excluded, ev.NewMember) {
			return errors.New("the newcomer is banned, or excluded")
		}
		if member(ev.NewMember) {
			return errors.New("the newcomer is a member already")
		}
		// The inviter's joined events of a UTC day beyond the cap, by
		// date then ID, don't count (section 3.5).
		if int64(s.capRank[id]) >= s.Settings.CapPerDay {
			return errors.New("over the inviter's daily cap")
		}
		s.everMember[ev.NewMember] = true
		s.Members[ev.NewMember] = &GroupMember{ID: ev.NewMember, Key: ev.NewMember, Name: CleanText(ev.Name, MaxGroupName),
			InvitedBy: ev.Member, Joined: ev.Date, joinedID: id}
		// A member that left and comes back keeps counting from its seq.
		delete(s.removedAt, ev.NewMember)
	case EventAdmin:
		if !owner {
			return errors.New("admin events are the owner's")
		}
		if !member(ev.Target) || ev.On == nil {
			return errors.New("the target isn't a member")
		}
		s.adminLog = append(s.adminLog, adminChange{ev.OwnerSeq, ev.Target, *ev.On})
		m := s.Members[ev.Target]
		m.Admin = *ev.On
		if *ev.On {
			m.adminAt = ev.OwnerSeq
			delete(s.removedAt, ev.Target)
		} else if ev.LastSeq != nil {
			s.removedAt[ev.Target] = *ev.LastSeq
		}
	case EventHeir:
		if !owner {
			return errors.New("heir events are the owner's")
		}
		if ev.Target != "" && !member(ev.Target) {
			return errors.New("the target isn't a member")
		}
		s.Heir = ev.Target
	case EventOwner:
		if !owner {
			return errors.New("owner events are the owner's")
		}
		if !member(ev.Target) {
			return errors.New("the target isn't a member")
		}
		s.Owner = ev.Target
		if s.Heir == ev.Target {
			s.Heir = ""
		}
	case EventSettings:
		if !owner {
			return errors.New("settings are the owner's")
		}
		if n := CleanText(ev.Name, MaxGroupName); n != "" {
			s.Name = n
		}
		s.applySettings(ev.Settings)
	case EventBanned:
		if !owner && !admin {
			return errors.New("banned by a member without the right to ban")
		}
		if ev.Target == s.Owner {
			return errors.New("the owner can't be banned")
		}
		if !owner && (s.adminAsOf(ev.Target, ev.AsOf) || s.Members[ev.Target] != nil && s.Members[ev.Target].Admin) {
			return errors.New("an admin can't ban an admin")
		}
		if !member(ev.Target) || ev.LastSeq == nil {
			return errors.New("the target isn't a member")
		}
		if utf8.RuneCountInString(ev.Reason) > MaxGroupReason {
			return errors.New("a reason over 280 code points")
		}
		s.Banned[ev.Target] = id
		s.Reasons[ev.Target] = CleanText(ev.Reason, MaxGroupReason)
		s.bannedAt[ev.Target] = *ev.LastSeq
		s.WasBanned[ev.Target] = *s.Members[ev.Target]
		s.remove(ev.Target)
	case EventUnbanned:
		if !owner {
			return errors.New("unbanned is the owner's")
		}
		for m, ban := range s.Banned {
			if ban == ev.Ban {
				delete(s.Banned, m)
				delete(s.Reasons, m)
				delete(s.WasBanned, m)
				return nil
			}
		}
		return errors.New("no such ban")
	case EventLeft:
		if owner {
			return errors.New("the owner gives the group away before leaving")
		}
		if !member(ev.Member) {
			return errors.New("not a member")
		}
		s.remove(ev.Member)
	case EventDissolved:
		if !owner {
			return errors.New("dissolved is the owner's")
		}
		s.Dissolved = true
		s.Keep = ev.KeepPairings != nil && *ev.KeepPairings
	case EventRotated:
		var st struct {
			OldKey, NewKey, NextCommitment string
			Date                           int64
			OldSignature, NewSignature     string
		}
		if json.Unmarshal(ev.Statement, &st) != nil {
			return errors.New("not a rotation statement")
		}
		cur := s.keyAt(ev.Member, ev.Seq)
		old, _ := Decode64(st.OldKey, 32)
		nu, _ := Decode64(st.NewKey, 32)
		if st.OldKey != cur || st.NewKey != ev.Signer {
			return errors.New("the rotation isn't from the member's key to the signer")
		}
		var commitment []byte
		if st.NextCommitment != "" {
			commitment, _ = Decode64(st.NextCommitment, 32)
		}
		os, ok1 := Decode64(st.OldSignature, 64)
		ns, ok2 := Decode64(st.NewSignature, 64)
		msg := rotationStatement(old, nu, commitment, st.Date)
		if !ok1 || !ok2 || !ValidKey(nu) || !verify(old, msg, os) || !verify(nu, msg, ns) {
			return errors.New("the rotation statement doesn't verify")
		}
		if m := s.Members[ev.Member]; m != nil && m.NextCommitment != nil {
			h := sha256.Sum256(nu)
			if !slices.Equal(h[:], m.NextCommitment) {
				return errors.New("the new key isn't the committed one")
			}
			m.NextCommitment = commitment
		}
		s.keys[ev.Member] = append(s.keys[ev.Member], keyFrom{ev.Seq, st.NewKey})
		if m := s.Members[ev.Member]; m != nil {
			m.Key = st.NewKey
		}
	case EventRevoked:
		var st struct {
			Key       string
			Date      int64
			Signature string
		}
		if json.Unmarshal(ev.Statement, &st) != nil || ev.LastSeq == nil {
			return errors.New("not a revocation statement")
		}
		k, _ := Decode64(st.Key, 32)
		sig, ok := Decode64(st.Signature, 64)
		if !ok || !verify(k, revocationStatement(k, st.Date), sig) {
			return errors.New("the revocation statement doesn't verify")
		}
		// Only one of the member's own keys, its current one or a former
		// one (groups section 3.3).
		if !s.memberKey(ev.Member, st.Key) {
			return errors.New("the revoked key isn't one of the member's keys")
		}
		if r, ok := s.revoked[ev.Member]; !ok || *ev.LastSeq < r.seq {
			s.revoked[ev.Member] = keyFrom{seq: *ev.LastSeq, key: st.Key}
		}
	default:
		// A type of a later version: kept and passed on, changing nothing.
	}
	return nil
}

// Successor is who an owner stepping down names (groups section 3.3): its
// heir if still a member, else the oldest admin, else the oldest member;
// "" when the owner is alone.
func (s *GroupState) Successor() string {
	if s.Heir != "" && s.Members[s.Heir] != nil {
		return s.Heir
	}
	var admins, others []*GroupMember
	for _, m := range s.Members {
		if m.ID == s.Owner {
			continue
		}
		if m.Admin {
			admins = append(admins, m)
		} else {
			others = append(others, m)
		}
	}
	sort.Slice(admins, func(i, j int) bool {
		if admins[i].adminAt != admins[j].adminAt {
			return admins[i].adminAt < admins[j].adminAt
		}
		return admins[i].ID < admins[j].ID
	})
	sort.Slice(others, func(i, j int) bool {
		// The oldest: the lowest joined date, then the lowest event ID
		// (groups section 3.3).
		if others[i].Joined != others[j].Joined {
			return others[i].Joined < others[j].Joined
		}
		return others[i].joinedID < others[j].joinedID
	})
	if len(admins) > 0 {
		return admins[0].ID
	}
	if len(others) > 0 {
		return others[0].ID
	}
	return ""
}

// ---- mail (groups section 5) ---------------------------------------------------

// Mail is a mail's payload, read.
type Mail struct {
	Group   string `json:"group"`
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	From    string `json:"from"`
	To      string `json:"to"`
	Answers string `json:"answers,omitempty"`
	Expires int64  `json:"expires"`
	Sealed  string `json:"sealed"`
}

// Sealed is what a mail carries, once opened.
type Sealed struct {
	Address        string `json:"address"`
	Name           string `json:"name,omitempty"`
	NextCommitment string `json:"nextCommitment"`
	Inbound        bool   `json:"inbound"`
	// Certificates are the pins of the certificates the sender's address
	// serves (core section 3.2): group-pair checks them.
	Certificates []string `json:"certificates,omitempty"`
}

func mailKeys(group, id, from, to string) (info, aad []byte, err error) {
	g, ok1 := Decode64(group, 32)
	i, ok2 := Decode64(id, 16)
	f, ok3 := Decode64(from, 32)
	t, ok4 := Decode64(to, 32)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, nil, errors.New("msfp: a mail's group, id, from or to isn't well formed")
	}
	info = append(append([]byte(groupMailInfo), g...), i...)
	return info, append(f, t...), nil
}

// SealMail seals what a mail carries to the recipient's encryption key
// (HPKE base mode, DHKEM(X25519, HKDF-SHA256), HKDF-SHA256, AES-128-GCM),
// signs the mail with key, and gives its envelope.
func SealMail(key Key, m Mail, to []byte, content Sealed) (Envelope, error) {
	info, aad, err := mailKeys(m.Group, m.ID, m.From, m.To)
	if err != nil {
		return Envelope{}, err
	}
	pub, err := ecdh.X25519().NewPublicKey(to)
	if err != nil {
		return Envelope{}, err
	}
	pk, err := hpke.NewDHKEMPublicKey(pub)
	if err != nil {
		return Envelope{}, err
	}
	enc, sender, err := hpke.NewSender(pk, hpke.HKDFSHA256(), hpke.AES128GCM(), info)
	if err != nil {
		return Envelope{}, err
	}
	pt, err := json.Marshal(content)
	if err != nil {
		return Envelope{}, err
	}
	ct, err := sender.Seal(aad, pt)
	if err != nil {
		return Envelope{}, err
	}
	m.Sealed = Encode64(append(enc, ct...))
	b, err := json.Marshal(m)
	if err != nil {
		return Envelope{}, err
	}
	if len(b) > MaxGroupMail {
		return Envelope{}, errors.New("msfp: a mail over 2,048 bytes")
	}
	return Envelope{Mail: Encode64(b), Signature: Encode64(key.sign(append([]byte(groupMailLabel), b...)))}, nil
}

// ReadMail checks a mail's envelope and gives the mail; its signature is
// checked against fromKey, the sender's current key.
func ReadMail(e Envelope, fromKey []byte) (Mail, error) {
	b, ok := Decode64(e.Mail, -1)
	if !ok || len(b) == 0 || len(b) > MaxGroupMail {
		return Mail{}, errors.New("not a mail of at most 2,048 bytes")
	}
	sig, ok := Decode64(e.Signature, 64)
	if !ok || !verify(fromKey, append([]byte(groupMailLabel), b...), sig) {
		return Mail{}, errors.New("the mail's signature doesn't verify")
	}
	var m Mail
	if DecodeJSON(b, &m) != nil || (m.Kind != "request" && m.Kind != "answer") {
		return Mail{}, errors.New("not a mail")
	}
	if _, _, err := mailKeys(m.Group, m.ID, m.From, m.To); err != nil {
		return Mail{}, err
	}
	return m, nil
}

// OpenMail opens a mail to this server with its encryption key's private
// part.
func OpenMail(m Mail, priv []byte) (Sealed, error) {
	info, aad, err := mailKeys(m.Group, m.ID, m.From, m.To)
	if err != nil {
		return Sealed{}, err
	}
	sealed, ok := Decode64(m.Sealed, -1)
	if !ok || len(sealed) < 32+16 {
		return Sealed{}, errors.New("msfp: a sealed mail too short")
	}
	kem := hpke.DHKEM(ecdh.X25519())
	sk, err := kem.NewPrivateKey(priv)
	if err != nil {
		return Sealed{}, err
	}
	r, err := hpke.NewRecipient(sealed[:32], sk, hpke.HKDFSHA256(), hpke.AES128GCM(), info)
	if err != nil {
		return Sealed{}, err
	}
	pt, err := r.Open(aad, sealed[32:])
	if err != nil {
		return Sealed{}, errors.New("msfp: the mail doesn't open")
	}
	var s Sealed
	if err := DecodeJSON(pt, &s); err != nil {
		return Sealed{}, err
	}
	return s, nil
}

// MailID is a mail's ID from 16 random bytes.
func MailID(random []byte) string { return Encode64(random) }

// MailKeyPair makes an X25519 key pair for a profile from 32 random
// bytes: the private part (to seal and keep) and the public one.
func MailKeyPair(random []byte) (priv, pub []byte, err error) {
	k, err := ecdh.X25519().NewPrivateKey(random)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}
