package msfp

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// Groups (MSFP groups part): servers whose operators chose each other. A
// group is the list of signed events every member keeps (wire.GroupState
// computes what they give); members pass events and mail to the members
// they are paired with through the group, ask each other to pair through
// it, and pair by group-pair.

// The calls of the part groups.
const (
	callGroupSync = "group-sync"
	callGroupPush = "group-push"
	callGroupPair = "group-pair"
	callGroupJoin = "group-join"
)

// groupSyncEvery is how often a member reads the list from another
// (groups section 6), give or take a fifth; groupSyncUncallable, for a
// server that can't be called, which is never pushed to.
const (
	groupSyncEvery      = 10 * time.Minute
	groupSyncUncallable = 5 * time.Minute
	maxSyncEvents       = 500
	maxPushMail         = 100
	maxKeptMail         = 1000
)

// GroupSettings are a group's settings: the days before a member's
// requests are accepted automatically, and invitations a day per inviter.
type GroupSettings = wire.GroupSettings

// GroupSettingsDefault are a group's settings by default: 3 days before
// automatic acceptance, 5 invitations a day per inviter.
var GroupSettingsDefault = wire.DefaultGroupSettings

// ---- views -------------------------------------------------------------------

// GroupView is a group as the panel shows it.
type GroupView struct {
	ID, Name  string
	Settings  wire.GroupSettings
	Role      string // "owner", "admin" or "member"
	Me        string // this server's member ID
	Owner     string
	Heir      string
	Members   []GroupMemberView
	Banned    []GroupBanView
	Requests  []GroupRequestView
	Accept    bool
	Offers    []string
	Local     bool // its local titles shared with the group (SetGroupLocal)
	Pool      Limits
	Dissolved bool
	// Joining: the list hasn't arrived from the inviter yet.
	Joining bool
	// Joins are peers already paired with this server that used its code
	// for the group, waiting for AcceptJoin or RefuseJoin.
	Joins []GroupJoinView
	// NoWayIn: this server has no pairing through the group, and the group
	// has other members: nothing reaches them from here, nor here from them
	// (groups section 6, "No way in").
	NoWayIn bool
}

// GroupRef names a group.
type GroupRef struct{ ID, Name string }

// GroupJoinView is a peer waiting to join a group through this server.
type GroupJoinView struct {
	Peer, Name, Fingerprint string
	Received                time.Time
}

// GroupMemberView is a member, as the panel lists it (groups section 10).
type GroupMemberView struct {
	ID, Name, Fingerprint string
	Role                  string
	InvitedBy             string
	Joined                time.Time
	// Peer is the link with it, "" for none. Refused: this server refused
	// its requests (LiftRefusal undoes it); Asked: this server's request
	// to it waits for its answer.
	Peer           string
	Refused, Asked bool
}

// GroupBanView is a banned server, and the reason given.
// GroupBanView is a banned server: its member ID, name and fingerprint as
// it was a member, and the ban's reason ("" for none).
type GroupBanView struct{ ID, Name, Fingerprint, Reason string }

// GroupRequestView is a member's request waiting for this server's
// operator; Waited says it has been in the group the group's days.
type GroupRequestView struct {
	Mail, From, Name, Fingerprint string
	Received                      time.Time
	Waited                        bool
}

func role(s *wire.GroupState, m string) string {
	switch {
	case m == s.Owner:
		return "owner"
	case s.Members[m] != nil && s.Members[m].Admin:
		return "admin"
	}
	return "member"
}

// ---- reading a group ------------------------------------------------------------

func groupsTx(tx Tx) (GroupsTx, error) {
	g, ok := tx.(GroupsTx)
	if !ok {
		return nil, ErrNoGroups
	}
	return g, nil
}

// stateOf is the state a group's kept events give (groups section 3.5),
// whatever order they came in.
func (e *Engine) stateOf(g *GroupRecord) *wire.GroupState {
	s, _ := wire.ComputeGroup(g.ID, envelopes(g.Events, g.Held))
	return s
}

// storedID is a stored event's ID: SHA-256 of its payload, base64url.
func storedID(ev StoredEvent) string {
	b, _ := wire.Decode64(ev.Event, -1)
	sum := sha256.Sum256(b)
	return wire.Encode64(sum[:])
}

// envelopes are stored events as envelopes.
func envelopes(lists ...[]StoredEvent) []wire.Envelope {
	var out []wire.Envelope
	for _, l := range lists {
		for _, ev := range l {
			out = append(out, wire.Envelope{Payload: ev.Event, Signature: ev.Signature})
		}
	}
	return out
}

func (e *Engine) groupView(g *GroupRecord, peers []PeerRecord) GroupView {
	s := e.stateOf(g)
	v := GroupView{ID: g.ID, Name: s.Name, Settings: s.Settings, Me: g.Me, Owner: s.Owner, Heir: s.Heir,
		Role: role(s, g.Me), Accept: g.Accept, Offers: slices.Clone(g.Offers), Local: g.Local, Pool: g.Pool, Dissolved: s.Dissolved,
		Joining: s.Members[g.Me] == nil && len(g.Events) == 0}
	for _, m := range s.Members {
		key, _ := wire.Decode64(m.Key, 32)
		mv := GroupMemberView{ID: m.ID, Name: m.Name, Fingerprint: wire.Fingerprint(key), Role: role(s, m.ID),
			InvitedBy: m.InvitedBy, Joined: time.Unix(m.Joined, 0).UTC(), Refused: slices.Contains(g.Refused, m.ID),
			Asked: slices.ContainsFunc(g.Sent, func(x SentMail) bool { return x.To == m.ID && x.Kind == "request" && x.Expires.After(e.now()) })}
		for _, p := range peers {
			if isMember(m, &p) {
				mv.Peer = p.ID
			}
		}
		v.Members = append(v.Members, mv)
	}
	slices.SortFunc(v.Members, func(a, b GroupMemberView) int { return int(a.Joined.Sub(b.Joined)) })
	v.NoWayIn = e.cutOff(g, s, peers, "")
	for id := range s.Banned {
		b := GroupBanView{ID: id, Reason: s.Reasons[id]}
		if m, ok := s.WasBanned[id]; ok {
			key, _ := wire.Decode64(m.Key, 32)
			b.Name, b.Fingerprint = m.Name, wire.Fingerprint(key)
		}
		v.Banned = append(v.Banned, b)
	}
	slices.SortFunc(v.Banned, func(a, b GroupBanView) int { return strings.Compare(a.Name+a.ID, b.Name+b.ID) })
	for _, j := range g.Joins {
		jv := GroupJoinView{Peer: j.Peer, Received: j.Received}
		for _, p := range peers {
			if p.ID == j.Peer {
				jv.Name, jv.Fingerprint = p.Name, wire.Fingerprint(p.Key)
			}
		}
		v.Joins = append(v.Joins, jv)
	}
	for _, r := range g.Requests {
		key, _ := wire.Decode64(r.From, 32)
		if m := s.Members[r.From]; m != nil {
			key, _ = wire.Decode64(m.Key, 32)
		}
		v.Requests = append(v.Requests, GroupRequestView{Mail: r.Mail, From: r.From, Name: r.Name,
			Fingerprint: wire.Fingerprint(key), Received: r.Received, Waited: e.waited(s, r.From)})
	}
	return v
}

// waited says whether a member has been in the group its days (groups
// section 4.1).
// A member automatically accepted is accepted at once: the group's
// waitDays changes nothing (groups section 4.1).
func (e *Engine) waited(s *wire.GroupState, member string) bool {
	return s.Members[member] != nil
}

// Groups lists the groups this server is in.
func (e *Engine) Groups(ctx context.Context) ([]GroupView, error) {
	var out []GroupView
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		gs, err := gt.Groups()
		if err != nil {
			return err
		}
		peers, err := tx.Peers()
		for i := range gs {
			out = append(out, e.groupView(&gs[i], peers))
		}
		return err
	})
	return out, err
}

// wayIn says whether this server has a pairing through the group, but for
// the peer except: the links events and mail travel on (groups section 6).
func (e *Engine) wayIn(id string, peers []PeerRecord, except string) bool {
	return slices.ContainsFunc(peers, func(p PeerRecord) bool {
		return p.ID != except && p.State == Paired && slices.Contains(p.Groups, id) && e.on(&p, wire.PartGroups)
	})
}

// cutOff says whether this server, a member of the group with others in
// it, has no way in (without the peer except).
func (e *Engine) cutOff(g *GroupRecord, s *wire.GroupState, peers []PeerRecord, except string) bool {
	if s.Dissolved || s.Members[g.Me] == nil || len(s.Members) < 2 {
		return false
	}
	return !e.wayIn(g.ID, peers, except)
}

// GroupsCutBy lists the groups that ending the pairing with this peer
// would leave this server with no way in: the panel says so before it
// ends (groups section 6, "No way in").
func (e *Engine) GroupsCutBy(ctx context.Context, peer string) ([]GroupRef, error) {
	var out []GroupRef
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			if errors.Is(err, ErrNoGroups) {
				return nil
			}
			return err
		}
		gs, err := gt.Groups()
		if err != nil {
			return err
		}
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for i := range gs {
			g := &gs[i]
			s := e.stateOf(g)
			if e.wayIn(g.ID, peers, "") && e.cutOff(g, s, peers, peer) {
				out = append(out, GroupRef{ID: g.ID, Name: s.Name})
			}
		}
		return nil
	})
	return out, err
}

// Group is one group. Error: ErrNotFound.
func (e *Engine) Group(ctx context.Context, id string) (GroupView, error) {
	var out GroupView
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		peers, err := tx.Peers()
		out = e.groupView(g, peers)
		return err
	})
	return out, err
}

func sha256Sum(b []byte) []byte {
	h := sha256.Sum256(b)
	return h[:]
}

func cmpErr(err, otherwise error) error {
	if err != nil {
		return err
	}
	return otherwise
}

// ---- signing this server's events -------------------------------------------

