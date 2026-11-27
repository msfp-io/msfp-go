package msfp

import (
	"errors"
	"net/http"

	"github.com/msfp-io/msfp-go/wire"
)

// Error is an error of the engine: one code of MSFP's shared vocabulary
// (Code), and, for the logs, what happened (Err). Match it with
// errors.Is against the Err* values below:
//
//	if errors.Is(err, msfp.ErrCodeExpired) { … }
//
// The text of Err may hold detail for an operator's logs, never a
// secret (MSFP core section 14.1); it is never sent to a peer.
type Error struct {
	Code ErrorCode
	Err  error
}

// ErrorCode names an error, the same in every MSFP library.
type ErrorCode string

func (e *Error) Error() string {
	if e.Err != nil {
		return "msfp: " + string(e.Code) + ": " + e.Err.Error()
	}
	return "msfp: " + string(e.Code)
}

// Unwrap gives the detail, for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error of the same code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// with is e with detail err, for the logs.
func (e *Error) with(err error) error {
	return &Error{Code: e.Code, Err: err}
}

// A peer's refusals, as the engine reports them for calls it makes (MSFP
// Media base 4, MSFP core section 8.2).
var (
	// ErrNotFound: no such thing here, or a peer's signed 404 not-found
	// (no such title, or not paired yet).
	ErrNotFound = &Error{Code: "NotFound"}
	// ErrUnknown: the peer answered a hello with a signed 404: it doesn't
	// know this server, and is now marked gone.
	ErrUnknown = &Error{Code: "Unknown"}
	// ErrNotSupported: the part isn't on with the peer.
	ErrNotSupported = &Error{Code: "NotSupported"}
	// ErrInvalidRequest: the peer answered 400: a bug on this side.
	ErrInvalidRequest = &Error{Code: "InvalidRequest"}
	// ErrAlreadyPeer: the peer already knows this server's key.
	ErrAlreadyPeer = &Error{Code: "AlreadyPeer"}
	// ErrRotationRefused: the peer refused a rotation.
	ErrRotationRefused = &Error{Code: "RotationRefused"}
	// ErrStreamLimitReached: the pairing's streams are all in use.
	ErrStreamLimitReached = &Error{Code: "StreamLimitReached"}
	// ErrNotPlayable: the title can't be played.
	ErrNotPlayable = &Error{Code: "NotPlayable"}
	// ErrPlaybackStopped: the playback is over.
	ErrPlaybackStopped = &Error{Code: "PlaybackStopped"}
	// ErrDeliveryRefused: the owning server won't serve the delivery
	// asked of that copy, as such (MSFP Media playing 2.5): ask again with
	// another delivery (Instead names those it would serve, when it says),
	// or without one.
	ErrDeliveryRefused = &Error{Code: "DeliveryRefused"}
	// ErrTargetRefused: the owning server doesn't make a target the play
	// names: a remux's media type, a transcode codec, a range, a copy the
	// segment format can't carry, a subtitle to burn (MSFP Media playing
	// 2.5): ask again with other targets (Instead), another delivery, or
	// without one.
	ErrTargetRefused = &Error{Code: "TargetRefused"}
	// ErrGone: the peer held the title for this server and no longer
	// does (MSFP Media base 4): drop it; its titles are read again.
	ErrGone = &Error{Code: "Gone"}
	// ErrCopyChanged: a restart's copy isn't the one being read any more
	// (another etag): its bytes can't follow; read the title again.
	ErrCopyChanged = &Error{Code: "CopyChanged"}
	// ErrRateLimited: the peer asked this server to wait; calls hold
	// until then.
	ErrRateLimited = &Error{Code: "RateLimited"}
	// ErrServerBusy: the peer (or this server) is busy now.
	ErrServerBusy = &Error{Code: "ServerBusy"}
	// ErrPeerFailed: the peer answered 500.
	ErrPeerFailed = &Error{Code: "PeerFailed"}
	// ErrUnreachable: no answer, an unsigned or badly signed one, or a
	// refused certificate.
	ErrUnreachable = &Error{Code: "Unreachable"}
)

