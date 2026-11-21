package msfp_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// joinGroup has member redeem a code inviter makes for the group, and the
// inviter's operator accept it (MSFP groups sections 2 and 4).
func joinGroup(t *testing.T, inviter, member *server, group string) {
	t.Helper()
	ctx := context.Background()
	code, view, err := inviter.eng.MakeCodeWith(ctx, msfp.CodeOptions{Group: group})
	if err != nil {
		t.Fatal(err)
	}
	if view.Group != group || view.Uses != 1 {
		t.Errorf("code view: %+v", view)
	}
	if pv, err := member.eng.ReadCode(ctx, code); err != nil || pv.Group != group || pv.GroupName == "" {
		t.Errorf("preview: %+v, %v", pv, err)
	}
	theirs, err := member.eng.RedeemCode(ctx, code)
	if err != nil {
		t.Fatal(err)
	}
	var ours string
	peers, _ := inviter.eng.Peers(ctx)
	for _, p := range peers {
		if p.State == msfp.Ours {
			ours = p.ID
		}
	}
	if _, err := inviter.eng.Accept(ctx, ours); err != nil {
		t.Fatal(err)
	}
	waitAll(inviter, member)
	_ = theirs
}

// settle waits until the servers' background work, which starts work on
// each other, is over.
func settle(servers ...*server) {
	for range 8 {
		waitAll(servers...)
		time.Sleep(20 * time.Millisecond)
	}
}

func memberIDs(v msfp.GroupView) []string {
	var out []string
	for _, m := range v.Members {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	return out
}

// Three servers: A founds Family and invites B, then N; N's request
// reaches B through A, B accepts, and B and N pair through the group.
func TestGroupJoinAndPair(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }})
	n := newServer(t, msfp.Config{Name: func() string { return "N" }})

	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	if g.Role != "owner" || g.Name != "Family" || len(g.Members) != 1 {
		t.Fatalf("founded: %+v", g)
	}
	joinGroup(t, a, b, g.ID)
	gb, err := b.eng.Group(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gb.Role != "member" || len(gb.Members) != 2 || gb.Owner != g.Me {
		t.Fatalf("B's copy: %+v", gb)
	}

	joinGroup(t, a, n, g.ID)
	settle(a, b, n)
	ga, _ := a.eng.Group(ctx, g.ID)
	gn, _ := n.eng.Group(ctx, g.ID)
	gb, _ = b.eng.Group(ctx, g.ID)
	if !slices.Equal(memberIDs(ga), memberIDs(gn)) || !slices.Equal(memberIDs(ga), memberIDs(gb)) || len(ga.Members) != 3 {
		t.Fatalf("copies differ: A %v, B %v, N %v", memberIDs(ga), memberIDs(gb), memberIDs(gn))
	}
	if len(gb.Requests) != 1 || gb.Requests[0].Name != "N" {
		t.Fatalf("B's requests: %+v", gb.Requests)
	}
	if err := b.eng.AcceptRequest(ctx, g.ID, gb.Requests[0].Mail); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	var onB, onN msfp.PeerView
	for _, p := range mustPeers(t, b) {
		if p.Name == "N" {
			onB = p
		}
	}
	for _, p := range mustPeers(t, n) {
		if p.Name == "B" {
			onN = p
		}
	}
	if onB.State != msfp.Paired || onN.State != msfp.Paired {
		t.Fatalf("B and N: %+v / %+v", onB, onN)
	}
	if onB.ByHand || len(onB.Groups) != 1 || onB.Groups[0] != g.ID {
		t.Fatalf("B's link with N: by hand %v, groups %v", onB.ByHand, onB.Groups)
	}
	if _, err := b.eng.Group(ctx, g.ID); err != nil {
		t.Fatal(err)
	}
}

