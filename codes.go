package msfp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// Pairing codes (MSFP core sections 5 and 7.1): this server's operator
// makes one and sends it to another operator, who redeems it on their
// server.

// CodeView is a live code, as the panel lists it: never its text, which
// is shown once, when made.
type CodeView struct {
	ID      string
	Note    string
	Created time.Time
	Expires time.Time
	// Uses is how many more servers may use it; Group the group it
	// invites to ("" for none).
	Uses  int
	Group string
}

// CodeOptions are a code's settings (MSFP core section 5.1): how many
// servers may use it (0: once) and how long it works (0: 24 hours; at most
// 30 days).
type CodeOptions struct {
	Note     string
	Uses     int
	Lifetime time.Duration
	// Group is a group's ID, for a code inviting to it (groups section
	// 2): this server must be its owner or an admin.
	Group string
}

// MaxCodeUses is how many servers one code may serve at most.
const MaxCodeUses = 100

// CodePreview is what a code says, for the operator to see before
// redeeming it.
type CodePreview struct {
	Address     string
	Fingerprint string
	Expires     time.Time
	// Group and GroupName are the group a group's code invites to ("" for
	// a plain code): show the name before redeeming.
	Group, GroupName string
}

// MakeCode makes a pairing code, noted as note for the panel, and gives
// its text, to show once. It works once, for 24 hours. Error: ErrNoCodes
// (this server can't be called).
func (e *Engine) MakeCode(ctx context.Context, note string) (string, CodeView, error) {
	return e.MakeCodeWith(ctx, CodeOptions{Note: note})
}

// MakeCodeWith makes a pairing code with its options. Errors: ErrNoCodes,
// ErrCodeOptions (uses or lifetime out of range), and for a group's code
// ErrNotFound (no such group) or ErrNoRight (not its owner nor an admin).
func (e *Engine) MakeCodeWith(ctx context.Context, o CodeOptions) (string, CodeView, error) {
	uses, life := o.Uses, o.Lifetime
	if uses == 0 {
		uses = 1
	}
	if life == 0 {
		life = wire.CodeLifetime
	}
	if uses < 1 || uses > MaxCodeUses || life < time.Minute || life > wire.MaxCodeLifetime {
		return "", CodeView{}, ErrCodeOptions
	}
	var group []byte
	var groupName string
	if o.Group != "" {
		var err error
		if group, groupName, err = e.groupForCode(ctx, o.Group); err != nil {
			return "", CodeView{}, err
		}
	}
	rec, err := e.serverRecord(ctx)
	if err != nil {
		return "", CodeView{}, err
	}
	if rec.Uncallable || e.saysUncallable() {
		// Nobody could redeem it (MSFP core section 7.5): the other
		// operator makes one.
		return "", CodeView{}, ErrNoCodes
	}
	secret, err := e.random(wire.SecretSize)
	if err != nil {
		return "", CodeView{}, err
	}
	id, err := e.newID("c")
	if err != nil {
		return "", CodeView{}, err
	}
	now := e.now()
	code := wire.Code{Address: e.named(e.address()), Key: e.current().Public(), Secret: secret,
		Expires: now.Add(life).Truncate(time.Second), Group: group, GroupName: groupName}
	sealed, err := e.cfg.Sealer.Seal(secret, sealCode)
	if err != nil {
		return "", CodeView{}, err
	}
	c := CodeRecord{ID: id, Secret: sealed, SecretHash: wire.SecretHash(secret), Note: cleanName(o.Note, ""),
		Created: now.Truncate(time.Second), Expires: code.Expires.UTC(), Uses: uses, Group: o.Group}
	if err := e.cfg.Store.Update(ctx, func(tx Tx) error { return tx.PutCode(c) }); err != nil {
		return "", CodeView{}, err
	}
	return code.String(), codeView(c), nil
}

func codeView(c CodeRecord) CodeView {
	uses := c.Uses
	if uses == 0 {
		uses = 1
	}
	return CodeView{ID: c.ID, Note: c.Note, Created: c.Created, Expires: c.Expires, Uses: uses, Group: c.Group}
}

// Codes lists the live codes, soonest to expire first.
func (e *Engine) Codes(ctx context.Context) ([]CodeView, error) {
	var out []CodeView
	err := e.cfg.Store.Read(ctx, func(tx Tx) error {
		codes, err := tx.Codes()
		for _, c := range codes {
			if c.Expires.After(e.now()) {
				out = append(out, codeView(c))
			}
		}
		return err
	})
	slices.SortFunc(out, func(a, b CodeView) int { return a.Expires.Compare(b.Expires) })
	return out, err
}

// DeleteCode withdraws a code. Error: ErrNotFound.
func (e *Engine) DeleteCode(ctx context.Context, id string) error {
	return e.cfg.Store.Update(ctx, func(tx Tx) error {
		codes, err := tx.Codes()
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(codes, func(c CodeRecord) bool { return c.ID == id }) {
			return ErrNotFound
		}
		return tx.DeleteCode(id)
	})
}

