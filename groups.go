package msfp

import (
	"context"
	"crypto/sha256"
	"errors"
	"math/rand/v2"
	"net/http"
	"slices"
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
	Pool      Limits
	Dissolved bool
	// Joining: the list hasn't arrived from the inviter yet.
	Joining bool
}

// GroupMemberView is a member, as the panel lists it (groups section 10).
type GroupMemberView struct {
	ID, Name, Fingerprint string
	Role                  string
	InvitedBy             string
	Joined                time.Time
	// Peer is the link with it, "" for none.
	Peer string
}

// GroupBanView is a banned server, and the reason given.
type GroupBanView struct{ ID, Reason string }

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

// stateOf replays a group's kept events.
func (e *Engine) stateOf(g *GroupRecord) *wire.GroupState {
	s := wire.NewGroupState()
	for _, ev := range g.Events {
		s.Apply(wire.Envelope{Payload: ev.Event, Signature: ev.Signature}, e.now().Add(time.Hour*24*365*100))
	}
	return s
}

func (e *Engine) groupView(g *GroupRecord, peers []PeerRecord) GroupView {
	s := e.stateOf(g)
	v := GroupView{ID: g.ID, Name: s.Name, Settings: s.Settings, Me: g.Me, Owner: s.Owner, Heir: s.Heir,
		Role: role(s, g.Me), Accept: g.Accept, Offers: slices.Clone(g.Offers), Pool: g.Pool, Dissolved: s.Dissolved,
		Joining: s.Members[g.Me] == nil && len(g.Events) == 0}
	for _, m := range s.Members {
		key, _ := wire.Decode64(m.Key, 32)
		mv := GroupMemberView{ID: m.ID, Name: m.Name, Fingerprint: wire.Fingerprint(key), Role: role(s, m.ID),
			InvitedBy: m.InvitedBy, Joined: time.Unix(m.Joined, 0).UTC()}
		for _, p := range peers {
			if wire.Encode64(p.Key) == m.Key {
				mv.Peer = p.ID
			}
		}
		v.Members = append(v.Members, mv)
	}
	slices.SortFunc(v.Members, func(a, b GroupMemberView) int { return int(a.Joined.Sub(b.Joined)) })
	for id, ban := range s.Reasons {
		v.Banned = append(v.Banned, GroupBanView{ID: id, Reason: ban})
	}
	for id := range s.Banned {
		if _, ok := s.Reasons[id]; !ok {
			v.Banned = append(v.Banned, GroupBanView{ID: id})
		}
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
func (e *Engine) waited(s *wire.GroupState, member string) bool {
	m := s.Members[member]
	return m != nil && e.now().Unix() >= m.Joined+s.Settings.WaitDays*86400
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
	ev.Seq, ev.Date = s.Seq(g.Me)+1, e.now().Unix()
	if s.Owner == g.Me && ev.Type != wire.EventProfile && ev.Type != wire.EventLeft && ev.Type != wire.EventRotated && ev.Type != wire.EventRevoked {
		ev.OwnerSeq = s.OwnerSeq + 1
	} else if ev.Type == wire.EventJoined || ev.Type == wire.EventBanned {
		ev.AsOf = s.OwnerSeq
	}
	env, _, err := wire.SignEvent(e.current(), ev)
	if err != nil {
		return StoredEvent{}, err
	}
	if _, _, err := s.Apply(env, e.now()); err != nil {
		return StoredEvent{}, ErrNoRight.with(err)
	}
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
	if ev.Type == wire.EventBanned {
		if k, ok := wire.Decode64(ev.Target, 32); ok {
			if p, err := e.peerByKey(ctx, k); err == nil && p != nil && !slices.ContainsFunc(to, func(q PeerRecord) bool { return q.ID == p.ID }) {
				to = append(to, *p)
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
	if settings.WaitDays < 0 || settings.WaitDays > 30 || settings.CapPerDay < 1 || settings.CapPerDay > 100 {
		return GroupView{}, ErrCodeOptions
	}
	name = wire.CleanText(name, wire.MaxGroupName)
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

// SetGroupOffers sets the host libraries this server offers a group
// (groups section 9); every link through it is offered them.
func (e *Engine) SetGroupOffers(ctx context.Context, id string, libraries []string) error {
	return e.updateGroup(ctx, id, func(g *GroupRecord) { g.Offers = slices.Clone(libraries) })
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
func (e *Engine) SetGroupSettings(ctx context.Context, id, name string, settings wire.GroupSettings) error {
	return e.act(ctx, id, wire.Event{Type: wire.EventSettings, Name: wire.CleanText(name, wire.MaxGroupName), Settings: &settings})
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

// LeaveGroup leaves a group; end lists the peers (links made through it)
// whose pairing ends too. The others lose this group as a source; one
// with no other becomes a pairing by hand, keeping what the group gave
// it (groups section 7).
func (e *Engine) LeaveGroup(ctx context.Context, id string, end []string) error {
	if err := e.act(ctx, id, wire.Event{Type: wire.EventLeft}); err != nil {
		return err
	}
	return e.dropGroup(ctx, id, end, true)
}

// DissolveGroup dissolves the group (the owner only): every member removes
// it; with keepPairings false, links whose only source it was end.
func (e *Engine) DissolveGroup(ctx context.Context, id string, keepPairings bool) error {
	return e.act(ctx, id, wire.Event{Type: wire.EventDissolved, KeepPairings: &keepPairings})
}

// dropGroup removes a group from this server: its record, and from every
// link's sources. Links in end end; a link with no other source ends when
// keep is false, and becomes a pairing by hand when it is true.
func (e *Engine) dropGroup(ctx context.Context, id string, end []string, keep bool) error {
	var ending []string
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, _ := gt.Group(id)
		peers, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range peers {
			if !slices.Contains(p.Groups, id) {
				continue
			}
			p.Groups = slices.DeleteFunc(p.Groups, func(x string) bool { return x == id })
			alone := p.NotByHand && len(p.Groups) == 0
			if slices.Contains(end, p.ID) || (alone && !keep) {
				ending = append(ending, p.ID)
				continue
			}
			if alone && g != nil {
				// A pairing by hand from now on, with what the group gave.
				p.NotByHand = false
				p.Offers, p.Limits = slices.Clone(g.Offers), g.Pool
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
			return tx.Forget(ForgottenKey{KeyHash: h, At: e.now()})
		})
		if err != nil {
			return err
		}
		if peer != "" {
			if err := e.End(ctx, peer); err != nil && !errors.Is(err, ErrNotFound) {
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
		wasMember := s.Members[g.Me] != nil
		now := e.now()
		pending := make([]StoredEvent, 0, len(g.Held)+len(events))
		pending = append(pending, g.Held...)
		for _, env := range events {
			pending = append(pending, StoredEvent{Event: env.Payload, Signature: env.Signature, Received: now})
		}
		g.Held = nil
		for progress := true; progress; {
			progress = false
			var still []StoredEvent
			for _, p := range pending {
				_, _, err := s.Apply(wire.Envelope{Payload: p.Event, Signature: p.Signature}, now)
				switch {
				case err == nil:
					g.Events = append(g.Events, p)
					newEvents = append(newEvents, p)
					progress = true
				case errors.Is(err, wire.ErrHeld):
					if now.Sub(p.Received) < wire.HeldFor {
						still = append(still, p)
					}
				}
			}
			pending = still
		}
		g.Held = pending
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
		if !wasMember && s.Members[g.Me] != nil {
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
		key, _ := wire.Decode64(from.Key, 32)
		hash := sha256Sum(key)
		if f, err := tx.Forgotten(hash); err != nil || f != nil || slices.Contains(g.Refused, m.From) {
			return cmpErr(err, ErrNotFound) // blocked or refused: dropped
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
		}
		return gt.PutGroup(*g)
	})
	if err != nil {
		return
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
				e.background(func(ctx context.Context) { _ = e.dropGroup(ctx, id, nil, keep) })
			})
		}
		return nil
	case s.Banned[g.Me] != "":
		e.log.Info("msfp: this server was banned from a group", "group", id, "reason", s.Reasons[g.Me])
		if effects != nil {
			*effects = append(*effects, func() {
				e.background(func(ctx context.Context) { _ = e.dropGroup(ctx, id, nil, false) })
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
		key := wire.Encode64(p.Key)
		in := false
		for _, m := range s.Members {
			if m.Key == key {
				in = true
			}
		}
		if in {
			continue
		}
		p.Groups = slices.DeleteFunc(p.Groups, func(x string) bool { return x == id })
		_, banned := s.Banned[wire.Encode64(p.Key)]
		if p.NotByHand && len(p.Groups) == 0 {
			if banned {
				pid := p.ID
				if effects != nil {
					*effects = append(*effects, func() {
						e.background(func(ctx context.Context) { _ = e.End(ctx, pid) })
					})
				}
			} else {
				// A member that left keeping the link: by hand from now on.
				p.NotByHand = false
				p.Offers, p.Limits = slices.Clone(g.Offers), g.Pool
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
func (e *Engine) refreshLinks(tx Tx) error {
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
		var offers []string
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
			}
			pools = append(pools, GroupPool{Group: id, Limits: g.Pool})
		}
		slices.Sort(offers)
		if slices.Equal(offers, p.GroupOffers) && slices.Equal(pools, p.Pools) {
			continue
		}
		changed := !slices.Equal(offers, p.GroupOffers)
		p.GroupOffers, p.Pools = offers, pools
		if changed {
			if lt, ok := tx.(OwnLogTx); ok {
				if n, err := lt.BumpOwnSeq(); err == nil {
					p.OffersSeq = n
				}
			}
		}
		if err := tx.PutPeer(p); err != nil {
			return err
		}
	}
	return nil
}

// offered are the host libraries a link is offered: by hand, and through
// its groups (their union).
func offered(p *PeerRecord) []string {
	out := slices.Clone(p.GroupOffers)
	if !p.NotByHand {
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
	if !p.NotByHand {
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
// random (run from the engine's loop).
func (e *Engine) syncGroups(ctx context.Context) {
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
		if e.now().Sub(g.Synced) < every-every/5+time.Duration(rand.Int64N(int64(every/5*2))) {
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
		key := wire.Encode64(req.peer.Key)
		for _, m := range s.Members {
			if m.Key == key {
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
	if _, ok := e.coMember(req, body.Group); !ok || !e.on(req.peer, wire.PartGroups) {
		return refuse(probNotFound)
	}
	if err := e.receive(req.r.Context(), body.Group, body.Events, body.Mail, req.peer.ID); err != nil {
		return refuse(probInternal)
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
		if f, err := tx.Forgotten(hash); err != nil || f != nil {
			return cmpErr(err, ErrNotFound)
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
	if g, err := gt.Group(id); err != nil || g != nil {
		return err
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
		err := e.act(ctx, id, wire.Event{Type: wire.EventJoined, NewMember: wire.Encode64(p.Key), Name: p.Name})
		if err != nil {
			e.log.Info("msfp: a server couldn't be added to the group its code invited it to", "peer", p.ID, "error", err)
		}
		_ = e.updatePeer(ctx, p.ID, func(q *PeerRecord) {
			q.JoiningGroup = ""
			if err == nil && !slices.Contains(q.Groups, id) {
				q.Groups = append(q.Groups, id)
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
		g.Via = ""
		return gt.PutGroup(*g)
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

// poolRoom says whether one of a link's pools has a stream free now
// (room), and whether one allows any stream at all (any).
func (e *Engine) poolRoom(p *PeerRecord, audio bool) (room, any bool) {
	for _, pool := range pools(p) {
		used, most := e.inUse(poolKey(p, pool), pool.Limits, audio)
		any = any || most > 0
		room = room || used < most
	}
	return room, any
}

// admitPools admits a stream in the first of a link's pools with room
// (groups section 9): each playback counts against exactly one.
func (e *Engine) admitPools(p *PeerRecord, audio bool, method string) (*countSlot, error) {
	err := error(ErrStreamLimitReached)
	for _, pool := range pools(p) {
		s, e2 := e.admitStream(poolKey(p, pool), pool.Limits, audio, method)
		if e2 == nil {
			return s, nil
		}
		err = e2
	}
	return nil, err
}

// generous is the most generous of a link's pools (what libraries tells
// it, groups section 9): the most streams and transcodes, and the highest
// bitrate cap (none, 0, above all).
func generous(p *PeerRecord) Limits {
	ps := pools(p)
	if len(ps) == 0 {
		return Limits{}
	}
	out := ps[0].Limits
	for _, pool := range ps[1:] {
		l := pool.Limits
		out.Streams = max(out.Streams, l.Streams)
		out.Transcodes = max(out.Transcodes, l.Transcodes)
		out.AudioStreams = max(out.AudioStreams, l.AudioStreams)
		out.AudioTranscodes = max(out.AudioTranscodes, l.AudioTranscodes)
		out.Audio = out.Audio || l.Audio
		if out.BitrateKbps != 0 && (l.BitrateKbps == 0 || l.BitrateKbps > out.BitrateKbps) {
			out.BitrateKbps = l.BitrateKbps
		}
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
