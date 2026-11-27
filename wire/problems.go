package wire

import "net/http"

// Problems (MSFP core section 8.2): the types of a problem document, as
// any server answering MSFP's exchanges gives them, with their status and
// a title (titles are examples; callers branch on the type).
// Playback's refusals (playback sections 2.5, 3, 4) are among them.
const (
	ProblemNotFound        = "not-found"
	ProblemNotSupported    = "not-supported"
	ProblemInvalid         = "invalid-request"
	ProblemRotationRefused = "rotation-refused"
	ProblemStreamLimit     = "stream-limit-reached"
	ProblemNotPlayable     = "not-playable"
	ProblemDeliveryRefused = "delivery-refused"
	ProblemStopped         = "playback-stopped"
	ProblemRateLimited     = "rate-limited"
	ProblemInternal        = "internal-error"
	ProblemServerBusy      = "server-busy"
)

var problems = map[string]struct {
	status int
	title  string
}{
	ProblemNotFound:        {http.StatusNotFound, "Not found"},
	ProblemNotSupported:    {http.StatusNotFound, "Not supported"},
	ProblemInvalid:         {http.StatusBadRequest, "Invalid request"},
	ProblemRotationRefused: {http.StatusConflict, "Rotation refused"},
	ProblemStreamLimit:     {http.StatusConflict, "Too many streams on this account"},
	ProblemNotPlayable:     {http.StatusConflict, "This item can't be played"},
	ProblemDeliveryRefused: {http.StatusConflict, "This delivery isn't served"},
	ProblemStopped:         {http.StatusGone, "Playback was stopped"},
	ProblemRateLimited:     {http.StatusTooManyRequests, "Too many requests"},
	ProblemInternal:        {http.StatusInternalServerError, "Internal error"},
	ProblemServerBusy:      {http.StatusServiceUnavailable, "The server is busy; try again shortly"},
}

// Problem is a problem type's status and title; false for a type MSFP
// doesn't list.
func Problem(typ string) (status int, title string, ok bool) {
	p, ok := problems[typ]
	return p.status, p.title, ok
}