// readCode parses a pasted code and checks it isn't ours, nor a peer's.
func (e *Engine) readCode(ctx context.Context, text string) (wire.Code, error) {
	c, err := wire.ParseCode(text)
	switch {
	case errors.Is(err, wire.ErrPlainHTTP):
		return wire.Code{}, ErrPlainHTTP
	case err != nil:
		return wire.Code{}, ErrCodeInvalid
	case bytes.Equal(c.Key, e.current().Public()):
		return wire.Code{}, ErrOwnCode
	case !c.Expires.After(e.now()):
		return wire.Code{}, ErrCodeExpired
	}
	var known *PeerRecord
	err = e.cfg.Store.Read(ctx, func(tx Tx) error {
		var err error
		known, err = tx.PeerByKey(c.Key)
		return err
	})
	if err != nil {
		return wire.Code{}, err
	}
	if known != nil && !(c.Group != nil && known.State == Paired) {
		// To pair again with a peer that is gone, its operator removes it
		// first (MSFP core section 7.1, step 2).
		return wire.Code{}, ErrAlreadyPeer
	}
	return c, nil
}

// ReadCode says what a pasted code holds, for the operator to see before
// redeeming it. Errors: ErrCodeInvalid, ErrCodeExpired, ErrOwnCode,
// ErrAlreadyPeer, ErrPlainHTTP.
func (e *Engine) ReadCode(ctx context.Context, text string) (CodePreview, error) {
	c, err := e.readCode(ctx, text)
	if err != nil {
		return CodePreview{}, err
	}
	v := CodePreview{Address: c.Address, Fingerprint: wire.Fingerprint(c.Key), Expires: c.Expires.UTC(), GroupName: c.GroupName}
	if c.Group != nil {
		v.Group = wire.Encode64(c.Group)
	}
	return v, nil
}

// pairRequest is pair's body (MSFP core section 9.1).
type pairRequest struct {
	Address        string  `json:"address"`
	Name           string  `json:"name"`
	SecretHash     string  `json:"secretHash"`
	Proof          string  `json:"proof"`
	NextCommitment *string `json:"nextCommitment,omitempty"`
	Inbound        *bool   `json:"inbound,omitempty"`
}

// pairAnswer is pair's 200 answer.
type pairAnswer struct {
	Name           string `json:"name"`
	NextCommitment string `json:"nextCommitment,omitempty"`
}

// RedeemCode is this server's operator pasting another server's code:
// it fetches that server's discovery document (its base path), calls its
// pair, and adds it as a peer waiting for its operator (state theirs).
// While this server can't be called, its pair says so (inbound false,
// its address of the .invalid form). Errors: ErrCodeInvalid, ErrCodeExpired,
// ErrOwnCode, ErrAlreadyPeer, ErrPlainHTTP, ErrRefusedDestination,
// ErrUnreachable, ErrCodeRefused (used, withdrawn, expired there, or not
// its own).
func (e *Engine) RedeemCode(ctx context.Context, text string) (PeerView, error) {
	c, err := e.readCode(ctx, text)
	if err != nil {
		return PeerView{}, err
	}
	if c.Group != nil {
		// A group's code from a peer already: group-join, through the
		// pairing (MSFP groups sections 2 and 8.4).
		var p *PeerRecord
		if err := e.cfg.Store.Read(ctx, func(tx Tx) error {
			var err error
			p, err = tx.PeerByKey(c.Key)
			return err
		}); err != nil {
			return PeerView{}, err
		}
		if p != nil {
			return e.joinPaired(ctx, c, p)
		}
	}
	commitment := wire.Encode64(e.commitment())
	ours := e.named(e.address())
	req := pairRequest{Address: ours, Name: e.name(),
		SecretHash:     wire.Encode64(wire.SecretHash(c.Secret)),
		Proof:          wire.PairingProof(c.Secret, e.current().Public(), ours),
		NextCommitment: &commitment}
	if wire.Uncallable(ours) {
		no := false
		req.Inbound = &no
	}
	var ans pairAnswer
	maker := &PeerRecord{Address: c.Address, Key: c.Key, Inbound: InboundYes, Base: e.fetchBase(ctx, c.Address)}
	r, err := e.call(ctx, maker, callPair, req, &ans, false)
	switch {
	case err != nil:
		return PeerView{}, err
	case r.status == http.StatusConflict:
		return PeerView{}, ErrAlreadyPeer
	case r.status != http.StatusOK:
		return PeerView{}, ErrCodeRefused
	}
	// Its commitment is required (MSFP core section 9.1): without it, the
	// answer fails like a badly signed one, and nothing is kept.
	theirs, ok := wire.Decode64(ans.NextCommitment, 32)
	if !ok {
		return PeerView{}, ErrUnreachable.with(errors.New("a pair answer without a next key commitment"))
	}
	id, err := e.newID("p")
	if err != nil {
		return PeerView{}, err
	}
	now := e.now().Truncate(time.Second)
	p := PeerRecord{ID: id, Key: c.Key, Name: cleanName(ans.Name, c.Address), Created: now, Seen: now,
		State: Theirs, Address: c.Address, Base: maker.Base, Inbound: InboundYes,
		Commitment: theirs, CommitmentFrom: "pair", CommitmentTold: true, Limits: *e.cfg.NewPairing}
	err = e.cfg.Store.Update(ctx, func(tx Tx) error {
		// Paired meanwhile (its own code, redeemed by it at once)?
		if q, err := tx.PeerByKey(c.Key); err != nil || q != nil {
			if err == nil {
				err = ErrAlreadyPeer
			}
			return err
		}
		// Redeeming a code is consent to pair again (MSFP core section
		// 7.1, step 5).
		hash := sha256.Sum256(c.Key)
		if err := tx.Unforget(hash[:]); err != nil {
			return err
		}
		if c.Group != nil {
			// A group's code: this server joins it through the maker
			// (MSFP groups section 4).
			if err := e.joinVia(tx, c, p.ID); err != nil {
				return err
			}
		}
		return tx.PutPeer(p)
	})
	if err != nil {
		return PeerView{}, err
	}
	e.log.Info("msfp: a code redeemed, waiting for the other operator", "peer", p.ID, "name", p.Name)
	return e.view(&p), nil
}

