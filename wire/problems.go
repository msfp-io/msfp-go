package wire

import "net/http"

// Problems: the types of a problem document, with their status, a title
// (titles are examples; callers branch on the type) and whether the
// refusal is final or temporary. The language's own are MSFP Media
// base 4's; MSFP adds, as a carrier, its own (MSFP core section 8.2):
// not-supported, rotation-refused, stream-limit-reached and
// rate-limited.
const (
	// MSFP Media base 4: the language's refusals.
	ProblemInvalid         = "invalid-request"
	ProblemNotFound        = "not-found"
	ProblemGone            = "gone"
	ProblemDeliveryRefused = "delivery-refused"
	ProblemTargetRefused   = "target-refused"
	ProblemNotPlayable     = "not-playable"
	ProblemStopped         = "playback-stopped"
	ProblemInternal        = "internal-error"
	ProblemServerBusy      = "server-busy"
	ProblemUnreachable     = "unreachable"
	ProblemTicketRefused   = "ticket-refused"

	// MSFP core section 8.2: MSFP's own, as a carrier.
	ProblemNotSupported    = "not-supported"
	ProblemRotationRefused = "rotation-refused"
	ProblemStreamLimit     = "stream-limit-reached"
	ProblemRateLimited     = "rate-limited"
)

type problem struct {
	status    int
	title     string
	temporary bool
	language  bool // MSFP Media's, not MSFP's own
}

var problems = map[string]problem{
	ProblemInvalid:         {http.StatusBadRequest, "Invalid request", false, true},
	ProblemNotFound:        {http.StatusNotFound, "Not found", false, true},
	ProblemGone:            {http.StatusNotFound, "No longer held", false, true},
	ProblemDeliveryRefused: {http.StatusConflict, "This delivery is refused", false, true},
	ProblemTargetRefused:   {http.StatusConflict, "This target is refused", false, true},
	ProblemNotPlayable:     {http.StatusConflict, "This item can't be played", false, true},
	ProblemStopped:         {http.StatusGone, "Playback was stopped", false, true},
	ProblemInternal:        {http.StatusInternalServerError, "Internal error", true, true},
	ProblemServerBusy:      {http.StatusServiceUnavailable, "The server is busy; try again shortly", true, true},
	ProblemUnreachable:     {http.StatusServiceUnavailable, "The media can't be reached now", true, true},
	ProblemTicketRefused:   {http.StatusNotFound, "Ticket refused", false, true},

	ProblemNotSupported:    {http.StatusNotFound, "Not supported", false, false},
	ProblemRotationRefused: {http.StatusConflict, "Rotation refused", false, false},
	ProblemStreamLimit:     {http.StatusConflict, "Too many streams on this account", true, false},
	ProblemRateLimited:     {http.StatusTooManyRequests, "Too many requests", true, false},
}

// Problem is a problem type's status and title; false for a type neither
// MSFP Media nor MSFP lists.
func Problem(typ string) (status int, title string, ok bool) {
	p, ok := problems[typ]
	return p.status, p.title, ok
}

// Temporary says whether a refusal of that type may be retried (MSFP
// Media base 4: after retryAfter when it carries one); false for a final
// one and for a type not listed.
func Temporary(typ string) bool { return problems[typ].temporary }

// LanguageProblem says whether typ is one of MSFP Media's own refusals
// (base 4), not a carrier's: a carrier's type is never one of them.
func LanguageProblem(typ string) bool { return problems[typ].language }

// What a refusal means to its caller (Meaning), as MSFP Media base 4 and
// playing 2.5 read it.
const (
	MeansBadAsk      = "bad ask"      // 400: gives up
	MeansNotFound    = "not found"    // 404: the title isn't held
	MeansGone        = "gone"         // 404 gone: drop the title, read the titles again
	MeansAskElse     = "ask else"     // 409 on play: another delivery, other targets (ReadInstead)
	MeansNotPlayable = "not playable" // 409 not-playable on play
	MeansStopped     = "stopped"      // 410: the playback is over
	MeansBusy        = "busy"         // 409 of a carrier on play, 429, 500, 503: busy now, try later
	MeansOther       = "other"        // none of these: as the call's own text says
)

// Meaning is what a refusal of status and type means to its caller
// (MSFP Media base 4; for play, playing 2.5): a type the table lists,
// with its own status, means what the table says; any other is read by
// its status alone: 400 a bad ask, 404 not found, 410 no such playback,
// 429, 500 and 503 temporary, and a 409 of play (onPlay) busy now. A
// carrier's own types are read so too, but MSFP's not-supported and
// rotation-refused, whose meanings are MSFP's (MeansOther here).
func Meaning(status int, typ string, onPlay bool) string {
	if p, ok := problems[typ]; ok && p.status == status {
		switch typ {
		case ProblemGone:
			return MeansGone
		case ProblemDeliveryRefused, ProblemTargetRefused:
			if onPlay {
				return MeansAskElse
			}
		case ProblemNotPlayable:
			if onPlay {
				return MeansNotPlayable
			}
		case ProblemNotSupported, ProblemRotationRefused:
			return MeansOther
		}
	}
	switch status {
	case http.StatusBadRequest:
		return MeansBadAsk
	case http.StatusNotFound:
		return MeansNotFound
	case http.StatusGone:
		return MeansStopped
	case http.StatusConflict:
		if onPlay {
			return MeansBusy
		}
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return MeansBusy
	}
	return MeansOther
}