// sign makes this server's next event in a group, on its state: its seq,
// and ownerSeq or asOf as its role asks.
func (e *Engine) sign(g *GroupRecord, s *wire.GroupState, ev wire.Event) (StoredEvent, error) {
	ev.Group, ev.Signer, ev.Member = g.ID, wire.Encode64(e.current().Public()), g.Me
	if ev.Type == wire.EventFounded {
		ev.Group = ""
	}
	// Never a seq it signed already, counted or not.
	ev.Seq, ev.Date = s.Highest(g.Me)+1, e.now().Unix()
	if s.Owner == g.Me && ev.Type != wire.EventProfile && ev.Type != wire.EventLeft && ev.Type != wire.EventRotated && ev.Type != wire.EventRevoked {
		ev.OwnerSeq = s.OwnerSeq + 1
	} else if ev.Type == wire.EventJoined || ev.Type == wire.EventBanned {
		ev.AsOf = s.OwnerSeq
	}
	env, id, err := wire.SignEvent(e.current(), ev)
	if err != nil {
		return StoredEvent{}, err
	}
	// It must count, with the events kept: otherwise it isn't sent.
	group := g.ID
	if ev.Type == wire.EventFounded {
		group = id
	}
	next, results := wire.ComputeGroup(group, append(envelopes(g.Events, g.Held), env))
	if r := results[id]; r.Verdict != wire.Counted {
		return StoredEvent{}, ErrNoRight.with(errors.New(r.Reason))
	}
	*s = *next
	return StoredEvent{Event: env.Payload, Signature: env.Signature, Received: e.now()}, nil
}

// act signs one event of this server in a group, keeps it, and passes it
// on. Errors: ErrNotFound, ErrNoRight.
func (e *Engine) act(ctx context.Context, id string, ev wire.Event) error {
	var sent StoredEvent
	var effects []func()
	// Who is told: the members it is paired with through the group now,
	// before the event changes who they are; a banned server too
	// (groups section 7).
	to := e.groupPeers(ctx, id, "")
	var banned *PeerRecord
	if ev.Type == wire.EventBanned {
		if k, ok := wire.Decode64(ev.Target, 32); ok {
			if p, err := e.peerByKey(ctx, k); err == nil && p != nil {
				banned = p
				to = slices.DeleteFunc(to, func(q PeerRecord) bool { return q.ID == p.ID })
			}
		}
	}
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		if s.Members[g.Me] == nil {
			return ErrNotMember
		}
		if sent, err = e.sign(g, s, ev); err != nil {
			return err
		}
		g.Events = append(g.Events, sent)
		if err := gt.PutGroup(*g); err != nil {
			return err
		}
		return e.afterEvents(tx, g, s, &effects)
	})
	if err != nil {
		return err
	}
	if banned != nil && banned.Inbound == InboundYes {
		// The banned server hears of it before its link here ends, which
		// it does when nothing else holds it (core section 7.7).
		body := groupPushRequest{Group: id, Events: []wire.Envelope{{Payload: sent.Event, Signature: sent.Signature}}}
		var ans struct{}
		if _, err := e.call(ctx, banned, callGroupPush, body, &ans, true); err != nil {
			e.log.Debug("msfp: the banned server couldn't be told", "peer", banned.ID, "error", err)
		}
	}
	e.pushTo(to, id, []StoredEvent{sent}, nil)
	for _, f := range effects {
		f()
	}
	return nil
}

func (e *Engine) peerByKey(ctx context.Context, key []byte) (*PeerRecord, error) {
	var p *PeerRecord
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		p, err = tx.PeerByKey(key)
		return err
	})
	return p, err
}

// ---- the operator's actions ----------------------------------------------------

// CreateGroup founds a group, this server its owner, with name and
// settings (zero values: the defaults).
func (e *Engine) CreateGroup(ctx context.Context, name string, settings wire.GroupSettings) (GroupView, error) {
	if settings == (wire.GroupSettings{}) {
		settings = wire.DefaultGroupSettings
	}
	name = wire.CleanText(name, wire.MaxGroupName)
	if !settingsOK(settings) || name == "" {
		return GroupView{}, ErrCodeOptions
	}
	nonce, err := e.random(16)
	if err != nil {
		return GroupView{}, err
	}
	me := wire.Encode64(e.current().Public())
	g := GroupRecord{Me: me, Pool: *e.cfg.NewPairing}
	if err := e.newMailKey(&g); err != nil {
		return GroupView{}, err
	}
	s := wire.NewGroupState()
	founded, err := e.sign(&g, s, wire.Event{Type: wire.EventFounded, Name: name, Settings: &settings, Nonce: wire.Encode64(nonce), OwnerSeq: 1})
	if err != nil {
		return GroupView{}, err
	}
	g.ID = s.ID
	g.Events = []StoredEvent{founded}
	profile, err := e.profile(&g, s)
	if err != nil {
		return GroupView{}, err
	}
	g.Events = append(g.Events, profile)
	var v GroupView
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		if err := gt.PutGroup(g); err != nil {
			return err
		}
		v = e.groupView(&g, nil)
		return nil
	})
	return v, err
}

func (e *Engine) newMailKey(g *GroupRecord) error {
	seed, err := e.random(32)
	if err != nil {
		return err
	}
	priv, _, err := wire.MailKeyPair(seed)
	if err != nil {
		return err
	}
	g.MailKey, err = e.cfg.Sealer.Seal(priv, sealMailKey)
	return err
}

// profile signs this server's profile: its name, mail key and next key
// commitment.
func (e *Engine) profile(g *GroupRecord, s *wire.GroupState) (StoredEvent, error) {
	priv, err := e.cfg.Sealer.Open(g.MailKey, sealMailKey)
	if err != nil {
		return StoredEvent{}, err
	}
	_, pub, err := wire.MailKeyPair(priv)
	if err != nil {
		return StoredEvent{}, err
	}
	return e.sign(g, s, wire.Event{Type: wire.EventProfile, Name: e.name(), EncryptionKey: wire.Encode64(pub),
		NextCommitment: wire.Encode64(e.commitment())})
}

// groupForCode is a group's ID and name for a code inviting to it: this
// server must be its owner or an admin.
func (e *Engine) groupForCode(ctx context.Context, id string) ([]byte, string, error) {
	gid, ok := wire.Decode64(id, 32)
	if !ok {
		return nil, "", ErrNotFound
	}
	var name string
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		if s.Dissolved || !s.IsAdmin(g.Me) {
			return ErrNoRight
		}
		name = s.Name
		return nil
	})
	return gid, name, err
}

// SetGroupAnswer chooses how this server answers requests in a group:
// accept by itself once a member waited the group's days, or ask.
func (e *Engine) SetGroupAnswer(ctx context.Context, id string, accept bool) error {
	return e.updateGroup(ctx, id, func(g *GroupRecord) { g.Accept = accept })
}

// GroupOffers lists the host's libraries, those offered to a group marked
// Offered (as Offers does for a peer). Errors: ErrNotFound, ErrNoGroups.
func (e *Engine) GroupOffers(ctx context.Context, id string) ([]Library, error) {
	g, err := e.Group(ctx, id)
	if err != nil {
		return nil, err
	}
	libs, err := e.hostLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for i := range libs {
		libs[i].Offered = slices.Contains(g.Offers, libs[i].ID)
	}
	return libs, nil
}

// SetGroupOffers sets the host libraries this server offers a group
// (groups section 9; IDs that aren't the host's are left out): every link
// through it is offered them, and their playbacks from libraries no longer
// offered end. Error: ErrNotFound.
func (e *Engine) SetGroupOffers(ctx context.Context, id string, libraries []string) error {
	ours, err := e.hostLibraries(ctx)
	if err != nil {
		return err
	}
	var keep []string
	for _, l := range ours {
		if slices.Contains(libraries, l.ID) && !slices.Contains(keep, l.ID) {
			keep = append(keep, l.ID)
		}
	}
	slices.Sort(keep)
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		if _, err := e.publicIDs(tx, keep); err != nil {
			return err
		}
		g.Offers = keep
		if err := gt.PutGroup(*g); err != nil {
			return err
		}
		return e.refreshLinksAt(tx, mark)
	})
	if err != nil {
		return err
	}
	e.bumpLog(ctx)
	e.endPlaybacksNotOffered(ctx)
	return nil
}

// SetGroupLocal chooses whether this server shares its local titles
// (MSFP catalog section 4.3) with a group, in the libraries it offers it:
// off by default. A change forces a whole reading on every link through
// it. Errors: ErrNotFound, ErrNoGroups.
func (e *Engine) SetGroupLocal(ctx context.Context, id string, on bool) error {
	mark, err := e.seqMark(ctx)
	if err != nil {
		return err
	}
	changed := false
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		if changed = g.Local != on; !changed {
			return nil
		}
		g.Local = on
		if err := gt.PutGroup(*g); err != nil {
			return err
		}
		return e.refreshLinksAt(tx, mark)
	})
	if err == nil && changed {
		e.bumpLog(ctx)
	}
	return err
}

// endPlaybacksNotOffered ends the playbacks from libraries their link is no
// longer offered (playback section 6.3).
func (e *Engine) endPlaybacksNotOffered(ctx context.Context) {
	offers := map[string][]string{}
	_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
		peers, err := tx.Peers()
		for _, p := range peers {
			offers[p.ID] = offered(&p)
		}
		return err
	})
	e.endPlaybacks(func(pb *playback) bool {
		o, ok := offers[pb.peer]
		return ok && !offeredIn(pb.libraries, o)
	}, EndNotOffered)
}

// SetGroupPool sets the pool this server lends a group: streams,
// transcodes and a bitrate cap shared by its members. Error:
// ErrInvalidLimits.
func (e *Engine) SetGroupPool(ctx context.Context, id string, l Limits) error {
	if l.Streams < 0 || l.Streams > 50 || l.Transcodes < 0 || l.Transcodes > l.Streams || l.BitrateKbps < 0 {
		return ErrInvalidLimits
	}
	return e.updateGroup(ctx, id, func(g *GroupRecord) { g.Pool = l })
}

func (e *Engine) updateGroup(ctx context.Context, id string, fn func(*GroupRecord)) error {
	return e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		fn(g)
		if err := gt.PutGroup(*g); err != nil {
			return err
		}
		return e.refreshLinks(tx)
	})
}

// SetAdmin names a member admin, or removes its role (the owner only).
func (e *Engine) SetAdmin(ctx context.Context, id, member string, on bool) error {
	ev := wire.Event{Type: wire.EventAdmin, Target: member, On: &on}
	if !on {
		last, err := e.lastSeq(ctx, id, member)
		if err != nil {
			return err
		}
		ev.LastSeq = &last
	}
	return e.act(ctx, id, ev)
}

// SetHeir names the member that takes the group when this owner steps
// down ("" for none).
func (e *Engine) SetHeir(ctx context.Context, id, member string) error {
	return e.act(ctx, id, wire.Event{Type: wire.EventHeir, Target: member})
}