// pair is another server redeeming one of our codes (MSFP core sections
// 7.1, step 4, and 9.1).
func (e *Engine) pair(req *request) response {
	var body pairRequest
	if wire.DecodeJSON(req.body, &body) != nil || wire.CheckAddress(body.Address) != nil {
		return e.pairRefused(req)
	}
	uncallable := body.Inbound != nil && !*body.Inbound
	if wire.CheckInbound(uncallable, body.Address) != nil {
		return e.pairRefused(req)
	}
	hash, ok := wire.Decode64(body.SecretHash, 32)
	var commitment []byte
	if body.NextCommitment != nil {
		var good bool
		commitment, good = wire.Decode64(*body.NextCommitment, 32)
		ok = ok && good
	} else {
		ok = false
	}
	if !ok {
		return e.pairRefused(req)
	}
	id, err := e.newID("p")
	if err != nil {
		return refuse(probInternal)
	}
	now := e.now().Truncate(time.Second)
	p := PeerRecord{ID: id, Key: req.v.Key, Name: cleanName(body.Name, body.Address), Created: now, Seen: now,
		State: Ours, Address: body.Address, OurAddress: req.ours, Inbound: InboundYes,
		Commitment: commitment, CommitmentFrom: "pair", CommitmentTold: true, Limits: *e.cfg.NewPairing}
	if uncallable {
		p.Inbound = InboundNo
	}
	var known bool
	err = e.cfg.Store.Update(req.r.Context(), func(tx Tx) error {
		// The code and the proof first, then whether the key is known
		// (MSFP core section 9.1).
		c, err := tx.CodeByHash(hash)
		if err != nil {
			return err
		}
		if c == nil || !c.Expires.After(e.now()) {
			return ErrNotFound
		}
		secret, err := e.cfg.Sealer.Open(c.Secret, sealCode)
		if err != nil || !wire.CheckPairingProof(secret, req.v.Key, body.Address, body.Proof) {
			return ErrNotFound
		}
		if q, err := tx.PeerByKey(req.v.Key); err != nil || q != nil {
			known = q != nil
			if err == nil {
				err = ErrAlreadyPeer // the code stays live
			}
			return err
		}
		// One use spent; the code goes with its last (core section 5.1).
		if c.Uses > 1 {
			c.Uses--
			if err := tx.PutCode(*c); err != nil {
				return err
			}
		} else if err := tx.DeleteCode(c.ID); err != nil {
			return err
		}
		p.JoiningGroup = c.Group
		keyHash := sha256.Sum256(req.v.Key)
		if err := tx.Unforget(keyHash[:]); err != nil {
			return err
		}
		return tx.PutPeer(p)
	})
	switch {
	case errors.Is(err, ErrNotFound):
		return e.pairRefused(req)
	case errors.Is(err, ErrAlreadyPeer) && known:
		// Already a peer: told plainly, signed, with an empty body.
		return response{status: http.StatusConflict}
	case err != nil:
		e.log.Error("msfp: a pairing code couldn't be redeemed", "error", err)
		if req.signer == nil {
			return plain404
		}
		return refuse(probInternal)
	}
	e.log.Info("msfp: a server redeemed a code, waiting for the operator", "peer", p.ID, "name", p.Name)
	e.notify(PeerWaiting{Peer: p.ID, Name: p.Name, Fingerprint: wire.Fingerprint(p.Key)})
	if !uncallable {
		// Its base path, from its discovery document (MSFP core section
		// 11), for every call to it.
		e.background(func(ctx context.Context) { e.keepBase(ctx, p.ID, p.Address) })
	}
	return answer(pairAnswer{Name: e.name(), NextCommitment: wire.Encode64(e.commitment())})
}

// pairRefused is pair's 404: plain to a stranger, signed to a key this
// server knows.
func (e *Engine) pairRefused(req *request) response {
	if req.signer == nil {
		return plain404
	}
	return refuse(probNotFound)
}
