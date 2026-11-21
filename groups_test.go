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