// MaxInstead is how many entries of a refusal's instead a caller reads
// (MSFP Media base 4).
const MaxInstead = 4

// Instead is an entry of a refusal's instead (MSFP Media base 4, on
// delivery-refused and target-refused): the members of a play request
// that name what to serve, which the source would serve of the same copy
// and track, for the same purpose. It is a hint: a play asking for it
// may still be refused.
type Instead struct {
	Delivery  string            `json:"delivery"`
	MediaType string            `json:"mediaType,omitempty"`
	Transcode *TranscodeTargets `json:"transcode,omitempty"`
}

// ReadInstead reads the instead of a refusal's body, for a play of
// purpose ("" for play): none when the body isn't an I-JSON object or its
// instead isn't an array; of its first MaxInstead entries, those that
// are objects whose members keep the forms of play's request (delivery
// present, a token, mediaType a media type with delivery remux only and then
// required, a transcode object of its forms with delivery transcode
// only, no null) and whose delivery the purpose takes (PurposeAllows);
// the others skipped.
func ReadInstead(body []byte, purpose string) []Instead {
	tree, err := strictTree(body, true)
	if err != nil {
		return nil
	}
	o, ok := tree.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := o["instead"].([]any)
	if !ok {
		return nil
	}
	if purpose == "" {
		purpose = PurposePlay
	}
	var out []Instead
	for i, e := range list {
		if i == MaxInstead {
			break
		}
		m, ok := e.(map[string]any)
		if !ok || holdsNull(m) {
			continue
		}
		var r PlayRequest
		if _, has := m["delivery"]; !has {
			continue
		}
		if members(m).readDelivery(&r) != nil || !r.TrackIDsOK() || !PurposeAllows(purpose, r.Delivery, r.Transcode) {
			continue
		}
		out = append(out, Instead{Delivery: r.Delivery, MediaType: r.MediaType, Transcode: r.Transcode})
	}
	return out
}

// What a caller tells its viewer of a refused play (PlayRefusalReading).
const (
	TellsAskAgain    = "asks again"   // delivery-refused, target-refused: another delivery or targets
	TellsNotPlayable = "not playable" // not-playable; any 400, 404 or 500
	TellsBusy        = "busy now"     // any other 409, 429, 503, an unsigned refusal
)

// PlayRefusalReading is what a caller makes of a refused play (MSFP Media
// playing 2.5, base 4): what it tells the viewer; DropTitle for gone (the
// title dropped, the titles read again); Instead, on delivery-refused and
// target-refused (ReadInstead); RetryAfter, on a refusal read as busy
// now, 1 to 3,600 seconds when the body gives one.
type PlayRefusalReading struct {
	Tells      string    `json:"tells"`
	DropTitle  bool      `json:"dropTitle,omitempty"`
	Instead    []Instead `json:"instead,omitempty"`
	RetryAfter int       `json:"retryAfter,omitempty"`
}

// ReadPlayRefusal reads a refusal of play of status and body, for a play
// of purpose ("" for play). signed says whether the carrier vouches for
// it (MSFP: signed by the peer's key): one it doesn't is busy now, as are
// a 429 and a 503. A 409 is read by its type: delivery-refused and
// target-refused ask again, not-playable is not playable, any other type
// (a carrier's, one not known, none, a body that isn't JSON) busy now.
// Any other refusal is not playable; gone drops the title too.
func ReadPlayRefusal(status int, signed bool, body []byte, purpose string) PlayRefusalReading {
	typ := ""
	if tree, err := strictTree(body, true); err == nil {
		if o, ok := tree.(map[string]any); ok {
			typ, _ = o["type"].(string)
		}
	}
	busy := func() PlayRefusalReading { return PlayRefusalReading{Tells: TellsBusy, RetryAfter: RetryAfter(body)} }
	switch {
	case !signed || status == 429 || status == 503:
		if !signed {
			return PlayRefusalReading{Tells: TellsBusy}
		}
		return busy()
	case status == 409 && (typ == ProblemDeliveryRefused || typ == ProblemTargetRefused):
		return PlayRefusalReading{Tells: TellsAskAgain, Instead: ReadInstead(body, purpose)}
	case status == 409 && typ == ProblemNotPlayable:
		return PlayRefusalReading{Tells: TellsNotPlayable}
	case status == 409:
		return busy()
	}
	return PlayRefusalReading{Tells: TellsNotPlayable, DropTitle: status == 404 && typ == ProblemGone}
}
