package msfp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// ReadFailure says why this server couldn't read a paired peer's titles:
// one of a closed set of codes, the same in every MSFP engine, for a
// host's pages to name. It never holds an address, a URL, a file name,
// or anything the peer wrote: the detail is in the error, for the logs.
type ReadFailure string

// The reasons a reading fails. A host shows each as it likes; the
// texts here are what each means.
const (
	// ReadUnreachable: no answer (no route, refused, a name that doesn't
	// resolve, TLS failing), or an address this server never connects to.
	ReadUnreachable ReadFailure = "unreachable"
	// ReadTimeout: the peer, or the reading, took too long (a call's 15
	// seconds, a reading's 5 minutes, or the host's own deadline).
	ReadTimeout ReadFailure = "timeout"
	// ReadCertificateRefused: its certificate is neither publicly
	// trusted nor one it named.
	ReadCertificateRefused ReadFailure = "certificate-refused"
	// ReadRefusedUnsigned: an answer not signed by its key, a refusal or
	// not: it doesn't hear this server's key (it ended the pairing, or
	// lost its records), or something else answers at its address.
	ReadRefusedUnsigned ReadFailure = "refused-unsigned"
	// ReadRefused: a refusal signed by its key; LastReading.Refusal is
	// its MSFP problem type (core section 8.2).
	ReadRefused ReadFailure = "refused"
	// ReadNotPaired: it isn't paired with this server (any more), or it
	// must pair again (it never moved to this server's new key).
	ReadNotPaired ReadFailure = "not-paired"
	// ReadSuspended: it revoked its key; nothing is asked of it.
	ReadSuspended ReadFailure = "suspended"
	// ReadUncallable: it can't be called (its own "this server can't be
	// called", or an address that never resolves).
	ReadUncallable ReadFailure = "uncallable"
	// ReadNoCommonVersion: the two servers have no core version in common
	// (PeerView.Core is -1).
	ReadNoCommonVersion ReadFailure = "no-common-version"
	// ReadCatalogOff: the catalog part isn't on between the two.
	ReadCatalogOff ReadFailure = "catalog-off"
	// ReadOlderDraft: its answers are in the shapes of the previous draft
	// of MSFP version 0 (before proposals 16 and 17): it runs an older
	// engine, and must be updated for its titles to be read. Both drafts
	// announce core 0 with the same parts, so the hello can't tell; the
	// reading does, from what the old shapes look like (olderDraftPage).
	ReadOlderDraft ReadFailure = "older-draft"
	// ReadNotUnderstood: an answer that couldn't be read: coded, too
	// large, not JSON, not of the call's form, or a reading that breaks
	// the catalog's rules (pages too large, changes and whole list mixed).
	ReadNotUnderstood ReadFailure = "answer-not-understood"
	// ReadInternal: this server's own failure (its store, say).
	ReadInternal ReadFailure = "internal"
)

// ReadFailures are every ReadFailure, in the order above.
var ReadFailures = []ReadFailure{ReadUnreachable, ReadTimeout, ReadCertificateRefused, ReadRefusedUnsigned,
	ReadRefused, ReadNotPaired, ReadSuspended, ReadUncallable, ReadNoCommonVersion, ReadCatalogOff,
	ReadOlderDraft, ReadNotUnderstood, ReadInternal}

// LastReading is how a peer's last reading went (PeerView.Reading): kept
// in memory, from the engine's start.
type LastReading struct {
	// At is when it ended.
	At time.Time
	// Failure is why it failed; "" when it read.
	Failure ReadFailure
	// Refusal is, for ReadRefused, the problem type of the peer's
	// refusal, one of MSFP core section 8.2's ("not-found",
	// "rate-limited"…); "" for another type, or another failure.
	Refusal string
}

// errOlderDraft is a reading whose answers are in the previous draft's
// shapes (wrapped in ErrUnreachable).
var errOlderDraft = errors.New("answers in the shapes of the previous draft of version 0")