func mustPeers(t *testing.T, s *server) []msfp.PeerView {
	t.Helper()
	ps, err := s.eng.Peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// A ban ends the banned server's links made only through the group, and
// its copy of the group; a member that leaves keeps its links by hand;
// dissolving removes the group everywhere.
func TestGroupBanLeaveDissolve(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }})
	n := newServer(t, msfp.Config{Name: func() string { return "N" }})
	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	joinGroup(t, a, n, g.ID)
	settle(a, b, n)
	gb, _ := b.eng.Group(ctx, g.ID)
	if len(gb.Requests) != 1 {
		t.Fatalf("B's requests: %+v", gb.Requests)
	}
	if err := b.eng.AcceptRequest(ctx, g.ID, gb.Requests[0].Mail); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	nID := ""
	for _, m := range gb.Members {
		if m.Name == "N" {
			nID = m.ID
		}
	}
	if err := a.eng.Ban(ctx, g.ID, nID, "testing"); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	for _, p := range mustPeers(t, b) {
		if p.Name == "N" {
			t.Errorf("B still has N: %+v", p)
		}
	}
	if _, err := n.eng.Group(ctx, g.ID); err == nil {
		t.Error("N still has the group")
	}
	ga, _ := a.eng.Group(ctx, g.ID)
	if len(ga.Banned) != 1 || ga.Banned[0].Reason != "testing" {
		t.Errorf("A's bans: %+v", ga.Banned)
	}

	if err := b.eng.LeaveGroup(ctx, g.ID, nil); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	if _, err := b.eng.Group(ctx, g.ID); err == nil {
		t.Error("B still has the group it left")
	}
	if p := mustPeers(t, b); len(p) != 1 || p[0].State != msfp.Paired {
		t.Errorf("B's link with A, kept by hand: %+v", p)
	}
	ga, _ = a.eng.Group(ctx, g.ID)
	if len(ga.Members) != 1 {
		t.Errorf("A's members after B left: %+v", ga.Members)
	}
	if err := a.eng.DissolveGroup(ctx, g.ID, true); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	if _, err := a.eng.Group(ctx, g.ID); err == nil {
		t.Error("A still has the dissolved group")
	}
}

// A member that rotates its key tells its groups (rotated), and its
// events under the new key count on the others' copies.
func TestGroupKeyRotation(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }})
	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	settle(a, b)
	if err := a.eng.Rotate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if err := a.eng.SetGroupSettings(ctx, g.ID, "Family and friends", msfp.GroupSettingsDefault); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	gb, _ := b.eng.Group(ctx, g.ID)
	if gb.Name != "Family and friends" {
		t.Errorf("B's copy after A rotated: %+v", gb)
	}
	aServer, _ := a.eng.Server(ctx)
	for _, m := range gb.Members {
		if m.Role == "owner" && m.Fingerprint != aServer.Fingerprint {
			t.Errorf("A's key in B's copy: %s, A is %s", m.Fingerprint, aServer.Fingerprint)
		}
	}
}

// trio makes A (the owner, founding Family), B and N, both invited by A,
// with N's request to B accepted: B and N linked through the group only.
func trio(t *testing.T, cfgA, cfgB, cfgN msfp.Config, settings msfp.GroupSettings) (a, b, n *server, g msfp.GroupView) {
	t.Helper()
	ctx := context.Background()
	named := func(c msfp.Config, name string) msfp.Config {
		c.Name = func() string { return name }
		return c
	}
	a = newServer(t, named(cfgA, "A"))
	b = newServer(t, named(cfgB, "B"))
	n = newServer(t, named(cfgN, "N"))
	g, err := a.eng.CreateGroup(ctx, "Family", settings)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	joinGroup(t, a, n, g.ID)
	settle(a, b, n)
	return a, b, n, g
}

func peerNamed(t *testing.T, s *server, name string) msfp.PeerView {
	t.Helper()
	for _, p := range mustPeers(t, s) {
		if p.Name == name {
			return p
		}
	}
	return msfp.PeerView{}
}

func memberNamed(v msfp.GroupView, name string) string {
	for _, m := range v.Members {
		if m.Name == name {
			return m.ID
		}
	}
	return ""
}