// GiveGroup gives the group to a member; "" steps down to the successor
// (the heir, else the oldest admin, else the oldest member).
func (e *Engine) GiveGroup(ctx context.Context, id, member string) error {
	if member == "" {
		v, err := e.successor(ctx, id)
		if err != nil {
			return err
		}
		member = v
	}
	if member == "" {
		return ErrNoRight.with(errors.New("nobody to give the group to"))
	}
	return e.act(ctx, id, wire.Event{Type: wire.EventOwner, Target: member})
}

func (e *Engine) successor(ctx context.Context, id string) (string, error) {
	var out string
	err := e.withState(ctx, id, func(g *GroupRecord, s *wire.GroupState) error { out = s.Successor(); return nil })
	return out, err
}

// SetGroupSettings changes the group's name and settings (the owner only).
//
// Error: ErrCodeOptions (days 0 to 30, invitations 1 to 100 a day; an
// empty name keeps the name).
func (e *Engine) SetGroupSettings(ctx context.Context, id, name string, settings wire.GroupSettings) error {
	if !settingsOK(settings) {
		return ErrCodeOptions
	}
	return e.act(ctx, id, wire.Event{Type: wire.EventSettings, Name: wire.CleanText(name, wire.MaxGroupName), Settings: &settings})
}

// settingsOK says whether a group's settings are in range (groups section
// 3.3).
func settingsOK(s wire.GroupSettings) bool {
	return s.WaitDays >= 0 && s.WaitDays <= 30 && s.CapPerDay >= 1 && s.CapPerDay <= 100
}

// Ban bans a member, with an optional reason (the owner, or an admin for
// a member that isn't an admin).
func (e *Engine) Ban(ctx context.Context, id, member, reason string) error {
	last, err := e.lastSeq(ctx, id, member)
	if err != nil {
		return err
	}
	return e.act(ctx, id, wire.Event{Type: wire.EventBanned, Target: member, LastSeq: &last,
		Reason: wire.CleanText(reason, wire.MaxGroupReason)})
}

// Unban lifts a member's ban (the owner only).
func (e *Engine) Unban(ctx context.Context, id, member string) error {
	var ban string
	err := e.withState(ctx, id, func(g *GroupRecord, s *wire.GroupState) error {
		ban = s.Banned[member]
		if ban == "" {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	return e.act(ctx, id, wire.Event{Type: wire.EventUnbanned, Ban: ban})
}

func (e *Engine) lastSeq(ctx context.Context, id, member string) (int64, error) {
	var last int64
	err := e.withState(ctx, id, func(g *GroupRecord, s *wire.GroupState) error {
		if s.Members[member] == nil {
			return ErrNotMember
		}
		last = s.Seq(member)
		return nil
	})
	return last, err
}

func (e *Engine) withState(ctx context.Context, id string, fn func(*GroupRecord, *wire.GroupState) error) error {
	return e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		return fn(g, e.stateOf(g))
	})
}

// LeaveGroup leaves a group. Every link through it loses it as a
// relation; one with no relation left ends, but those in ask, which are
// asked for the individual relation first and stay until their operators
// answer (groups section 7, core section 7.7).
func (e *Engine) LeaveGroup(ctx context.Context, id string, ask []string) error {
	for _, p := range ask {
		if _, err := e.AskIndividual(ctx, p); err != nil && !errors.Is(err, ErrNotFound) {
			e.log.Info("msfp: a server couldn't be asked for the individual relation before leaving", "peer", p, "error", err)
		}
	}
	if g, err := e.Group(ctx, id); err == nil && g.Joining {
		// Not a member yet: giving up joining (groups section 2).
		return e.dropGroup(ctx, id, false, "left", "")
	}
	if err := e.act(ctx, id, wire.Event{Type: wire.EventLeft}); err != nil {
		return err
	}
	return e.dropGroup(ctx, id, false, "left", "")
}

// DissolveGroup dissolves the group (the owner only): every member removes
// it; links it alone held end, or, with keepPairings, wait for each
// side's operator to keep (AskIndividual) or end them (RefuseIndividual).
func (e *Engine) DissolveGroup(ctx context.Context, id string, keepPairings bool) error {
	return e.act(ctx, id, wire.Event{Type: wire.EventDissolved, KeepPairings: &keepPairings})
}

// dropGroup removes a group from this server: its record, and from every
// link's relations. A link with no relation left ends, or, with keep (a
// dissolution with keepPairings), waits for the operator's choice.
func (e *Engine) dropGroup(ctx context.Context, id string, keep bool, why, reason string) error {
	var ending []string
	var left *GroupLeft
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		left = nil
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, _ := gt.Group(id)
		if g != nil {
			left = &GroupLeft{Group: id, GroupName: e.stateOf(g).Name, Why: why, Reason: reason}
		}
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if !slices.Contains(p.Groups, id) {
				continue
			}
			p.Groups = slices.DeleteFunc(p.Groups, func(x string) bool { return x == id })
			alone := p.State == Paired && !holds(&p, false)
			if alone && !keep {
				ending = append(ending, p.ID)
				continue
			}
			if alone {
				// Kept or ended by our operator; its offers and limits, if
				// kept, are the group's until changed.
				p.KeepChoice = true
				if g != nil {
					p.Offers, p.Local, p.Limits = slices.Clone(g.Offers), g.Local, g.Pool
				}
			}
			if err := tx.PutPeer(p); err != nil {
				return err
			}
		}
		if err := gt.DeleteGroup(id); err != nil {
			return err
		}
		return e.refreshLinks(tx)
	})
	if err != nil {
		return err
	}
	if left != nil {
		e.notify(*left)
	}
	for _, p := range ending {
		if err := e.End(ctx, p); err != nil && !errors.Is(err, ErrNotFound) {
			e.log.Info("msfp: a link through a group couldn't be ended", "peer", p, "error", err)
		}
	}
	return nil
}

