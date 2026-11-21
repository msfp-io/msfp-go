package msfp_test

import (
	"context"
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