// B offers its films to the group, with a pool of one stream: N, linked
// to B through the group alone, sees them and plays one; a second is
// refused while the first plays (groups section 9).
func TestGroupOffersAndPool(t *testing.T) {
	ctx := context.Background()
	h := filmHost()
	h.libs = []msfp.Library{{ID: "films", Name: "Films", Kind: "movies"}}
	a, b, n, g := trio(t, msfp.Config{}, msfp.Config{Catalog: h, Playback: h, Media: h}, msfp.Config{}, msfp.GroupSettingsDefault)
	gb, _ := b.eng.Group(ctx, g.ID)
	if err := b.eng.AcceptRequest(ctx, g.ID, gb.Requests[0].Mail); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	if err := b.eng.SetGroupOffers(ctx, g.ID, []string{"films"}); err != nil {
		t.Fatal(err)
	}
	if err := b.eng.SetGroupPool(ctx, g.ID, msfp.Limits{Streams: 1, Transcodes: 0}); err != nil {
		t.Fatal(err)
	}
	onN := peerNamed(t, n, "B")
	if onN.State != msfp.Paired {
		t.Fatalf("N's link with B: %+v", onN)
	}
	n.eng.Check(ctx, onN.ID)
	libs, err := n.eng.TheirLibraries(ctx, onN.ID)
	if err != nil || len(libs) != 1 || libs[0].Name != "Films" {
		t.Fatalf("B's libraries, as N sees them: %+v %v", libs, err)
	}
	if v, _ := n.eng.Peer(ctx, onN.ID); v.TheirLimits == nil || v.TheirLimits.Streams != 1 {
		t.Errorf("the limits B tells N: %+v", v.TheirLimits)
	}
	n.eng.SetWants(ctx, onN.ID, []string{libs[0].ID})
	if _, err := n.eng.ReadNow(ctx, onN.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := n.eng.Play(ctx, onN.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); err != nil {
		t.Fatalf("a play through the group's pool: %v", err)
	}
	if _, err := n.eng.Play(ctx, onN.ID, msfp.PlayRequest{Title: "movie/tmdb/949"}); !errors.Is(err, msfp.ErrStreamLimitReached) {
		t.Errorf("a second play, past the pool of one: %v", err)
	}
}

// "Accept members": a request is accepted by the server itself once its
// sender has been in the group the group's days; before, it waits.
func TestGroupAcceptAfterDays(t *testing.T) {
	ctx := context.Background()
	clk := newClock(1800000000)
	a := newServer(t, msfp.Config{Name: func() string { return "A" }, Clock: clk})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }, Clock: clk})
	n := newServer(t, msfp.Config{Name: func() string { return "N" }, Clock: clk})
	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	if err := b.eng.SetGroupAnswer(ctx, g.ID, true); err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, n, g.ID)
	settle(a, b, n)
	gb, _ := b.eng.Group(ctx, g.ID)
	if len(gb.Requests) != 1 || gb.Requests[0].Waited {
		t.Fatalf("N's request, before its days: %+v", gb.Requests)
	}
	clk.Add(3*24*time.Hour + time.Minute)
	b.eng.SyncGroupsForTest(ctx)
	settle(a, b, n)
	if p := peerNamed(t, b, "N"); p.State != msfp.Paired {
		t.Errorf("B and N after N's days: %+v", p)
	}
	if gb, _ := b.eng.Group(ctx, g.ID); len(gb.Requests) != 0 {
		t.Errorf("requests left: %+v", gb.Requests)
	}
}