// Block ends the link with a member, whatever its sources, and forgets
// its key, so that its requests are dropped in every group (groups section
// 7); with invitees, the members it invited too, and theirs.
func (e *Engine) Block(ctx context.Context, id, member string, invitees bool) error {
	var keys [][]byte
	err := e.withState(ctx, id, func(g *GroupRecord, s *wire.GroupState) error {
		todo := []string{member}
		for len(todo) > 0 {
			m := todo[0]
			todo = todo[1:]
			if gm := s.Members[m]; gm != nil {
				k, _ := wire.Decode64(gm.Key, 32)
				keys = append(keys, k)
			} else if k, ok := wire.Decode64(m, 32); ok {
				keys = append(keys, k)
			}
			if invitees {
				for _, x := range s.Members {
					if x.InvitedBy == m {
						todo = append(todo, x.ID)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, k := range keys {
		var peer string
		err := e.cfg.Store.Update(ctx, func(tx Tx) error {
			p, err := tx.PeerByKey(k)
			if err != nil {
				return err
			}
			if p != nil {
				peer = p.ID
				return nil
			}
			h := sha256Sum(k)
			return tx.Forget(ForgottenKey{KeyHash: h, At: e.now(), Blocked: true})
		})
		if err != nil {
			return err
		}
		if peer != "" {
			if err := e.BlockPeer(ctx, peer); err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

// ---- requests -------------------------------------------------------------------

// AcceptRequest accepts a member's request: the two pair through the
// group (groups section 4.2).
func (e *Engine) AcceptRequest(ctx context.Context, id, mail string) error {
	var r GroupRequest
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		i := slices.IndexFunc(g.Requests, func(x GroupRequest) bool { return x.Mail == mail })
		if i < 0 {
			return ErrNotFound
		}
		r = g.Requests[i]
		g.Requests = slices.Delete(g.Requests, i, i+1)
		return gt.PutGroup(*g)
	})
	if err != nil {
		return err
	}
	return e.answerRequest(ctx, id, r)
}

// RefuseRequest refuses a member's request: nothing is sent, and its
// requests are dropped until LiftRefusal (groups section 4.1).
func (e *Engine) RefuseRequest(ctx context.Context, id, mail string) error {
	return e.updateGroup(ctx, id, func(g *GroupRecord) {
		i := slices.IndexFunc(g.Requests, func(x GroupRequest) bool { return x.Mail == mail })
		if i < 0 {
			return
		}
		if !slices.Contains(g.Refused, g.Requests[i].From) {
			g.Refused = append(g.Refused, g.Requests[i].From)
		}
		g.Requests = slices.Delete(g.Requests, i, i+1)
	})
}

// LiftRefusal takes a member off this server's refusals.
func (e *Engine) LiftRefusal(ctx context.Context, id, member string) error {
	return e.updateGroup(ctx, id, func(g *GroupRecord) {
		g.Refused = slices.DeleteFunc(g.Refused, func(x string) bool { return x == member })
	})
}

// RequestMember sends a member this server's request, as a newcomer does
// to every member (groups section 4).
func (e *Engine) RequestMember(ctx context.Context, id, member string) error {
	return e.sendRequests(ctx, id, []string{member})
}

// answerRequest makes the link a request asks for: group-pair at the
// requester's address, or an answer mail when it can't be called.
func (e *Engine) answerRequest(ctx context.Context, id string, r GroupRequest) error {
	if r.Inbound {
		return e.groupPair(ctx, id, r.From, r.Mail, r.Address, r.NextCommitment, true, r.Pins)
	}
	return e.sendMail(ctx, id, r.From, "answer", r.Mail)
}

// sendRequests sends this server's request to members (all those it isn't
// linked with, for nil).
func (e *Engine) sendRequests(ctx context.Context, id string, to []string) error {
	var targets []string
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		if to != nil {
			// Asked for by the operator: no way in is said first, whatever
			// else would skip the member (groups section 6).
			peers, err := tx.Peers()
			if err != nil {
				return err
			}
			if !e.wayIn(id, peers, "") {
				return ErrNoWayIn
			}
		}
		for _, m := range s.Members {
			if m.ID == g.Me || (to != nil && !slices.Contains(to, m.ID)) || m.EncryptionKey == nil {
				continue
			}
			key, _ := wire.Decode64(m.Key, 32)
			if p, err := tx.PeerByKey(key); err != nil || p != nil {
				continue
			}
			if slices.ContainsFunc(g.Sent, func(x SentMail) bool { return x.To == m.ID && x.Expires.After(e.now()) }) {
				continue
			}
			targets = append(targets, m.ID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.log.Debug("msfp: group requests", "group", id, "to", len(targets))
	for _, m := range targets {
		if err := e.sendMail(ctx, id, m, "request", ""); err != nil {
			if to != nil {
				return err // asked for by the operator: the panel says why
			}
			e.log.Info("msfp: a group request couldn't be made", "group", id, "error", err)
		}
	}
	return nil
}

// sendMail seals and sends one mail to a member: a request, or an answer
// to one.
func (e *Engine) sendMail(ctx context.Context, id, to, kind, answers string) error {
	var env StoredMail
	pins := e.ownPins(ctx, false)
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		m := s.Members[to]
		if m == nil || m.EncryptionKey == nil {
			return ErrNotMember
		}
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		if !e.wayIn(id, peers, "") {
			return ErrNoWayIn
		}
		raw, err := e.random(16)
		if err != nil {
			return err
		}
		mid := wire.MailID(raw)
		expires := e.now().Add(wire.MailLifetime)
		mail := wire.Mail{Group: id, ID: mid, Kind: kind, From: g.Me, To: to, Answers: answers, Expires: expires.Unix()}
		content := wire.Sealed{Address: e.named(e.address()), Name: e.name(), NextCommitment: wire.Encode64(e.commitment()),
			Inbound: !e.saysUncallable(), Certificates: pins}
		sealed, err := wire.SealMail(e.current(), mail, m.EncryptionKey, content)
		if err != nil {
			return err
		}
		env = StoredMail{ID: mid, Mail: sealed.Mail, Signature: sealed.Signature, Expires: expires.Unix()}
		g.Mail = append(g.Mail, env)
		g.Sent = append(g.Sent, SentMail{ID: mid, To: to, Kind: kind, Expires: expires})
		return gt.PutGroup(*g)
	})
	if err != nil {
		return err
	}
	e.pushSoon(id, nil, []StoredMail{env}, "")
	return nil
}

// ---- receiving events and mail ----------------------------------------------------

// receive keeps what a member passed on: events (in any order: those
// that wait for earlier ones are held) and mail. It gives what was new,
// to pass on to the others.
func (e *Engine) receive(ctx context.Context, id string, events []wire.Envelope, mail []wire.Envelope, from string) error {
	var newEvents []StoredEvent
	var newMail []StoredMail
	var effects []func()
	var told []Event
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		newEvents, newMail, effects, told = nil, nil, nil, nil
		var types []string
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		wasMember := e.stateOf(g).Members[g.Me] != nil
		now := e.now()
		// Every event that passes section 3.2's checks is kept and passed
		// on, counted or not: the state is what they all give, whatever
		// order they came in (groups section 3.5).
		all := append(slices.Clone(g.Events), g.Held...)
		known := map[string]bool{}
		for _, ev := range all {
			known[storedID(ev)] = true
		}
		var added []StoredEvent
		for _, env := range events {
			if _, eid, err := wire.CheckEvent(env, id, now); err == nil && !known[eid] {
				known[eid] = true
				added = append(added, StoredEvent{Event: env.Payload, Signature: env.Signature, Received: now})
			}
		}
		all = append(all, added...)
		s, results := wire.ComputeGroup(id, envelopes(all))
		g.Events, g.Held = nil, nil
		for _, ev := range all {
			switch r := results[storedID(ev)]; {
			case r.Verdict == wire.Forged:
				// Not its member's: dropped.
			case r.Verdict == wire.Pending && now.Sub(ev.Received) >= wire.HeldFor:
				// Waited too long for what it needs: dropped.
			default:
				g.Events = append(g.Events, ev)
			}
		}
		for _, ev := range added {
			r := results[storedID(ev)]
			if r.Verdict == wire.Forged {
				continue
			}
			newEvents = append(newEvents, ev)
			if r.Verdict == wire.Counted && !slices.Contains(types, r.Event.Type) {
				types = append(types, r.Event.Type)
			}
		}
		for _, env := range mail {
			m, ok := e.keepMail(g, s, env)
			if !ok {
				continue
			}
			newMail = append(newMail, m)
		}
		g.Mail = slices.DeleteFunc(g.Mail, func(m StoredMail) bool { return m.Expires < now.Unix() })
		if len(g.Mail) > maxKeptMail {
			slices.SortFunc(g.Mail, func(a, b StoredMail) int { return int(b.Expires - a.Expires) })
			g.Mail = g.Mail[:maxKeptMail]
		}
		if err := gt.PutGroup(*g); err != nil {
			return err
		}
		if err := e.afterEvents(tx, g, s, &effects); err != nil {
			return err
		}
		if len(types) > 0 && wasMember {
			told = append(told, GroupChanged{Group: id, GroupName: s.Name, Types: types})
		}
		if !wasMember && s.Members[g.Me] != nil {
			told = append(told, GroupJoined{Group: id, GroupName: s.Name})
			// The list says this server joined: its profile, then its
			// requests (groups section 4, steps 5 and 6).
			effects = append(effects, func() { e.background(func(ctx context.Context) { e.joined(ctx, id) }) })
		}
		for _, m := range newMail {
			if mm, err := e.mailTo(g, s, m); err == nil && mm != nil {
				mm := *mm
				effects = append(effects, func() { e.background(func(ctx context.Context) { e.gotMail(ctx, id, mm) }) })
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.notify(told...)
	for _, f := range effects {
		f()
	}
	if len(newEvents) > 0 || len(newMail) > 0 {
		e.pushSoon(id, newEvents, newMail, from)
	}
	return nil
}

// keepMail checks a mail and keeps it when it is new.
func (e *Engine) keepMail(g *GroupRecord, s *wire.GroupState, env wire.Envelope) (StoredMail, bool) {
	b, ok := wire.Decode64(env.Mail, -1)
	if !ok {
		return StoredMail{}, false
	}
	var head struct {
		ID      string `json:"id"`
		From    string `json:"from"`
		Expires int64  `json:"expires"`
	}
	if wire.DecodeEntry(b, &head) != nil {
		return StoredMail{}, false
	}
	from := s.Members[head.From]
	if from == nil || head.Expires < e.now().Unix() || head.Expires > e.now().Add(wire.MailLifetime+time.Hour).Unix() {
		e.log.Debug("msfp: a mail from a non-member or out of date", "from", head.From, "member", from != nil)
		return StoredMail{}, false
	}
	if slices.ContainsFunc(g.Mail, func(m StoredMail) bool { return m.ID == head.ID }) {
		return StoredMail{}, false
	}
	key, _ := wire.Decode64(from.Key, 32)
	m, err := wire.ReadMail(env, key)
	if err != nil || m.Group != g.ID || s.Members[m.To] == nil {
		e.log.Debug("msfp: a mail refused", "error", err)
		return StoredMail{}, false
	}
	sm := StoredMail{ID: m.ID, Mail: env.Mail, Signature: env.Signature, Expires: m.Expires}
	g.Mail = append(g.Mail, sm)
	return sm, true
}

// mailTo is a kept mail read, when it is to this server.
func (e *Engine) mailTo(g *GroupRecord, s *wire.GroupState, sm StoredMail) (*wire.Mail, error) {
	from := s.Members[e.mailFrom(sm)]
	if from == nil {
		return nil, ErrNotMember
	}
	key, _ := wire.Decode64(from.Key, 32)
	m, err := wire.ReadMail(wire.Envelope{Mail: sm.Mail, Signature: sm.Signature}, key)
	if err != nil || m.To != g.Me {
		return nil, err
	}
	return &m, nil
}

func (e *Engine) mailFrom(sm StoredMail) string {
	b, _ := wire.Decode64(sm.Mail, -1)
	var head struct {
		From string `json:"from"`
	}
	_ = wire.DecodeEntry(b, &head)
	return head.From
}

// gotMail handles a mail to this server: a request waits for the
// operator, or is accepted by the server itself; an answer leads to
// group-pair with the member that accepted.
func (e *Engine) gotMail(ctx context.Context, id string, m wire.Mail) {
	e.log.Debug("msfp: a mail to this server", "kind", m.Kind)
	var opened wire.Sealed
	var accept bool
	var req GroupRequest
	var waiting *GroupRequestWaiting
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		waiting = nil
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		priv, err := e.cfg.Sealer.Open(g.MailKey, sealMailKey)
		if err != nil {
			return err
		}
		if opened, err = wire.OpenMail(m, priv); err != nil || wire.CheckAddress(opened.Address) != nil {
			return ErrNotFound
		}
		if m.Kind != "request" {
			return nil
		}
		from := s.Members[m.From]
		if from == nil {
			return ErrNotFound // its sender left or was banned since
		}
		key, _ := wire.Decode64(from.Key, 32)
		hash := sha256Sum(key)
		if f, err := tx.Forgotten(hash); err != nil || f != nil && f.Blocked || slices.Contains(g.Refused, m.From) {
			return cmpErr(err, ErrNotFound) // blocked or refused: dropped (core section 7.4)
		}
		if p, err := tx.PeerByKey(key); err != nil || p != nil {
			if p != nil && !slices.Contains(p.Groups, id) {
				// Already linked: the group is one more source.
				p.Groups = append(p.Groups, id)
				if err := tx.PutPeer(*p); err != nil {
					return err
				}
				return e.refreshLinks(tx)
			}
			return err
		}
		commitment, _ := wire.Decode64(opened.NextCommitment, 32)
		req = GroupRequest{Mail: m.ID, From: m.From, Name: wire.CleanText(opened.Name, 80), Address: opened.Address,
			NextCommitment: commitment, Inbound: opened.Inbound, Pins: wire.CleanPins(opened.Certificates), Received: e.now()}
		if g.Accept && e.waited(s, m.From) {
			accept = true
			return nil
		}
		if !slices.ContainsFunc(g.Requests, func(r GroupRequest) bool { return r.Mail == m.ID }) {
			g.Requests = append(g.Requests, req)
			waiting = &GroupRequestWaiting{Group: id, GroupName: s.Name, Mail: m.ID, Member: m.From,
				Name: req.Name, Fingerprint: wire.Fingerprint(key)}
		}
		return gt.PutGroup(*g)
	})
	if err != nil {
		return
	}
	if waiting != nil {
		e.notify(*waiting)
	}
	switch {
	case m.Kind == "request" && accept:
		if err := e.answerRequest(ctx, id, req); err != nil {
			e.log.Info("msfp: a group request couldn't be accepted", "group", id, "error", err)
		}
	case m.Kind == "answer":
		commitment, _ := wire.Decode64(opened.NextCommitment, 32)
		if err := e.groupPair(ctx, id, m.From, m.ID, opened.Address, commitment, opened.Inbound, wire.CleanPins(opened.Certificates)); err != nil {
			e.log.Info("msfp: group-pair after an answer failed", "group", id, "error", err)
		}
	}
}

// afterEvents applies what kept events change beyond the list: bans, a
// dissolution, members gone, roles and offers. tx is the transaction they
// were kept in.
func (e *Engine) afterEvents(tx Tx, g *GroupRecord, s *wire.GroupState, effects *[]func()) error {
	id := g.ID
	switch {
	case s.Dissolved:
		if effects != nil {
			keep := s.Keep
			*effects = append(*effects, func() {
				e.background(func(ctx context.Context) { _ = e.dropGroup(ctx, id, keep, "dissolved", "") })
			})
		}
		return nil
	case s.Banned[g.Me] != "":
		e.log.Info("msfp: this server was banned from a group", "group", id, "reason", s.Reasons[g.Me])
		if effects != nil {
			reason := s.Reasons[g.Me]
			*effects = append(*effects, func() {
				e.background(func(ctx context.Context) { _ = e.dropGroup(ctx, id, false, "banned", reason) })
			})
		}
		return nil
	}
	// Links with servers no longer members lose this group as a source.
	peers, err := tx.Peers()
	if err != nil {
		return err
	}
	for _, p := range peers {
		if !slices.Contains(p.Groups, id) {
			continue
		}
		in := false
		for _, m := range s.Members {
			if isMember(m, &p) {
				in = true
			}
		}
		if in {
			continue
		}
		p.Groups = slices.DeleteFunc(p.Groups, func(x string) bool { return x == id })
		if p.State == Paired && !holds(&p, false) {
			// Banned, or left: a link nothing else holds ends (core
			// section 7.7); one the leaver asked to keep is held by its ask.
			pid := p.ID
			if effects != nil {
				*effects = append(*effects, func() {
					e.background(func(ctx context.Context) { _ = e.End(ctx, pid) })
				})
			}
		}
		if err := tx.PutPeer(p); err != nil {
			return err
		}
	}
	return e.refreshLinks(tx)
}

// refreshLinks copies onto every link what its groups give it: their
// offers and pools (groups section 9), and moves its offers' change number
// when they change.
func (e *Engine) refreshLinks(tx Tx) error { return e.refreshLinksAt(tx, e.own.seqSeen.Load()) }

// refreshLinksAt is refreshLinks with the change number the offers were
// set after (seqMark).
func (e *Engine) refreshLinksAt(tx Tx, mark int64) error {
	gt, err := groupsTx(tx)
	if err != nil {
		return nil
	}
	groups, err := gt.Groups()
	if err != nil {
		return err
	}
	byID := map[string]GroupRecord{}
	for _, g := range groups {
		byID[g.ID] = g
	}
	peers, err := tx.Peers()
	if err != nil {
		return err
	}
	for _, p := range peers {
		var offers, local []string
		var pools []GroupPool
		for _, id := range p.Groups {
			g, ok := byID[id]
			if !ok {
				continue
			}
			for _, l := range g.Offers {
				if !slices.Contains(offers, l) {
					offers = append(offers, l)
				}
				if g.Local && !slices.Contains(local, l) {
					local = append(local, l)
				}
			}
			pools = append(pools, GroupPool{Group: id, Limits: g.Pool})
		}
		slices.Sort(offers)
		slices.Sort(local)
		if slices.Equal(offers, p.GroupOffers) && slices.Equal(local, p.GroupLocal) && slices.Equal(pools, p.Pools) {
			continue
		}
		// What it is offered, local titles included: a whole reading
		// follows (MSFP catalog section 5).
		changed := !slices.Equal(offers, p.GroupOffers) || !slices.Equal(local, p.GroupLocal)
		p.GroupOffers, p.GroupLocal, p.Pools = offers, local, pools
		if changed {
			if p.OffersSeq, err = e.offersSeq(tx, mark); err != nil {
				return err
			}
		}
		if err := tx.PutPeer(p); err != nil {
			return err
		}
	}
	return nil
}

// localOffered are the host libraries whose local titles a link is
// shared: those its individual relation offers when it shares local
// titles, and those of its groups that do (MSFP catalog section 5, groups
// section 9: a relation that allows it, and offers the library).
func localOffered(p *PeerRecord) []string {
	out := slices.Clone(p.GroupLocal)
	if p.individual() && p.Local {
		for _, l := range p.Offers {
			if !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// offered are the host libraries a link is offered: by hand, and through
// its groups (their union).
func offered(p *PeerRecord) []string {
	out := slices.Clone(p.GroupOffers)
	if p.individual() {
		for _, l := range p.Offers {
			if !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// pools are the pools a link's playbacks may count against, in order:
// its own limits when paired by hand, then its groups' (groups section 9).
func pools(p *PeerRecord) []GroupPool {
	var out []GroupPool
	if p.individual() {
		out = append(out, GroupPool{Group: "", Limits: p.Limits})
	}
	return append(out, p.Pools...)
}

// poolKey is the key a pool's playbacks are counted under.
func poolKey(p *PeerRecord, pool GroupPool) string {
	if pool.Group == "" {
		return p.ID
	}
	return "group:" + pool.Group
}

// ---- passing on (groups section 6) ------------------------------------------------

// groupPeers are the members of a group this server is paired with
// through it, but except.
func (e *Engine) groupPeers(ctx context.Context, id, except string) []PeerRecord {
	var out []PeerRecord
	_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
		peers, err := tx.Peers()
		for _, p := range peers {
			if p.State == Paired && p.ID != except && slices.Contains(p.Groups, id) && e.on(&p, wire.PartGroups) {
				out = append(out, p)
			}
		}
		return err
	})
	return out
}

// pushSoon passes new events and mail on to the group's paired members,
// in the background.
func (e *Engine) pushSoon(id string, events []StoredEvent, mail []StoredMail, except string) {
	if len(events) == 0 && len(mail) == 0 {
		return
	}
	e.pushTo(e.groupPeers(context.Background(), id, except), id, events, mail)
}

// pushTo pushes events and mail to these peers, in the background.
func (e *Engine) pushTo(to []PeerRecord, id string, events []StoredEvent, mail []StoredMail) {
	if len(events) == 0 && len(mail) == 0 {
		return
	}
	body := groupPushRequest{Group: id}
	for _, ev := range events {
		body.Events = append(body.Events, wire.Envelope{Payload: ev.Event, Signature: ev.Signature})
	}
	for _, m := range mail {
		body.Mail = append(body.Mail, wire.Envelope{Mail: m.Mail, Signature: m.Signature})
	}
	e.background(func(ctx context.Context) {
		for _, p := range to {
			if p.Inbound != InboundYes {
				continue
			}
			var ans struct{}
			if _, err := e.call(ctx, &p, callGroupPush, body, &ans, true); err != nil {
				e.log.Debug("msfp: a group push failed", "peer", p.ID, "error", err)
			}
		}
	})
}

// syncGroups reads each group's list from one of its paired members, at
// random (run from the engine's loop), and accepts the requests that waited
// their days where this server accepts members by itself.
func (e *Engine) syncGroups(ctx context.Context) { e.syncGroupsNow(ctx, false) }

func (e *Engine) syncGroupsNow(ctx context.Context, force bool) {
	var groups []GroupRecord
	_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return nil
		}
		groups, err = gt.Groups()
		return err
	})
	every := groupSyncEvery
	if e.saysUncallable() {
		every = groupSyncUncallable
	}
	for _, g := range groups {
		e.acceptWaited(ctx, g.ID)
		if !force && e.now().Sub(g.Synced) < every-every/5+time.Duration(rand.Int64N(int64(every/5*2))) {
			continue
		}
		peers := e.groupPeers(ctx, g.ID, "")
		if g.Via != "" {
			if p, err := e.peerRecord(ctx, g.Via); err == nil && p != nil && p.State == Paired {
				peers = append(peers, *p)
			}
		}
		if len(peers) == 0 {
			continue
		}
		p := peers[rand.IntN(len(peers))]
		if err := e.syncWith(ctx, g.ID, &p); err != nil {
			e.log.Debug("msfp: a group sync failed", "group", g.ID, "peer", p.ID, "error", err)
		}
	}
}

// backInIfMember ends a member's way back in a group it was already in.
func (e *Engine) backInIfMember(ctx context.Context, id string) {
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil || g.Via == "" || e.stateOf(g).Members[g.Me] == nil {
			return err
		}
		if err := e.backIn(tx, g); err != nil {
			return err
		}
		return gt.PutGroup(*g)
	})
	if err != nil {
		e.log.Info("msfp: back in a group: the link couldn't gain it", "group", id, "error", err)
	}
}

// syncGroupsSoon runs, in the background, a group-sync with a peer that
// just took this server's new key, for each group they share: it pushes
// the events the peer lacks. Those signed for the change (rotated; revoked,
// after a stolen key) were pushed while the peer still knew the old key, or
// held this server suspended (core section 12.6), and may have been
// refused; without this, they and every event after them would wait for
// the next round of group-sync.
func (e *Engine) syncGroupsSoon(peer string) {
	e.background(func(ctx context.Context) {
		p, err := e.peerRecord(ctx, peer)
		if err != nil || p == nil || p.State != Paired {
			return
		}
		for _, id := range p.Groups {
			if err := e.syncWith(ctx, id, p); err != nil {
				e.log.Debug("msfp: a group sync after a rotation failed", "group", id, "peer", peer, "error", err)
			}
		}
	})
}

// syncWith calls group-sync on a peer for a group, keeps what it lacked,
// and pushes it what it lacks.
func (e *Engine) syncWith(ctx context.Context, id string, p *PeerRecord) error {
	for range 20 {
		var have map[string]int64
		var mailIDs []string
		var kept []StoredEvent
		var keptMail []StoredMail
		err := e.cfg.Store.Read(ctx, func(tx Tx) error {
			gt, err := groupsTx(tx)
			if err != nil {
				return err
			}
			g, err := gt.Group(id)
			if err != nil || g == nil {
				return cmpErr(err, ErrNotFound)
			}
			have = e.stateOf(g).Have()
			for _, m := range g.Mail {
				mailIDs = append(mailIDs, m.ID)
			}
			kept, keptMail = g.Events, g.Mail
			return nil
		})
		if err != nil {
			return err
		}
		var ans groupSyncAnswer
		r, err := e.call(ctx, p, callGroupSync, groupSyncRequest{Group: id, Have: have, Mail: mailIDs}, &ans, true)
		if err != nil {
			return err
		}
		if r.status != http.StatusOK {
			return ErrUnreachable.with(errors.New(r.problem))
		}
		if err := e.receive(ctx, id, ans.Events, ans.Mail, p.ID); err != nil {
			return err
		}
		// What it lacks: pushed back.
		var push groupPushRequest
		push.Group = id
		for _, ev := range kept {
			var head struct {
				Member string `json:"member"`
				Seq    int64  `json:"seq"`
			}
			b, _ := wire.Decode64(ev.Event, -1)
			_ = wire.DecodeEntry(b, &head)
			if head.Seq > ans.Have[head.Member] && len(push.Events) < maxSyncEvents {
				push.Events = append(push.Events, wire.Envelope{Payload: ev.Event, Signature: ev.Signature})
			}
		}
		for _, m := range keptMail {
			if len(push.Mail) < maxPushMail {
				push.Mail = append(push.Mail, wire.Envelope{Mail: m.Mail, Signature: m.Signature})
			}
		}
		if len(push.Events) > 0 && p.Inbound == InboundYes {
			var none struct{}
			_, _ = e.call(ctx, p, callGroupPush, push, &none, true)
		}
		_ = e.updateGroup(ctx, id, func(g *GroupRecord) { g.Synced = e.now() })
		if !ans.More {
			return nil
		}
	}
	return nil
}

type groupSyncRequest struct {
	Group string           `json:"group"`
	Have  map[string]int64 `json:"have"`
	Mail  []string         `json:"mail,omitempty"`
}

type groupSyncAnswer struct {
	Events []wire.Envelope  `json:"events"`
	Mail   []wire.Envelope  `json:"mail"`
	Have   map[string]int64 `json:"have"`
	More   bool             `json:"more"`
}

type groupPushRequest struct {
	Group  string          `json:"group"`
	Events []wire.Envelope `json:"events,omitempty"`
	Mail   []wire.Envelope `json:"mail,omitempty"`
}

type groupPairRequest struct {
	Group          string `json:"group"`
	Answers        string `json:"answers"`
	Address        string `json:"address"`
	Name           string `json:"name,omitempty"`
	NextCommitment string `json:"nextCommitment"`
	Inbound        *bool  `json:"inbound,omitempty"`
}

type groupPairAnswer struct {
	Name           string `json:"name"`
	NextCommitment string `json:"nextCommitment"`
	Inbound        *bool  `json:"inbound,omitempty"`
}

// ---- the calls served (groups section 8) -----------------------------------------

// coMember says whether a peer is a member of a group this server is in,
// and gives the group.
func (e *Engine) coMember(req *request, id string) (*GroupRecord, bool) {
	if req.peer == nil || req.peer.State != Paired {
		return nil, false
	}
	var g *GroupRecord
	in := false
	_ = e.cfg.Store.Read(req.r.Context(), func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err = gt.Group(id)
		if err != nil || g == nil {
			return err
		}
		if g.Via == req.peer.ID {
			// The inviter of a newcomer that has no list yet.
			in = true
			return nil
		}
		s := e.stateOf(g)
		for _, m := range s.Members {
			if isMember(m, req.peer) {
				in = true
			}
		}
		return nil
	})
	return g, in && g != nil
}

func (e *Engine) groupSync(req *request) response {
	var body groupSyncRequest
	if wire.DecodeJSON(req.body, &body) != nil || len(body.Mail) > maxKeptMail {
		return refuse(probInvalid)
	}
	g, ok := e.coMember(req, body.Group)
	if !ok || !e.on(req.peer, wire.PartGroups) {
		return refuse(probNotFound)
	}
	ans := groupSyncAnswer{Events: []wire.Envelope{}, Mail: []wire.Envelope{}, Have: e.stateOf(g).Have()}
	for _, ev := range g.Events {
		var head struct {
			Member string `json:"member"`
			Seq    int64  `json:"seq"`
		}
		b, _ := wire.Decode64(ev.Event, -1)
		_ = wire.DecodeEntry(b, &head)
		if head.Seq > body.Have[head.Member] {
			if len(ans.Events) == maxSyncEvents {
				ans.More = true
				break
			}
			ans.Events = append(ans.Events, wire.Envelope{Payload: ev.Event, Signature: ev.Signature})
		}
	}
	for _, m := range g.Mail {
		if !slices.Contains(body.Mail, m.ID) && m.Expires > e.now().Unix() {
			ans.Mail = append(ans.Mail, wire.Envelope{Mail: m.Mail, Signature: m.Signature})
		}
	}
	return answer(ans)
}

func (e *Engine) groupPush(req *request) response {
	var body groupPushRequest
	if wire.DecodeJSON(req.body, &body) != nil || len(body.Events) > maxSyncEvents || len(body.Mail) > maxPushMail {
		return refuse(probInvalid)
	}
	g, ok := e.coMember(req, body.Group)
	if !ok || !e.on(req.peer, wire.PartGroups) {
		return refuse(probNotFound)
	}
	if err := e.receive(req.r.Context(), body.Group, body.Events, body.Mail, req.peer.ID); err != nil {
		return refuse(probInternal)
	}
	if g.Via == req.peer.ID {
		// Pushed by the maker this member used a code with: its yes
		// (section 2, "A member back in").
		e.backInIfMember(req.r.Context(), body.Group)
	}
	return answer(struct{}{})
}

// groupPairServed is group-pair, served: the caller is a member answering
// a request this server sent, or the member whose answer it received.
func (e *Engine) groupPairServed(req *request) response {
	var body groupPairRequest
	if wire.DecodeJSON(req.body, &body) != nil || wire.CheckAddress(body.Address) != nil {
		return e.pairRefused(req)
	}
	commitment, ok := wire.Decode64(body.NextCommitment, 32)
	if !ok {
		return e.pairRefused(req)
	}
	inbound := body.Inbound == nil || *body.Inbound
	key := req.v.Key
	member, used, err := e.matchSent(req.r.Context(), body.Group, key, body.Answers)
	switch {
	case errors.Is(err, errUsed):
		return response{status: http.StatusConflict}
	case err != nil:
		return e.pairRefused(req)
	}
	_ = used
	if _, err := e.linkThrough(req.r.Context(), body.Group, member, key, body.Address, body.Name, commitment, inbound, nil); err != nil {
		return refuse(probInternal)
	}
	t := !e.saysUncallable()
	return answer(groupPairAnswer{Name: e.name(), NextCommitment: wire.Encode64(e.commitment()), Inbound: &t})
}

var errUsed = errors.New("msfp: a mail already used")

// matchSent finds, for a group-pair from key, the mail it answers: one
// this server sent to that member, not used before; it is then used.
func (e *Engine) matchSent(ctx context.Context, id string, key []byte, mailID string) (string, bool, error) {
	var member string
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		if slices.Contains(g.Used, mailID) {
			return errUsed
		}
		s := e.stateOf(g)
		k := wire.Encode64(key)
		for _, m := range s.Members {
			if m.Key == k {
				member = m.ID
			}
		}
		if member == "" {
			return ErrNotMember
		}
		hash := sha256Sum(key)
		if f, err := tx.Forgotten(hash); err != nil || f != nil && f.Blocked {
			return cmpErr(err, ErrNotFound) // blocked: kept away from groups (core section 7.4)
		}
		i := slices.IndexFunc(g.Sent, func(x SentMail) bool { return x.ID == mailID && x.To == member })
		if i < 0 {
			return ErrNotFound
		}
		g.Sent = slices.Delete(g.Sent, i, i+1)
		g.Used = append(g.Used, mailID)
		return gt.PutGroup(*g)
	})
	return member, true, err
}

// linkThrough makes (or adds the group to) the link with a member, paired
// at once (groups section 4.2), and says hello to it.
func (e *Engine) linkThrough(ctx context.Context, id, member string, key []byte, address, name string, commitment []byte, inbound bool, pins []string) (string, error) {
	var pid string
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		p, err := tx.PeerByKey(key)
		if err != nil {
			return err
		}
		now := e.now().Truncate(time.Second)
		if p == nil {
			// Linked again: an ended key comes off the forgotten list
			// (core section 7.4); a blocked one never gets this far.
			if err := tx.Unforget(sha256Sum(key)); err != nil {
				return err
			}
			newID, err := e.newID("p")
			if err != nil {
				return err
			}
			p = &PeerRecord{ID: newID, Key: key, Name: cleanName(name, address), Created: now, Seen: now,
				State: Paired, WasPaired: true, Paired: now, Address: address, OurAddress: e.ourHost(), Inbound: InboundYes,
				Commitment: commitment, CommitmentFrom: "pair", CommitmentTold: true, Limits: *e.cfg.NewPairing, NotByHand: true,
				Pins: pins}
			if !inbound {
				p.Inbound = InboundNo
			}
		}
		if !slices.Contains(p.Groups, id) {
			p.Groups = append(p.Groups, id)
		}
		pid = p.ID
		if err := tx.PutPeer(*p); err != nil {
			return err
		}
		return e.refreshLinks(tx)
	})
	if err != nil {
		return "", err
	}
	e.notify(PeerPaired{Peer: pid, Name: name})
	e.background(func(ctx context.Context) {
		if _, err := e.Check(ctx, pid); err != nil {
			e.log.Info("msfp: a member paired through a group couldn't be told hello yet", "peer", pid, "error", err)
		}
	})
	return pid, nil
}

// groupPair calls group-pair on a member at address, answering mailID,
// and makes the link.
func (e *Engine) groupPair(ctx context.Context, id, member, mailID, address string, commitment []byte, inbound bool, pins []string) error {
	var key []byte
	err := e.withState(ctx, id, func(g *GroupRecord, s *wire.GroupState) error {
		m := s.Members[member]
		if m == nil {
			return ErrNotMember
		}
		key, _ = wire.Decode64(m.Key, 32)
		return nil
	})
	if err != nil {
		return err
	}
	t := !e.saysUncallable()
	body := groupPairRequest{Group: id, Answers: mailID, Address: e.named(e.address()), Name: e.name(),
		NextCommitment: wire.Encode64(e.commitment()), Inbound: &t}
	// Its certificate is checked as for any call after pairing: trusted,
	// or named by the pins its mail carried (MSFP groups section 8.3).
	tmp := &PeerRecord{Key: key, Address: address, Inbound: InboundYes, Pins: pins}
	var ans groupPairAnswer
	r, err := e.exchange(ctx, tmp, callGroupPair, body, &ans, e.current())
	if err != nil {
		return err
	}
	if r.status != http.StatusOK {
		return ErrUnreachable.with(errors.New(r.problem))
	}
	_, err = e.linkThrough(ctx, id, member, key, address, ans.Name, commitment, inbound, pins)
	return err
}

// ---- joining (groups section 4) ---------------------------------------------------

// joinVia notes, on redeeming a group's code, the group this server joins
// through the code's maker: a record without events until the list comes.
func (e *Engine) joinVia(tx Tx, c wire.Code, peer string) error {
	gt, err := groupsTx(tx)
	if err != nil {
		return nil
	}
	id := wire.Encode64(c.Group)
	if g, err := gt.Group(id); err != nil {
		return err
	} else if g != nil {
		// A member back in (section 2): the link through the maker
		// gains the group once it's made.
		g.Via, g.Joining = peer, e.now()
		return gt.PutGroup(*g)
	}
	g := GroupRecord{ID: id, Me: wire.Encode64(e.current().Public()), Via: peer, Pool: *e.cfg.NewPairing}
	if err := e.newMailKey(&g); err != nil {
		return err
	}
	return gt.PutGroup(g)
}

// paired is the hook of a pairing that became paired: a newcomer reads the
// list from its inviter; the inviter, accepting, signs joined.
func (e *Engine) paired(ctx context.Context, p *PeerRecord) {
	if p.JoiningGroup != "" {
		id := p.JoiningGroup
		var err error
		if g, gerr := e.Group(ctx, id); gerr == nil && slices.ContainsFunc(g.Members, func(m GroupMemberView) bool { return m.Peer == p.ID }) {
			// A member back in (section 2): the link gains the group.
		} else {
			err = e.act(ctx, id, wire.Event{Type: wire.EventJoined, NewMember: wire.Encode64(p.Key), Name: p.Name})
		}
		if err != nil {
			e.log.Info("msfp: a server couldn't be added to the group its code invited it to", "peer", p.ID, "error", err)
		}
		_ = e.updatePeer(ctx, p.ID, func(q *PeerRecord) {
			q.JoiningGroup = ""
			if err == nil && !slices.Contains(q.Groups, id) {
				q.Groups = append(q.Groups, id)
			}
			if err != nil {
				// No relation: this side asks for the individual one
				// (groups section 2), the other side's operator answers.
				q.NotByHand = false
			}
		})
		_ = e.cfg.Store.Update(ctx, func(tx Tx) error { return e.refreshLinks(tx) })
		return
	}
	var via []string
	_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return nil
		}
		gs, err := gt.Groups()
		for _, g := range gs {
			if g.Via == p.ID {
				via = append(via, g.ID)
			}
		}
		return err
	})
	for _, id := range via {
		_ = e.updatePeer(ctx, p.ID, func(q *PeerRecord) {
			if !slices.Contains(q.Groups, id) {
				q.Groups = append(q.Groups, id)
			}
		})
		e.backInIfMember(ctx, id)
		if err := e.syncWith(ctx, id, p); err != nil {
			e.log.Info("msfp: the group's list couldn't be read from the inviter yet", "group", id, "error", err)
		}
	}
}

