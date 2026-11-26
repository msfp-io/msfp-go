package msfp

import (
	"errors"
	"net/http"
	"testing"
)

// A peer's signed refusal as the engine's error (engine.md section 6):
// each problem this engine sends reads back as its own error, a status
// told apart by its type where one status carries several; anything
// else, unreachable.
func TestRefusalErrors(t *testing.T) {
	want := map[problemType]error{
		probNotFound:     ErrNotFound,
		probNotSupported: ErrNotSupported,
		probInvalid:      ErrInvalidRequest,
		probRotation:     ErrRotationRefused,
		probStreamLimit:  ErrStreamLimitReached,
		probNotPlayable:  ErrNotPlayable,
		probDelivery:     ErrDeliveryRefused,
		probStopped:      ErrPlaybackStopped,
		probRateLimited:  ErrRateLimited,
		probInternal:     ErrPeerFailed,
		probServerBusy:   ErrServerBusy,
	}
	for typ, p := range problems {
		w, ok := want[typ]
		if !ok {
			t.Errorf("%s: no error expected of it", typ)
			continue
		}
		if got := refusalError(p.status, string(typ)); !errors.Is(got, w) {
			t.Errorf("%d %s: %v, want %v", p.status, typ, got, w)
		}
	}
	// A type this engine doesn't know: by its status alone.
	for status, w := range map[int]error{404: ErrNotFound, 400: ErrInvalidRequest, 409: ErrStreamLimitReached, 410: ErrPlaybackStopped,
		429: ErrRateLimited, 503: ErrServerBusy, 500: ErrPeerFailed} {
		if got := refusalError(status, "something-new"); !errors.Is(got, w) {
			t.Errorf("%d something-new: %v, want %v", status, got, w)
		}
	}
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, 418, http.StatusBadGateway} {
		if got := refusalError(status, ""); !errors.Is(got, ErrUnreachable) {
			t.Errorf("%d: %v, want unreachable", status, got)
		}
	}
}
