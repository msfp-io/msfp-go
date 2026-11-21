package msfp

import (
	"context"
	"errors"
	"slices"
)

// Relations (MSFP core section 7.7): the operator manages why two servers
// are linked, not the link. The individual relation holds while both sides
// want it (NotByHand false here, and the peer's last hello said
// individual true); a group relation for each group in PeerRecord.Groups.
// A link lasts while one relation holds.

// theirIndividual is whether the peer wants the individual relation: what
// it last said, or, never said, what this side wants.
func (p *PeerRecord) theirIndividual() bool {
	if p.TheirIndividual == nil {
		return !p.NotByHand
	}
	return *p.TheirIndividual
}

// individual is whether the individual relation holds: both sides want it.
func (p *PeerRecord) individual() bool {
	return !p.NotByHand && p.theirIndividual()
}

// keepIndividual keeps a hello's individual: a peer that stops wanting the
// relation while it held ends it for both (core section 7.7).
func (e *Engine) keepIndividual(p *PeerRecord, theirs *bool) []Event {
	if theirs == nil {
		return nil
	}
	held := p.individual()
	asked := !p.NotByHand && !p.theirIndividual()
	asks := p.NotByHand && p.theirIndividual() && !p.IndividualRefused
	t := *theirs
	p.TheirIndividual = &t
	if held && !t {
		p.NotByHand = true // it ended the relation: ended here too
	}
	if !t {
		p.IndividualRefused = false
	}
	if held != p.individual() || asked != (!p.NotByHand && !p.theirIndividual()) ||
		asks != (p.NotByHand && p.theirIndividual() && !p.IndividualRefused) {
		return []Event{RelationsChanged{Peer: p.ID, Name: p.Name}}
	}
	return nil
}

// holds says whether a paired link still has a reason to be: a group
// relation, a group it is being joined through, an individual relation
// either side wants (an ask waiting for its answer included), or our
// operator choosing whether to keep it.
func holds(p *PeerRecord, joiningVia bool) bool {
	return len(p.Groups) > 0 || joiningVia || p.JoiningGroup != "" || !p.NotByHand ||
		(p.theirIndividual() && !p.IndividualRefused) || p.KeepChoice
}

// unheld says whether the link with this peer, paired, has no relation
// left (core section 7.7, "When the last relation goes").
func (e *Engine) unheld(tx Tx, p *PeerRecord) (bool, error) {
	if p.State != Paired {
		return false, nil
	}
	via := false
	if gt, err := groupsTx(tx); err == nil {
		gs, err := gt.Groups()
		if err != nil {
			return false, err
		}
		via = slices.ContainsFunc(gs, func(g GroupRecord) bool { return g.Via == p.ID })
	}
	return !holds(p, via), nil
}

// endIfUnheld ends, in the background, the link with this peer when no
// relation holds it any more.
func (e *Engine) endIfUnheld(id string) {
	e.background(func(ctx context.Context) {
		end := false
		_ = e.cfg.Store.Read(ctx, func(tx Tx) error {
			p, err := tx.Peer(id)
			if err != nil || p == nil {
				return err
			}
			end, err = e.unheld(tx, p)
			return err
		})
		if end {
			e.log.Info("msfp: no relation holds the link any more: it ends", "peer", id)
			if err := e.End(ctx, id); err != nil {
				e.log.Info("msfp: a link with no relation couldn't be ended", "peer", id, "error", err)
			}
		}
	})
}

// setIndividual sets whether this side wants the individual relation, and
// tells the peer at once (a peer that can't be called learns at its next
// hello).
func (e *Engine) setIndividual(ctx context.Context, id string, want bool) (PeerView, error) {
	var p PeerRecord
	err := e.cfg.Store.Update(ctx, func(tx Tx) error {
		q, err := tx.Peer(id)
		switch {
		case err != nil:
			return err
		case q == nil:
			return ErrNotFound
		case q.State != Paired:
			return ErrNotPaired
		}
		was := q.individual()
		q.NotByHand, q.KeepChoice = !want, false
		if want {
			q.IndividualRefused = false
		}
		if was != q.individual() {
			// What it is offered changes (its individual relation's offers).
			if q.OffersSeq, err = e.offersSeq(tx, e.own.seqSeen.Load()); err != nil {
				return err
			}
		}
		p = *q
		return tx.PutPeer(*q)
	})
	if err != nil {
		return PeerView{}, err
	}
	e.notify(RelationsChanged{Peer: p.ID, Name: p.Name})
	if p.Inbound == InboundYes && p.Suspended.IsZero() {
		if _, err := e.Check(ctx, id); err != nil {
			e.log.Info("msfp: the peer couldn't be told of the individual relation yet; it learns at the next hello", "peer", id, "error", err)
		}
	}
	return e.Peer(ctx, id)
}

// AskIndividual asks a peer linked through a group for the individual
// relation, or accepts its ask (core section 7.7); it also keeps a link a
// dissolution left to our choice. Errors: ErrNotFound, ErrNotPaired.
func (e *Engine) AskIndividual(ctx context.Context, id string) (PeerView, error) {
	return e.setIndividual(ctx, id, true)
}

// EndIndividual ends the individual relation with a peer, or withdraws
// this server's ask: the link stays while a group holds it, and ends
// otherwise, without keeping the peer away (core section 7.7). Error:
// ErrNotFound.
func (e *Engine) EndIndividual(ctx context.Context, id string) error {
	if _, err := e.setIndividual(ctx, id, false); err != nil {
		if errors.Is(err, ErrNotPaired) {
			return e.End(ctx, id)
		}
		return err
	}
	return e.endNowIfUnheld(ctx, id)
}

// RefuseIndividual refuses a peer's ask for the individual relation, or
// ends a link a dissolution left to our choice: privately, nothing sent
// (core section 7.7); a link nothing else holds ends. Errors:
// ErrNotFound.
func (e *Engine) RefuseIndividual(ctx context.Context, id string) error {
	err := e.updatePeer(ctx, id, func(q *PeerRecord) {
		q.IndividualRefused, q.KeepChoice = true, false
	})
	if err != nil {
		return err
	}
	e.notify(RelationsChanged{Peer: id})
	return e.endNowIfUnheld(ctx, id)
}

// endNowIfUnheld ends the link now when no relation holds it.
func (e *Engine) endNowIfUnheld(ctx context.Context, id string) error {
	end := false
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		p, err := tx.Peer(id)
		if err != nil || p == nil {
			return cmpErr(err, ErrNotFound)
		}
		end, err = e.unheld(tx, p)
		return err
	})
	if err != nil || !end {
		return err
	}
	return e.End(ctx, id)
}