// signedRefusal is a peer's signed refusal, as the detail of the error
// it is (refused).
type signedRefusal struct {
	status  int
	problem string
}

func (r signedRefusal) Error() string { return fmt.Sprintf("a signed refusal: %d", r.status) }

// readEnded keeps how a reading of p ended, err nil for one that read;
// false when it isn't one to keep: the engine holding off (ErrBackingOff,
// which keeps the failure before it), or the host cancelling.
func (e *Engine) readEnded(p *PeerRecord, err error) (LastReading, bool) {
	f, refusal, ok := e.readFailure(p, err)
	if !ok {
		return LastReading{}, false
	}
	last := LastReading{At: e.now(), Failure: f, Refusal: refusal}
	r := &e.reading
	r.mu.Lock()
	if r.last == nil {
		r.last = map[string]LastReading{}
	}
	r.last[p.ID] = last
	r.mu.Unlock()
	return last, true
}

// lastReading is p's last reading; false for none since the start.
func (e *Engine) lastReading(id string) (LastReading, bool) {
	r := &e.reading
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.last[id]
	return last, ok
}

// readFailure is why a reading of p failed with err.
func (e *Engine) readFailure(p *PeerRecord, err error) (f ReadFailure, refusal string, ok bool) {
	var signed signedRefusal
	var ne net.Error
	switch {
	case err == nil:
		return "", "", true
	case errors.Is(err, ErrBackingOff), errors.Is(err, context.Canceled):
		return "", "", false
	case errors.As(err, &signed):
		if _, known := problems[problemType(signed.problem)]; known {
			refusal = signed.problem
		}
		return ReadRefused, refusal, true
	case errors.Is(err, errOlderDraft):
		return ReadOlderDraft, "", true
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return ReadTimeout, "", true
	case errors.Is(err, errNotUnderstood):
		return ReadNotUnderstood, "", true
	case errors.Is(err, errNotHeard), errors.Is(err, errUnsigned):
		return ReadRefusedUnsigned, "", true
	case errors.Is(err, ErrCertificateRefused):
		return ReadCertificateRefused, "", true
	case errors.Is(err, ErrUnreachable), errors.Is(err, ErrRefusedDestination):
		return ReadUnreachable, "", true
	case errors.Is(err, ErrSuspended):
		return ReadSuspended, "", true
	case errors.Is(err, ErrUncallable):
		return ReadUncallable, "", true
	case errors.Is(err, ErrNotSupported):
		if core, _ := e.agreed(p); core < 0 {
			return ReadNoCommonVersion, "", true
		}
		return ReadCatalogOff, "", true
	case errors.Is(err, ErrNotPaired), errors.Is(err, ErrKeyLost), errors.Is(err, ErrUnknown), errors.Is(err, ErrNotFound):
		return ReadNotPaired, "", true
	}
	return ReadInternal, "", true
}

// olderDraftPage tells, from a page of titles of a peer whose libraries
// all carried a kind, whether its answers are in the shapes of the
// previous draft of version 0: old, when every title carries a kind
// that is the one its key names (the previous draft's required member,
// gone from this one), and at least one film, episode or track is on the
// page and none of them carries copies (this draft's required member,
// which the previous one didn't have). current, when a film, episode or
// track carries copies: the page is this draft's, and nothing more need
// be checked. A single title missing copies tells nothing (an owner's
// mistake): the three marks together do, at the cost of reading members
// already read.
func olderDraftPage(titles []json.RawMessage) (old, current bool) {
	playable := 0
	for _, raw := range titles {
		var t struct {
			Key    string          `json:"key"`
			Kind   *string         `json:"kind"`
			Copies json.RawMessage `json:"copies"`
		}
		if json.Unmarshal(raw, &t) != nil {
			return false, false
		}
		k, ok := wire.ParseKey(t.Key)
		if k.Playable && t.Copies != nil {
			return false, true
		}
		if !ok || t.Kind == nil || *t.Kind != k.Kind {
			return false, false
		}
		if k.Playable {
			playable++
		}
	}
	return playable > 0, false
}