// joined is a newcomer's next steps, once the list names it: its
// profile, then its requests.
func (e *Engine) joined(ctx context.Context, id string) {
	var sent StoredEvent
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		if m := s.Members[g.Me]; m == nil || m.EncryptionKey != nil {
			return errors.New("no profile to sign")
		}
		if sent, err = e.profile(g, s); err != nil {
			return err
		}
		g.Events = append(g.Events, sent)
		if g.Via != "" {
			// The inviter's link gains the group as a source (a peer
			// already, by group-join, or paired by its code).
			if p, err := tx.Peer(g.Via); err != nil {
				return err
			} else if p != nil && !slices.Contains(p.Groups, id) {
				p.Groups = append(p.Groups, id)
				if err := tx.PutPeer(*p); err != nil {
					return err
				}
			}
		}
		g.Via, g.Joining = "", time.Time{}
		if err := gt.PutGroup(*g); err != nil {
			return err
		}
		return e.refreshLinks(tx)
	})
	if err != nil {
		return
	}
	e.pushSoon(id, []StoredEvent{sent}, nil, "")
	if err := e.sendRequests(ctx, id, nil); err != nil {
		e.log.Info("msfp: a newcomer's requests couldn't be sent", "group", id, "error", err)
	}
}

// groupMember says whether key is the current key of a member of one of
// this server's groups, not banned (groups section 8: who is heard).
func (e *Engine) groupMember(r *http.Request, key []byte) bool {
	k := wire.Encode64(key)
	found := false
	_ = e.cfg.Store.Read(r.Context(), func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return nil
		}
		gs, err := gt.Groups()
		for i := range gs {
			for _, m := range e.stateOf(&gs[i]).Members {
				if m.Key == k {
					found = true
				}
			}
		}
		return err
	})
	return found
}

