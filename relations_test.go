package msfp_test

import (
	"context"
	"testing"

	msfp "github.com/msfp-io/msfp-go"
)

// Relations (MSFP core section 7.7): a plain code gives the individual
// relation; a group's code the group relation only. Between group
// members, one asks for the individual relation and the other accepts or
// refuses; ending it keeps the link while the group holds it. Leaving
// ends a link nothing else holds.
func TestRelations(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	b := newServer(t, msfp.Config{Name: func() string { return "B" }})
	c := newServer(t, msfp.Config{Name: func() string { return "C" }})
	view := func(s *server, name string) msfp.PeerView {
		t.Helper()
		for _, p := range mustPeers(t, s) {
			if p.Name == name {
				return p
			}
		}
		return msfp.PeerView{}
	}

	// A plain code: individual on both sides.
	pair(t, a, c)
	settle(a, c)
	if v := view(a, "C"); !v.Individual || len(v.Groups) != 0 {
		t.Fatalf("A's C: %+v", v)
	}
	if v := view(c, "A"); !v.Individual {
		t.Fatalf("C's A: %+v", v)
	}

	// A group's code: the group relation only.
	g, err := a.eng.CreateGroup(ctx, "Family", msfp.GroupSettingsDefault)
	if err != nil {
		t.Fatal(err)
	}
	joinGroup(t, a, b, g.ID)
	settle(a, b)
	if v := view(a, "B"); v.Individual || v.IndividualAsks || len(v.Groups) != 1 {
		t.Fatalf("A's B: %+v", v)
	}
	if v := view(b, "A"); v.Individual || v.IndividualAsked || len(v.Groups) != 1 {
		t.Fatalf("B's A: %+v", v)
	}

	// B asks, A accepts.
	aOnB, bOnA := view(b, "A").ID, view(a, "B").ID
	if v, err := b.eng.AskIndividual(ctx, aOnB); err != nil || !v.IndividualAsked {
		t.Fatalf("B asks: %+v %v", v, err)
	}
	settle(a, b)
	if v := view(a, "B"); !v.IndividualAsks {
		t.Fatalf("A isn't asked: %+v", v)
	}
	if _, err := a.eng.AskIndividual(ctx, bOnA); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if !view(a, "B").Individual || !view(b, "A").Individual {
		t.Fatalf("not individual: %+v %+v", view(a, "B"), view(b, "A"))
	}

	// A ends it: the group still holds the link, and B's side ends too.
	if err := a.eng.EndIndividual(ctx, bOnA); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if v := view(b, "A"); v.ID == "" || v.Individual || v.IndividualAsks || v.IndividualAsked || len(v.Groups) != 1 {
		t.Fatalf("B's A after the end: %+v", v)
	}
	if v := view(a, "B"); v.ID == "" || v.Individual || v.IndividualAsked {
		t.Fatalf("A's B after the end: %+v", v)
	}

	// B asks again, A refuses: privately, B's ask stays shown on B.
	if _, err := b.eng.AskIndividual(ctx, aOnB); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if err := a.eng.RefuseIndividual(ctx, bOnA); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if v := view(a, "B"); v.ID == "" || v.IndividualAsks || v.Individual {
		t.Fatalf("A's B after refusing: %+v", v)
	}
	if v := view(b, "A"); !v.IndividualAsked {
		t.Fatalf("B's A after the refusal: %+v", v)
	}

	// B leaves without asking: the link nothing else holds ends, on both
	// sides; A's individual link with C stays.
	if err := b.eng.EndIndividual(ctx, aOnB); err != nil {
		t.Fatal(err)
	}
	settle(a, b)
	if err := b.eng.LeaveGroup(ctx, g.ID, nil); err != nil {
		t.Fatal(err)
	}
	settle(a, b, c)
	if v := view(b, "A"); v.ID != "" {
		t.Fatalf("B still has A: %+v", v)
	}
	if v := view(a, "B"); v.ID != "" {
		t.Fatalf("A still has B: %+v", v)
	}
	if v := view(a, "C"); !v.Individual {
		t.Fatalf("A's C: %+v", v)
	}

	// Not kept away: a new code for the group brings B back.
	joinGroup(t, a, b, g.ID)
	settle(a, b)
	if v := view(b, "A"); len(v.Groups) != 1 {
		t.Fatalf("B back: %+v", v)
	}
}