// The engine's own refusals, before or instead of a call.
var (
	ErrBackingOff  = &Error{Code: "BackingOff"}  // the peer asked to wait, or didn't answer lately
	ErrUncallable  = &Error{Code: "Uncallable"}  // the peer can't be called
	ErrSuspended   = &Error{Code: "Suspended"}   // the peer's key was revoked
	ErrKeyLost     = &Error{Code: "KeyLost"}     // the peer never moved to this server's new key
	ErrNotPaired   = &Error{Code: "NotPaired"}   // the peer isn't paired
	ErrNotWaiting  = &Error{Code: "NotWaiting"}  // the peer isn't waiting for this server's operator
	ErrCodeInvalid = &Error{Code: "CodeInvalid"} // not a pairing code
	ErrCodeExpired = &Error{Code: "CodeExpired"} // the code's 24 hours are over
	ErrOwnCode     = &Error{Code: "OwnCode"}     // the code is this server's own
	ErrCodeRefused = &Error{Code: "CodeRefused"} // the code's server refused it: used, withdrawn, or not its own
	ErrNoCodes     = &Error{Code: "NoCodes"}     // this server can't be called, so it makes no codes
	ErrCodeOptions = &Error{Code: "CodeOptions"} // uses or lifetime out of range

	ErrNoGroups  = &Error{Code: "NoGroups"}  // the store keeps no groups (GroupsTx)
	ErrNoRight   = &Error{Code: "NoRight"}   // this server's role in the group doesn't allow it
	ErrNotMember = &Error{Code: "NotMember"} // not a member of the group (or the group is gone)
	// ErrNoWayIn: this server has no pairing through the group, so a mail
	// it sends can't leave it (groups section 6, "No way in").
	ErrNoWayIn = &Error{Code: "NoWayIn"}
	// ErrAlreadyMember: already in the group, banned from it, or waiting
	// to join it (a group's code from a peer already).
	ErrAlreadyMember = &Error{Code: "AlreadyMember"}

	ErrNoNextKey        = &Error{Code: "NoNextKey"}        // the next key's seed isn't on the server
	ErrNotNextKey       = &Error{Code: "NotNextKey"}       // the text given isn't the committed next key
	ErrNoPending        = &Error{Code: "NoPending"}        // no rotation waits for the operator
	ErrKeyTaken         = &Error{Code: "KeyTaken"}         // the key is another peer's
	ErrRotationUnderWay = &Error{Code: "RotationUnderWay"} // a rotation isn't over yet

	ErrInvalidAddress     = &Error{Code: "InvalidAddress"}     // not an address in MSFP's form
	ErrPlainHTTP          = &Error{Code: "PlainHTTP"}          // an http address: MSFP is HTTPS only
	ErrRefusedDestination = &Error{Code: "RefusedDestination"} // an address the engine never connects to
	ErrCertificateRefused = &Error{Code: "CertificateRefused"} // neither trusted nor named
	ErrMoveFailed         = &Error{Code: "MoveFailed"}         // a new address doesn't answer

	ErrInvalidLimits       = &Error{Code: "InvalidLimits"}       // limits out of range
	ErrInvalidReason       = &Error{Code: "InvalidReason"}       // not an ending a host may give (StopPlayback)
	ErrRangeNotSatisfiable = &Error{Code: "RangeNotSatisfiable"} // a malformed Range, or out of the file
	ErrSealed              = &Error{Code: "Sealed"}              // a sealed value didn't open
	ErrInternal            = &Error{Code: "Internal"}            // anything else; logged, never shown to a peer
)

// problemType is a problem document's type, as MSFP Media base 4 and
// MSFP core section 8.2 list them (wire.Problem gives their statuses and
// titles).
type problemType string

const (
	probNotFound     problemType = wire.ProblemNotFound
	probNotSupported problemType = wire.ProblemNotSupported
	probInvalid      problemType = wire.ProblemInvalid
	probRotation     problemType = wire.ProblemRotationRefused
	probStreamLimit  problemType = wire.ProblemStreamLimit
	probNotPlayable  problemType = wire.ProblemNotPlayable
	probDelivery     problemType = wire.ProblemDeliveryRefused
	probTarget       problemType = wire.ProblemTargetRefused
	probUnreachable  problemType = wire.ProblemUnreachable
	probStopped      problemType = wire.ProblemStopped
	probRateLimited  problemType = wire.ProblemRateLimited
	probInternal     problemType = wire.ProblemInternal
	probServerBusy   problemType = wire.ProblemServerBusy
)

// refusalError is what a peer's signed refusal means, as an engine
// error (engine.md section 6). hello's signed 404 is ErrUnknown, and
// is handled by its caller.
func refusalError(status int, typ string) error {
	switch {
	case status == http.StatusNotFound && typ == string(probNotSupported):
		return ErrNotSupported
	case status == http.StatusNotFound && typ == wire.ProblemGone:
		return ErrGone
	case status == http.StatusNotFound:
		return ErrNotFound
	case status == http.StatusBadRequest:
		return ErrInvalidRequest
	case status == http.StatusConflict && typ == string(probRotation):
		return ErrRotationRefused
	case status == http.StatusConflict && typ == string(probNotPlayable):
		return ErrNotPlayable
	case status == http.StatusConflict && typ == string(probDelivery):
		return ErrDeliveryRefused
	case status == http.StatusConflict && typ == string(probTarget):
		return ErrTargetRefused
	case status == http.StatusConflict:
		return ErrStreamLimitReached
	case status == http.StatusGone:
		return ErrPlaybackStopped
	case status == http.StatusTooManyRequests:
		return ErrRateLimited
	case status == http.StatusServiceUnavailable:
		return ErrServerBusy
	case status == http.StatusInternalServerError:
		return ErrPeerFailed
	}
	return ErrUnreachable.with(errors.New("an unexpected status"))
}

// insteadErr carries what a refusal's instead names (MSFP Media base 4).
type insteadErr struct{ list []wire.Instead }

func (e *insteadErr) Error() string { return "the peer names what it would serve instead" }

// Instead is what a peer's delivery-refused or target-refused of a play
// names that it would serve instead, of the same copy and track for the
// same purpose (MSFP Media base 4): at most 4, each the delivery with
// its media type or targets ("" delivery: the baseline). A hint: a play
// asking for one may still be refused. None for any other error.
func Instead(err error) []wire.Instead {
	var ie *insteadErr
	if errors.As(err, &ie) {
		return ie.list
	}
	return nil
}