// poolRoom says whether one of a link's pools has room now for a
// playback of purpose (room), and whether one allows any at all (open).
func (e *Engine) poolRoom(p *PeerRecord, purpose string, audio bool) (room, open bool) {
	for _, pool := range pools(p) {
		r, o := e.roomIn(poolKey(p, pool), pool.Limits, purpose, audio)
		room, open = room || r, open || o
	}
	return room, open
}

// admitPools admits a playback of purpose in the first of a link's pools
// with room (groups section 9, playback section 5): each playback counts
// against exactly one, and a day's counts are kept in the pool it
// counted against.
func (e *Engine) admitPools(p *PeerRecord, purpose string, audio bool, method string) (*countSlot, error) {
	err := error(ErrStreamLimitReached)
	for _, pool := range pools(p) {
		s, e2 := e.admitIn(poolKey(p, pool), pool.Limits, purpose, audio, method)
		if e2 == nil {
			return s, nil
		}
		err = e2
	}
	return nil, err
}

// generous is the most generous of a link's pools (what libraries tells
// it, groups section 9): the most streams, transcodes, downloads and
// scans, and the highest caps (none, 0, above all; a daily download cap
// only among pools that allow downloads).
func generous(p *PeerRecord) Limits {
	ps := pools(p)
	if len(ps) == 0 {
		return Limits{}
	}
	out := ps[0].Limits
	most := func(a, b int) int {
		if a == 0 || b == 0 {
			return 0
		}
		return max(a, b)
	}
	for _, pool := range ps[1:] {
		l := pool.Limits
		out.Streams = max(out.Streams, l.Streams)
		out.Transcodes = max(out.Transcodes, l.Transcodes)
		out.AudioStreams = max(out.AudioStreams, l.AudioStreams)
		out.AudioTranscodes = max(out.AudioTranscodes, l.AudioTranscodes)
		out.Audio = out.Audio || l.Audio
		out.BitrateKbps = most(out.BitrateKbps, l.BitrateKbps)
		out.DownloadKbps = most(out.DownloadKbps, l.DownloadKbps)
		switch {
		case l.Downloads == 0:
		case out.Downloads == 0:
			out.DownloadsPerDay = l.DownloadsPerDay
		default:
			out.DownloadsPerDay = most(out.DownloadsPerDay, l.DownloadsPerDay)
		}
		out.Downloads = max(out.Downloads, l.Downloads)
		out.ScansPerDay = max(out.ScansPerDay, l.ScansPerDay)
		out.ScanBytes = max(out.ScanBytes, l.ScanBytes)
	}
	return out
}

