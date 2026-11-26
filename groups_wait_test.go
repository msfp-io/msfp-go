package msfp_test

import (
	"context"
	"slices"
	"testing"

	"github.com/msfp-io/msfp-go"
)

// waitDays, which earlier drafts had, changes nothing (groups section 3.3,
// settings): with 2 set by the owner, B, which accepts new members by
// itself, accepts the newcomer at once; nothing waits.
func TestGroupWaitDaysChangeNothing(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }})
	n := newServer(t, msfp.Config{Name: func() string { return "N" }})
	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	settle(a, b)
	if err := a.eng.SetGroupSettings(ctx, g.ID, "", msfp.GroupSettings{WaitDays: 2, CapPerDay: 5}); err != nil {
		t.Fatal(err)
	}
	if err := b.eng.SetGroupAnswer(ctx, g.ID, true); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	joinGroup(t, a, n, g.ID)
	settle(a, b, n)
	gb, _ := b.eng.Group(ctx, g.ID)
	if p := peerNamed(t, b, "N"); p.State != msfp.Paired || slices.ContainsFunc(gb.Requests, func(r msfp.GroupRequestView) bool { return r.Name == "N" }) {
		t.Errorf("N on B with waitDays 2: %s, requests %+v", p.State, gb.Requests)
	}
}
