package msfp

import (
	"context"
	"slices"
)

// What this server shows from a group (its members' libraries, as one
// choice for the whole group): everything its members offer it, but the
// libraries it hides. What it reads of a server is what it shows of it
// individually (SetWants, while the individual relation holds) and what
// its groups show of it.

// GroupShowsView is what a group shows here, member by member.
type GroupShowsView struct {
	Members []GroupMemberShows
}

// GroupMemberShows is one member's libraries offered to this server, and
// whether the group shows each. Peer is the link with it ("" for none:
// nothing of it is read yet).
type GroupMemberShows struct {
	Member, Name, Peer string
	Libraries          []ShownLibrary
}

// ShownLibrary is a library and whether it is shown.
type ShownLibrary struct {
	ID, Name, Kind string
	Shown          bool
}

// GroupShows is what a group shows here. Error: ErrNotFound.
func (e *Engine) GroupShows(ctx context.Context, id string) (GroupShowsView, error) {
	var out GroupShowsView
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
		if err != nil {
			return err
		}
		s := e.stateOf(g)
		for _, m := range s.Members {
			if m.ID == g.Me {
				continue
			}
			ms := GroupMemberShows{Member: m.ID, Name: m.Name}
			for i := range peers {
				p := &peers[i]
				if !isMember(m, p) || !slices.Contains(p.Groups, id) {
					continue
				}
				ms.Peer = p.ID
				for _, w := range p.Wants {
					if w.Gone {
						continue
					}
					hidden := slices.Contains(g.Hidden, HiddenLibrary{Member: m.ID, Library: w.ID})
					ms.Libraries = append(ms.Libraries, ShownLibrary{ID: w.ID, Name: w.Name, Kind: w.Kind, Shown: !hidden})
				}
			}
			out.Members = append(out.Members, ms)
		}
		slices.SortFunc(out.Members, func(a, b GroupMemberShows) int {
			if a.Name < b.Name {
				return -1
			}
			if a.Name > b.Name {
				return 1
			}
			return 0
		})
		return nil
	})
	return out, err
}

// SetGroupShows sets the members' libraries a group doesn't show here; it
// shows everything else they offer. Each member linked through it is read
// again soon. Error: ErrNotFound.
func (e *Engine) SetGroupShows(ctx context.Context, id string, hidden []HiddenLibrary) error {
	var peers []string
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return err
		}
		g, err := gt.Group(id)
		if err != nil || g == nil {
			return cmpErr(err, ErrNotFound)
		}
		g.Hidden = nil
		for _, h := range hidden {
			if h.Member != "" && h.Library != "" && !slices.Contains(g.Hidden, h) {
				g.Hidden = append(g.Hidden, h)
			}
		}
		ps, err := tx.Peers()
		if err != nil {
			return err
		}
		for _, p := range ps {
			if slices.Contains(p.Groups, id) {
				peers = append(peers, p.ID)
			}
		}
		return gt.PutGroup(*g)
	})
	if err != nil {
		return err
	}
	for _, p := range peers {
		e.readSoon(p)
	}
	return nil
}

// shownBy says which of a link's libraries its groups show here: shows(id)
// for a library, and any, whether some group shows anything of it.
func (e *Engine) shownBy(ctx context.Context, p *PeerRecord) (shows func(string) bool, any bool) {
	type hide struct {
		member string
		hidden []HiddenLibrary
	}
	var groups []hide
	_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
		gt, err := groupsTx(tx)
		if err != nil {
			return nil
		}
		for _, id := range p.Groups {
			g, err := gt.Group(id)
			if err != nil || g == nil {
				continue
			}
			s := e.stateOf(g)
			for _, m := range s.Members {
				if isMember(m, p) {
					groups = append(groups, hide{member: m.ID, hidden: g.Hidden})
				}
			}
		}
		return nil
	})
	return func(lib string) bool {
		return slices.ContainsFunc(groups, func(h hide) bool {
			return !slices.Contains(h.hidden, HiddenLibrary{Member: h.member, Library: lib})
		})
	}, len(groups) > 0
}