// A newcomer that can't be called: the member accepting answers by mail,
// and the newcomer calls group-pair itself (groups section 4.2).
func TestGroupUncallableNewcomer(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }})
	n := newServer(t, msfp.Config{Name: func() string { return "N" }})
	if err := n.eng.SetUncallable(ctx, true); err != nil {
		t.Fatal(err)
	}
	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	joinGroup(t, a, n, g.ID)
	// N learns it is paired by its own hello, then reads the list.
	for range 3 {
		n.eng.RunOnce(ctx)
		n.eng.SyncGroupsForTest(ctx)
		settle(a, b, n)
	}
	gb, _ := b.eng.Group(ctx, g.ID)
	if len(gb.Requests) != 1 {
		t.Fatalf("B's requests from N: %+v", gb.Requests)
	}
	if err := b.eng.AcceptRequest(ctx, g.ID, gb.Requests[0].Mail); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	n.eng.SyncGroupsForTest(ctx)
	settle(a, b, n)
	if p := peerNamed(t, n, "B"); p.State != msfp.Paired {
		t.Errorf("N's link with B: %+v", p)
	}
	if p := peerNamed(t, b, "N"); p.State != msfp.Paired || p.Inbound != msfp.InboundNo {
		t.Errorf("B's link with N: %+v", p)
	}
}

// Roles: an admin invites, a member can't; the owner gives the group to
// its heir; an unban lets a server back; a block ends a member's link and
// those of the members it invited.
func TestGroupRoles(t *testing.T) {
	ctx := context.Background()
	a, b, n, g := trio(t, msfp.Config{}, msfp.Config{}, msfp.Config{}, msfp.GroupSettingsDefault)
	ga, _ := a.eng.Group(ctx, g.ID)
	bID, nID := memberNamed(ga, "B"), memberNamed(ga, "N")
	if _, _, err := n.eng.MakeCodeWith(ctx, msfp.CodeOptions{Group: g.ID}); !errors.Is(err, msfp.ErrNoRight) {
		t.Errorf("a member's code: %v", err)
	}
	if err := a.eng.SetAdmin(ctx, g.ID, bID, true); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n)
	c := newServer(t, msfp.Config{Name: func() string { return "C" }})
	joinGroup(t, b, c, g.ID)
	settle(a, b, n, c)
	ga, _ = a.eng.Group(ctx, g.ID)
	cID := memberNamed(ga, "C")
	if cID == "" || len(ga.Members) != 4 {
		t.Fatalf("C invited by the admin B: %+v", ga.Members)
	}
	// N blocks B and the members it invited: C.
	if err := n.eng.Block(ctx, g.ID, bID, true); err != nil {
		t.Fatal(err)
	}
	if p := peerNamed(t, n, "B"); p.ID != "" {
		t.Errorf("N still linked with B: %+v", p)
	}
	// N, a member, can't ban.
	if err := n.eng.Ban(ctx, g.ID, cID, ""); !errors.Is(err, msfp.ErrNoRight) {
		t.Errorf("a member's ban: %v", err)
	}
	if err := a.eng.Ban(ctx, g.ID, nID, ""); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n, c)
	if err := a.eng.Unban(ctx, g.ID, nID); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n, c)
	if ga, _ = a.eng.Group(ctx, g.ID); len(ga.Banned) != 0 {
		t.Errorf("bans after the unban: %+v", ga.Banned)
	}
	if memberNamed(ga, "N") != "" {
		t.Error("an unbanned server is a member again without a new invitation")
	}
	// The owner names C its heir, then steps down: C owns the group.
	if err := a.eng.SetHeir(ctx, g.ID, cID); err != nil {
		t.Fatal(err)
	}
	if err := a.eng.GiveGroup(ctx, g.ID, ""); err != nil {
		t.Fatal(err)
	}
	settle(a, b, n, c)
	if gc, _ := c.eng.Group(ctx, g.ID); gc.Role != "owner" || gc.Owner != cID {
		t.Errorf("C after A stepped down: role %s, owner %s", gc.Role, gc.Owner)
	}
	if err := a.eng.SetAdmin(ctx, g.ID, bID, false); !errors.Is(err, msfp.ErrNoRight) {
		t.Errorf("A, no longer the owner, removing an admin: %v", err)
	}
}