// pushAll pushes a group's whole list and mail to one peer: the inviter
// to its newcomer, once paired (groups section 4, step 4).
func (e *Engine) pushAll(ctx context.Context, id, peer string) {
	var body groupPushRequest
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		body.Group = id
		for _, ev := range g.Events {
			body.Events = append(body.Events, wire.Envelope{Payload: ev.Event, Signature: ev.Signature})
		}
		for _, m := range g.Mail {
			if len(body.Mail) < maxPushMail {
				body.Mail = append(body.Mail, wire.Envelope{Mail: m.Mail, Signature: m.Signature})
			}
		}
		return nil
	})
	p, perr := e.peerRecord(ctx, peer)
	if err != nil || perr != nil || p == nil || p.Inbound != InboundYes {
		return
	}
	for len(body.Events) > 0 {
		chunk := body
		chunk.Events = body.Events[:min(len(body.Events), maxSyncEvents)]
		body.Events = body.Events[len(chunk.Events):]
		var none struct{}
		if _, err := e.call(ctx, p, callGroupPush, chunk, &none, true); err != nil {
			e.log.Info("msfp: a newcomer couldn't be sent the group's list; it reads it itself", "peer", peer, "error", err)
			return
		}
		body.Mail = nil
	}
}

// ---- keys (groups section 3.3, rotated and revoked) ------------------------------

// groupIDs are the groups this server is a member of.
func (e *Engine) groupIDs(ctx context.Context) []string {
	var out []string
	_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return nil
		}
		gs, err := gt.Groups()
		for i := range gs {
			if e.stateOf(&gs[i]).Members[gs[i].Me] != nil {
				out = append(out, gs[i].ID)
			}
		}
		return err
	})
	return out
}

// groupsRotated tells this server's groups of its new key: a rotated
// event in each, signed with it (core section 12, groups section 3.3).
func (e *Engine) groupsRotated(ctx context.Context) {
	k := e.ourKeys()
	if k.old == nil {
		return
	}
	rot := wire.SignRotation(*k.old, k.current, k.commitment, e.now())
	body := rot.Body()
	statement, err := marshal(map[string]any{"oldKey": wire.Encode64(rot.Old), "newKey": body.NewKey,
		"nextCommitment": body.NextCommitment, "date": body.Date, "oldSignature": body.OldSignature, "newSignature": body.NewSignature})
	if err != nil {
		return
	}
	for _, id := range e.groupIDs(ctx) {
		if err := e.act(ctx, id, wire.Event{Type: wire.EventRotated, Statement: statement}); err != nil {
			e.log.Warn("msfp: a group couldn't be told of this server's new key", "group", id, "error", err)
		}
	}
}

// groupsRevoked tells this server's groups that its key was stolen: a
// revoked event in each, signed with that key, its last good event the
// last before now.
func (e *Engine) groupsRevoked(ctx context.Context, r wire.Revocation) {
	body := r.Body()
	statement, err := marshal(map[string]any{"key": wire.Encode64(r.Key), "date": body.Date, "signature": body.Signature})
	if err != nil {
		return
	}
	for _, id := range e.groupIDs(ctx) {
		var last int64
		_ = e.withState(ctx, id, func(g *GroupRecord, s *wire.GroupState) error { last = s.Seq(g.Me); return nil })
		if err := e.act(ctx, id, wire.Event{Type: wire.EventRevoked, Statement: statement, LastSeq: &last}); err != nil {
			e.log.Warn("msfp: a group couldn't be told this server's key was stolen", "group", id, "error", err)
		}
	}
}

