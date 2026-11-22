package wire

import (
	"slices"
	"time"
)

// progress and stop (MSFP playback sections 3, 4, 6.1 and 7, music
// section 5.2): how an owning server reads them, and what they do to the
// playback they name.

// The calls a report is.
const (
	CallProgress = "progress"
	CallStop     = "stop"
)

// The bounds of a report's at (playback section 3), around the owning
// server's clock.
const (
	AtBefore = 30 * 24 * time.Hour
	AtAfter  = 5 * time.Minute
)

// Report is a progress or a stop, as ReadReport reads it.
type Report struct {
	ID string
	// PositionMs, when HasPosition.
	PositionMs  int64
	HasPosition bool
	// Paused, progress only.
	Paused bool
	// At is when the viewer was there: the request's at, when AtSaid,
	// else now.
	At     time.Time
	AtSaid bool
	// Title is the track playing, of a music queue, when TitleSaid.
	Title     string
	TitleSaid bool
}

// reportMembers are the members each call knows.
var reportMembers = map[string][]string{
	CallProgress: {"id", "positionMs", "paused", "at", "title"},
	CallStop:     {"id", "positionMs", "at", "title"},
}

// ReadReport reads a progress or a stop (call) at now, refusing (400, a
// *PlayRefusal) what sections 3 and 4 refuse: a body that isn't an I-JSON
// object or holds null in a member it knows, an id that isn't a playback
// ID, positionMs out of 0 to MaxPositionMs, paused not a boolean, at not
// an integer from now − AtBefore to now + AtAfter (Unix seconds), a title
// that isn't a string. Whether title is the playback's is Apply's.
func ReadReport(call string, body []byte, now time.Time) (Report, error) {
	o, err := requestObject(body, reportMembers[call])
	if err != nil {
		return Report{}, err
	}
	var r Report
	id, _, err := o.text("id")
	if err != nil || !PlayID(id) {
		return r, badPlay("id isn't a playback ID")
	}
	r.ID = id
	if _, r.HasPosition = o["positionMs"]; r.HasPosition {
		if r.PositionMs, err = o.integer("positionMs", MaxPositionMs); err != nil {
			return r, err
		}
	}
	if call == CallProgress {
		if r.Paused, _, err = o.boolean("paused"); err != nil {
			return r, err
		}
	}
	r.At = now
	if v, ok := o["at"]; ok {
		n, ok := integerOf(v)
		if !ok || n < now.Add(-AtBefore).Unix() || n > now.Add(AtAfter).Unix() {
			return r, badPlay("at isn't an integer from 30 days before to 5 minutes after now")
		}
		r.At, r.AtSaid = time.Unix(n, 0), true
	}
	if r.Title, r.TitleSaid, err = o.text("title"); err != nil {
		return r, err
	}
	return r, nil
}

// The states of a playback on the owning server (playback section 6.1);
// any other is a playback of another pairing, or one never given.
const (
	StateStarted = "started"
	StateActive  = "active"
	StateEnded   = "ended"
)

// What a report does (ReportOutcome.Effect).
const (
	EffectRecorded         = "recorded"           // the report is kept
	EffectRecordedAndEnded = "recorded and ended" // kept, and the playback ends (stop)
	EffectEnded            = "ended"              // the playback ends, nothing kept
	EffectIgnored          = "ignored"            // older than a report kept already
	EffectUnchanged        = "unchanged"          // nothing changes
)

// PlaybackState is the playback a report names, as the owning server
// holds it: its state, purpose, title and music queue, when its play was
// answered, and the at of the latest report it keeps (zero: none).
type PlaybackState struct {
	State    string
	Purpose  string
	Title    string
	Queue    []string
	PlayedAt time.Time
	LastAt   time.Time
}

// ReportOutcome is what a report does: its effect, the state the
// playback is in after it, and, when kept, the title, position and time
// kept.
type ReportOutcome struct {
	Effect      string
	State       string
	Title       string
	PositionMs  int64
	HasPosition bool
	At          time.Time
}

// Live says whether the playback's ticket works and it counts against
// its limits: started or active.
func (s PlaybackState) Live() bool { return s.State == StateStarted || s.State == StateActive }

// Apply is what report r (of call, read by ReadReport) does to s at now
// (playback sections 3, 4, 6.1 and 7). A playback of another pairing, or
// unknown: 410 playback-stopped to progress, unchanged to stop. A title
// neither the playback's nor of its queue: 400. An ended playback: 410 to
// progress, unchanged to stop, except a download's late report with at,
// while now is at most its play plus LateReports: kept, the playback
// still ended. A live scan's progress changes nothing. A report older
// than the one kept is ignored; a stop ends the playback anyway.
func (s PlaybackState) Apply(call string, r Report, now time.Time) (ReportOutcome, error) {
	stopped := func(why string) error { return &PlayRefusal{410, ProblemStopped, why} }
	unchanged := ReportOutcome{Effect: EffectUnchanged, State: s.State}
	if !s.Live() && s.State != StateEnded {
		if call == CallProgress {
			return ReportOutcome{}, stopped("not a playback of this pairing")
		}
		return unchanged, nil
	}
	title := r.Title
	if !r.TitleSaid {
		title = s.Title
	} else if title != s.Title && !slices.Contains(s.Queue, title) {
		return ReportOutcome{}, badPlay("title is neither the playback's nor of its queue")
	}
	kept := ReportOutcome{Title: title, PositionMs: r.PositionMs, HasPosition: r.HasPosition, At: r.At}
	older := !s.LastAt.IsZero() && r.At.Before(s.LastAt)
	if !s.Live() {
		late := s.Purpose == PurposeDownload && r.AtSaid && !now.After(s.PlayedAt.Add(LateReports))
		switch {
		case !late && call == CallProgress:
			return ReportOutcome{}, stopped("the playback has ended")
		case !late:
			return unchanged, nil
		case older:
			return ReportOutcome{Effect: EffectIgnored, State: StateEnded}, nil
		}
		kept.Effect, kept.State = EffectRecorded, StateEnded
		return kept, nil
	}
	if call == CallStop {
		if older || s.Purpose == PurposeScan {
			return ReportOutcome{Effect: EffectEnded, State: StateEnded}, nil
		}
		kept.Effect, kept.State = EffectRecordedAndEnded, StateEnded
		return kept, nil
	}
	if s.Purpose == PurposeScan {
		return unchanged, nil
	}
	if older {
		return ReportOutcome{Effect: EffectIgnored, State: StateActive}, nil
	}
	kept.Effect, kept.State = EffectRecorded, StateActive
	return kept, nil
}
