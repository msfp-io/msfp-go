package wire

import (
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"sort"
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

	Name           string         `json:"name,omitempty"`
	Settings       *GroupSettings `json:"settings,omitempty"`
	Nonce          string         `json:"nonce,omitempty"`
	EncryptionKey  string         `json:"encryptionKey,omitempty"`
	NextCommitment string         `json:"nextCommitment,omitempty"`
	NewMember      string         `json:"newMember,omitempty"`
	Target         string         `json:"target,omitempty"`
	On             *bool          `json:"on,omitempty"`
	LastSeq        *int64         `json:"lastSeq,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	Ban            string         `json:"ban,omitempty"`
	KeepPairings   *bool          `json:"keepPairings,omitempty"`
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
	Joined    int64 // its joined event's date (the founder: founded's)
	// EncryptionKey and NextCommitment, from its latest profile.
	EncryptionKey  []byte
	NextCommitment []byte
	adminAt        int64 // the ownerSeq that named it admin, for "oldest"
}

// GroupState is what a copy's events give.
type GroupState struct {
	ID        string
	Name      string
	Settings  GroupSettings
	Owner     string // member ID
	Heir      string
	Members   map[string]*GroupMember
	Banned    map[string]string // member ID -> the banned event's ID
	Reasons   map[string]string // member ID -> its ban's reason
	Excluded  []string          // equivocation
	Dissolved bool
	Keep      bool // dissolved with keepPairings
	OwnerSeq  int64

	seq       map[string]int64            // member -> last kept seq
	payloads  map[string]map[int64]string // member -> seq -> payload
	adminLog  []adminChange
	removedAt map[string]int64 // admin -> the last seq that counts
	bannedAt  map[string]int64 // banned -> the last seq that counts
	revokedAt map[string]int64
	keys      map[string][]keyFrom // member -> keys in force from a seq
	capCount  map[string]int64
	ids       map[string]bool // event IDs kept
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
	return &GroupState{Members: map[string]*GroupMember{}, Banned: map[string]string{}, Reasons: map[string]string{},
		Settings: DefaultGroupSettings, seq: map[string]int64{}, payloads: map[string]map[int64]string{},
		removedAt: map[string]int64{}, bannedAt: map[string]int64{}, revokedAt: map[string]int64{},
		keys: map[string][]keyFrom{}, capCount: map[string]int64{}, ids: map[string]bool{}}
}

// Seq is a member's last kept seq.
func (s *GroupState) Seq(member string) int64 { return s.seq[member] }

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

// Apply keeps an event if the rules allow it (groups sections 3.2 to 3.5),
// now being the copy's clock; ErrHeld when it must wait, ErrKept when it is
// there already, another error when it is refused.
func (s *GroupState) Apply(e Envelope, now time.Time) (Event, string, error) {
	ev, b, id, err := readEnvelope(e)
	if err != nil {
		return ev, id, err
	}
	if s.ids[id] {
		return ev, id, ErrKept
	}
	if ev.Seq < 1 || ev.Date < 1 || ev.Type == "" || ev.Member == "" {
		return ev, id, errors.New("a common member is missing")
	}
	if ev.Date > now.Unix()+300 {
		return ev, id, errors.New("dated more than 5 minutes in the future")
	}
	if ev.Type == EventFounded {
		if s.ID != "" {
			return ev, id, errors.New("a second founded event")
		}
		if ev.Seq != 1 || ev.Member != ev.Signer || ev.OwnerSeq != 1 || ev.Group != "" {
			return ev, id, errors.New("founded: seq 1, ownerSeq 1, by the founder's own key")
		}
		s.ID, s.Owner, s.OwnerSeq = id, ev.Member, 1
		s.Name = CleanText(ev.Name, MaxGroupName)
		s.applySettings(ev.Settings)
		s.Members[ev.Member] = &GroupMember{ID: ev.Member, Key: ev.Member, Joined: ev.Date}
		s.keep(ev, b, id)
		return ev, id, nil
	}
	if s.ID == "" || ev.Group != s.ID {
		return ev, id, errors.New("another group's event")
	}
	if s.Dissolved {
		return ev, id, errors.New("the group is dissolved")
	}
	if slices.Contains(s.Excluded, ev.Member) {
		return ev, id, errors.New("its member was excluded")
	}
	if prev, ok := s.payloads[ev.Member][ev.Seq]; ok && prev != string(b) {
		// Equivocation (groups section 3.2): the proof is kept, the
		// member excluded.
		s.Excluded = append(s.Excluded, ev.Member)
		s.remove(ev.Member)
		return ev, id, errors.New("equivocation: two events with one seq")
	}
	if ev.Seq > s.seq[ev.Member]+1 {
		return ev, id, ErrHeld
	}
	if ev.Seq <= s.seq[ev.Member] {
		return ev, id, ErrKept
	}
	m := s.Members[ev.Member]
	if m == nil && ev.Type != EventLeft {
		return ev, id, errors.New("its signer isn't a member")
	}
	if s.keyAt(ev.Member, ev.Seq) != ev.Signer && ev.Type != EventRotated {
		return ev, id, errors.New("signer isn't the member's key at that seq")
	}
	if last, ok := s.removedAt[ev.Member]; ok && ev.Seq > last && isAdminEvent(ev.Type) {
		return ev, id, errors.New("signed after its signer's role was removed")
	}
	for _, cut := range []map[string]int64{s.bannedAt, s.revokedAt} {
		if last, ok := cut[ev.Member]; ok && ev.Seq > last {
			return ev, id, errors.New("signed after its signer was banned or its key revoked")
		}
	}
	owner := ev.Member == s.Owner
	if owner && isOwnerEvent(ev.Type) {
		if ev.OwnerSeq != s.OwnerSeq+1 {
			if ev.OwnerSeq > s.OwnerSeq+1 {
				return ev, id, ErrHeld
			}
			return ev, id, errors.New("an owner event whose ownerSeq was taken")
		}
	}
	admin := !owner && ev.AsOf > 0 && ev.AsOf <= s.OwnerSeq && s.adminAsOf(ev.Member, ev.AsOf)
	if !owner && ev.AsOf > s.OwnerSeq {
		return ev, id, ErrHeld
	}
	if err := s.applyType(ev, id, owner, admin); err != nil {
		return ev, id, err
	}
	if owner && isOwnerEvent(ev.Type) {
		s.OwnerSeq = ev.OwnerSeq
	}
	s.keep(ev, b, id)
	return ev, id, nil
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
		if _, banned := s.Banned[ev.NewMember]; banned {
			return errors.New("the newcomer is banned")
		}
		if member(ev.NewMember) {
			return errors.New("the newcomer is a member already")
		}
		day := ev.Member + " " + time.Unix(ev.Date, 0).UTC().Format(time.DateOnly)
		if s.capCount[day] >= s.Settings.CapPerDay {
			return errors.New("over the inviter's daily cap")
		}
		s.capCount[day]++
		s.Members[ev.NewMember] = &GroupMember{ID: ev.NewMember, Key: ev.NewMember, Name: CleanText(ev.Name, MaxGroupName),
			InvitedBy: ev.Member, Joined: ev.Date}
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
		s.remove(ev.Target)
	case EventUnbanned:
		if !owner {
			return errors.New("unbanned is the owner's")
		}
		for m, ban := range s.Banned {
			if ban == ev.Ban {
				delete(s.Banned, m)
				delete(s.Reasons, m)
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
		if last, ok := s.revokedAt[ev.Member]; !ok || *ev.LastSeq < last {
			s.revokedAt[ev.Member] = *ev.LastSeq
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
		if others[i].Joined != others[j].Joined {
			return others[i].Joined < others[j].Joined
		}
		return others[i].ID < others[j].ID
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