// isMember says whether a peer is this member: its current key, its
// member ID, or, while either side is between keys, the peer's previous
// key (a rotation the copy hasn't kept yet, or the peer record has).
// memberOf says whether the peer is a member in this state.
func memberOf(s *wire.GroupState, p *PeerRecord) bool {
	for _, m := range s.Members {
		if isMember(m, p) {
			return true
		}
	}
	return false
}

// backIn ends a member's way back (groups section 2, "A member back in"):
// the link through which it used a group's code gains the group as a
// source, as a newcomer's does when the list names it.
func (e *Engine) backIn(tx Tx, g *GroupRecord) error {
	p, err := tx.Peer(g.Via)
	if err != nil || p == nil {
		return err
	}
	if !slices.Contains(p.Groups, g.ID) {
		p.Groups = append(p.Groups, g.ID)
		if err := tx.PutPeer(*p); err != nil {
			return err
		}
	}
	g.Via, g.Joining = "", time.Time{}
	return e.refreshLinks(tx)
}

func isMember(m *wire.GroupMember, p *PeerRecord) bool {
	for _, k := range [][]byte{p.Key, p.PreviousKey} {
		if len(k) == 0 {
			continue
		}
		if e := wire.Encode64(k); e == m.Key || e == m.ID {
			return true
		}
	}
	return false
}

// acceptWaited accepts, where this server accepts members by itself, the
// requests whose senders have now been in the group its days (groups
// section 4.1).
func (e *Engine) acceptWaited(ctx context.Context, id string) {
	var ready []GroupRequest
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil || !g.Accept || len(g.Requests) == 0 {
			return err
		}
		s := e.stateOf(g)
		var keep []GroupRequest
		for _, r := range g.Requests {
			if e.waited(s, r.From) {
				ready = append(ready, r)
			} else {
				keep = append(keep, r)
			}
		}
		if len(ready) == 0 {
			return nil
		}
		g.Requests = keep
		return gt.PutGroup(*g)
	})
	if err != nil {
		return
	}
	for _, r := range ready {
		if err := e.answerRequest(ctx, id, r); err != nil {
			e.log.Info("msfp: a request that waited its days couldn't be accepted", "group", id, "error", err)
		}
	}
}

// ---- group-join (groups sections 2 and 8.4) -------------------------------------

// joinWait is how long a server waits to join through group-join, and a
// join waits for the operator: a code works 30 days at most.
const joinWait = wire.MaxCodeLifetime

type groupJoinRequest struct {
	SecretHash string `json:"secretHash"`
	Proof      string `json:"proof"`
}

// joinPaired redeems a group's code from a peer already paired with this
// server: group-join, through the pairing, instead of pair.
func (e *Engine) joinPaired(ctx context.Context, c wire.Code, p *PeerRecord) (PeerView, error) {
	id := wire.Encode64(c.Group)
	if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil {
			return err
		}
		// A member back in, through a link that isn't through the group
		// (section 2): only if this server can be called, for the maker's
		// push says yes.
		if g != nil && (slices.Contains(p.Groups, id) || e.saysUncallable() || e.stateOf(g).Members[g.Me] == nil) {
			return ErrAlreadyMember
		}
		return nil
	}); err != nil {
		return PeerView{}, err
	}
	if !e.on(p, wire.PartGroups) {
		return PeerView{}, ErrNotSupported
	}
	body := groupJoinRequest{SecretHash: wire.Encode64(wire.SecretHash(c.Secret)), Proof: wire.JoinProof(c.Secret, e.current().Public())}
	var none struct{}
	r, err := e.call(ctx, p, callGroupJoin, body, &none, false)
	switch {
	case errors.Is(err, ErrNotFound):
		return PeerView{}, ErrCodeRefused
	case err != nil:
		return PeerView{}, err
	case r.status == http.StatusConflict:
		return PeerView{}, ErrAlreadyMember
	case r.status != http.StatusOK:
		return PeerView{}, ErrCodeRefused
	}
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		if err := e.joinVia(tx, c, p.ID); err != nil {
			return err
		}
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		g.Joining = e.now()
		return gt.PutGroup(*g)
	})
	if err != nil {
		return PeerView{}, err
	}
	e.log.Info("msfp: a group's code used with a peer, waiting for its operator", "peer", p.ID, "group", id)
	return e.view(p), nil
}

// groupJoin is a peer already paired with this server using its code for a
// group (groups section 8.4).
func (e *Engine) groupJoin(req *request) response {
	var body groupJoinRequest
	if wire.DecodeJSON(req.body, &body) != nil {
		return refuse(probInvalid)
	}
	hash, ok := wire.Decode64(body.SecretHash, 32)
	if !ok || req.peer == nil || req.peer.State != Paired || !e.on(req.peer, wire.PartGroups) {
		return refuse(probNotFound)
	}
	var waiting *GroupJoinWaiting
	err := e.cfg.Store.Update(req.r.Context(), func(tx Tx) error {
		waiting = nil
		gt, err := groupsTx(tx)
		if err != nil {
			return ErrNotFound
		}
		// The code, the proof, then the right to invite.
		c, err := tx.CodeByHash(hash)
		if err != nil {
			return err
		}
		if c == nil || c.Group == "" || !c.Expires.After(e.now()) {
			return ErrNotFound
		}
		secret, err := e.cfg.Sealer.Open(c.Secret, sealCode)
		if err != nil || !wire.CheckJoinProof(secret, req.v.Key, body.Proof) {
			return ErrNotFound
		}
		g, err := gt.Group(c.Group)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		s := e.stateOf(g)
		if r := role(s, g.Me); s.Members[g.Me] == nil || r == "member" || s.Dissolved {
			return ErrNotFound
		}
		member := wire.Encode64(req.peer.Key)
		// A member is back in through a link that isn't through the
		// group (section 2), if it can be called: it learns of the yes
		// from this server's push.
		in := memberOf(s, req.peer) && (slices.Contains(req.peer.Groups, g.ID) || req.peer.Inbound != InboundYes)
		if in || s.Banned[member] != "" || slices.ContainsFunc(g.Joins, func(j GroupJoin) bool { return j.Peer == req.peer.ID }) {
			return ErrAlreadyMember
		}
		if c.Uses > 1 {
			c.Uses--
			if err := tx.PutCode(*c); err != nil {
				return err
			}
		} else if err := tx.DeleteCode(c.ID); err != nil {
			return err
		}
		g.Joins = append(g.Joins, GroupJoin{Peer: req.peer.ID, Received: e.now()})
		waiting = &GroupJoinWaiting{Group: g.ID, GroupName: s.Name, Peer: req.peer.ID, Name: req.peer.Name, Fingerprint: wire.Fingerprint(req.peer.Key)}
		return gt.PutGroup(*g)
	})
	switch {
	case errors.Is(err, ErrNotFound):
		return refuse(probNotFound)
	case errors.Is(err, ErrAlreadyMember):
		return response{status: http.StatusConflict}
	case err != nil:
		e.log.Error("msfp: a group's code couldn't be used by a peer", "error", err)
		return refuse(probInternal)
	}
	e.notify(*waiting)
	return answer(struct{}{})
}

// AcceptJoin accepts a peer waiting to join a group through this server
// (GroupView.Joins): it signs joined, and sends the peer the list (groups
// section 2). Errors: ErrNotFound (no such group or peer waiting),
// ErrNoRight, ErrNotPaired.
func (e *Engine) AcceptJoin(ctx context.Context, id, peer string) error {
	g, err := e.Group(ctx, id)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(g.Joins, func(j GroupJoinView) bool { return j.Peer == peer }) {
		return ErrNotFound
	}
	p, err := e.peerRecord(ctx, peer)
	if err != nil {
		return err
	}
	if p.State != Paired {
		return ErrNotPaired
	}
	back := slices.ContainsFunc(g.Members, func(m GroupMemberView) bool { return m.Peer == p.ID })
	if !back {
		if err := e.act(ctx, id, wire.Event{Type: wire.EventJoined, NewMember: wire.Encode64(p.Key), Name: p.Name}); err != nil {
			return err
		}
	}
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		if gr, err := gt.Group(id); err != nil {
			return err
		} else if gr != nil {
			gr.Joins = slices.DeleteFunc(gr.Joins, func(j GroupJoin) bool { return j.Peer == peer })
			if err := gt.PutGroup(*gr); err != nil {
				return err
			}
		}
		q, err := tx.Peer(peer)
		if err != nil || q == nil {
			return err
		}
		if !slices.Contains(q.Groups, id) {
			q.Groups = append(q.Groups, id)
		}
		if err := tx.PutPeer(*q); err != nil {
			return err
		}
		return e.refreshLinks(tx)
	})
	if err != nil {
		return err
	}
	e.background(func(ctx context.Context) { e.pushAll(ctx, id, peer) })
	return nil
}

// RefuseJoin refuses a peer waiting to join a group through this server:
// nothing is sent (groups section 2). Error: ErrNotFound.
func (e *Engine) RefuseJoin(ctx context.Context, id, peer string) error {
	found := false
	err := e.updateGroup(ctx, id, func(g *GroupRecord) {
		n := len(g.Joins)
		g.Joins = slices.DeleteFunc(g.Joins, func(j GroupJoin) bool { return j.Peer == peer })
		found = len(g.Joins) < n
	})
	if err == nil && !found {
		return ErrNotFound
	}
	return err
}

// tidyGroups drops what waited too long for group-join: this server's own
// joins, and peers' joins waiting for its operator.
func (e *Engine) tidyGroups(ctx context.Context) error {
	var drop []string
	cutoff := e.now().Add(-joinWait)
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return nil
		}
		gs, err := gt.Groups()
		if err != nil {
			return err
		}
		for _, g := range gs {
			if !g.Joining.IsZero() && g.Joining.Before(cutoff) && len(g.Events) == 0 {
				drop = append(drop, g.ID)
				continue
			}
			n := len(g.Joins)
			g.Joins = slices.DeleteFunc(g.Joins, func(j GroupJoin) bool { return j.Received.Before(cutoff) })
			if len(g.Joins) < n {
				if err := gt.PutGroup(g); err != nil {
					return err
				}
			}
		}
		return nil
	})
	for _, id := range drop {
		e.log.Info("msfp: gave up joining a group: its inviter's operator didn't accept within 30 days", "group", id)
		_ = e.dropGroup(ctx, id, false, "left", "")
	}
	return err
}
