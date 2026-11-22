package wire

import (
	"errors"
	"slices"
)

// play's answer (MSFP playback section 2.3), as a viewer's server checks
// and cleans it, and the restart of a file being read (section 2.7).

// HLSType is the only mimeType an hls stream may name.
const HLSType = "application/vnd.apple.mpegurl"

// PlayAnswer is what a viewer's server keeps of play's answer: the
// playback's own members, checked and cleaned (ReadPlayAnswer). The
// content parts' members (video's subtitles and previews, music's queue)
// are theirs to read; unknown members (versions, info, title, next)
// aren't kept. AudioRenditions: the playlist carries every audio track
// as a rendition (media section 3.1); false, the owning server made none,
// and switching tracks means a new playback.
type PlayAnswer struct {
	ID              string      `json:"id"`
	Method          string      `json:"method"`
	Stream          PlayStream  `json:"stream"`
	Etag            string      `json:"etag"`
	PositionMs      int64       `json:"positionMs"`
	DurationMs      int64       `json:"durationMs,omitempty"`
	VersionID       string      `json:"versionId,omitempty"`
	AudioTracks     []PlayTrack `json:"audioTracks,omitempty"`
	AudioTrack      string      `json:"audioTrack,omitempty"`
	AudioRenditions bool        `json:"audioRenditions,omitempty"`
	MaxBitrateKbps  int         `json:"maxBitrateKbps,omitempty"`
	// ReadTitleAgain: the etag isn't the catalog's. The answer wins for
	// this playback; the title is read again from titles.
	ReadTitleAgain bool `json:"readTitleAgain,omitempty"`
}

// PlayStream is an answer's stream: its URL, under the owning server's
// stream root, its protocol (hls or file), its mimeType, and Base, the
// stream's base (StreamBase) under which every other URL of the answer
// must be.
type PlayStream struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	MimeType string `json:"mimeType,omitempty"`
	Base     string `json:"-"`
}

// PlayTrack is a track of an answer, of playback section 2.3's members
// (video's, its codec and the rest, are video's).
type PlayTrack struct {
	ID              string `json:"id"`
	Language        string `json:"language,omitempty"`
	Channels        int    `json:"channels,omitempty"`
	Role            string `json:"role,omitempty"`
	Default         bool   `json:"default,omitempty"`
	Forced          bool   `json:"forced,omitempty"`
	HearingImpaired bool   `json:"hearingImpaired,omitempty"`
}

// ErrPlayAnswer is an answer that breaks a "must" of playback section
// 2.3: the viewer's server sends stop for its id, when it has one, and
// gives up.
var ErrPlayAnswer = errors.New("msfp: play's answer refused")

// methodOf is the method each delivery must be answered with.
var methodOf = map[string]string{DeliveryFile: MethodDirect, DeliveryRemux: MethodRemux, DeliveryTranscode: MethodTranscode}

// ReadPlayAnswer checks and cleans play's answer: delivery is what the
// request asked ("" for none), root the owning server's stream root,
// catalogEtag the etag the catalog gave the copy ("" when unknown).
// Refused (ErrPlayAnswer): a body that isn't an I-JSON object, null at
// its top, a member of the wrong type; id not a playback ID; method not
// direct, remux or transcode, or not the delivery's (direct for file);
// stream not under root as its protocol says (StreamBase), an hls
// mimeType other than HLSType, a file stream for a remux, a transcode or
// the baseline (no delivery), an hls one for delivery file; etag not 1
// to 64 characters of A–Z a–z 0–9 - _; positionMs absent or out of 0 to
// MaxPositionMs; audioRenditions not a boolean. Cleaned: durationMs out
// of range set to 0; tracks with another ID dropped; other IDs cleared;
// track members of the wrong type or value dropped; audioRenditions true
// with the file dropped; maxBitrateKbps out of range dropped.
func ReadPlayAnswer(body []byte, delivery, root, catalogEtag string) (PlayAnswer, error) {
	var a PlayAnswer
	tree, err := strictTree(body, true)
	if err != nil {
		return a, ErrPlayAnswer
	}
	o, ok := tree.(map[string]any)
	if !ok {
		return a, ErrPlayAnswer
	}
	for _, v := range o {
		if v == nil {
			return a, ErrPlayAnswer
		}
	}
	str := func(name string, required bool) (string, bool) {
		v, there := o[name]
		s, isString := v.(string)
		return s, there && isString || !there && !required
	}
	var okID, okMethod, okEtag bool
	a.ID, okID = str("id", true)
	a.Method, okMethod = str("method", true)
	a.Etag, okEtag = str("etag", true)
	if !okID || !PlayID(a.ID) || !okMethod || !slices.Contains([]string{MethodDirect, MethodRemux, MethodTranscode}, a.Method) ||
		delivery != "" && a.Method != methodOf[delivery] || !okEtag || !ID(a.Etag) {
		return a, ErrPlayAnswer
	}
	stream, ok := o["stream"].(map[string]any)
	if !ok {
		return a, ErrPlayAnswer
	}
	a.Stream.URL, _ = stream["url"].(string)
	a.Stream.Protocol, _ = stream["protocol"].(string)
	if mt, there := stream["mimeType"]; there && mt != nil {
		if a.Stream.MimeType, ok = mt.(string); !ok {
			return a, ErrPlayAnswer
		}
	}
	file := a.Stream.Protocol == "file"
	if a.Stream.Base, ok = StreamBase(a.Stream.URL, root, a.Stream.Protocol); !ok ||
		!file && a.Stream.MimeType != "" && a.Stream.MimeType != HLSType ||
		file && a.Method != MethodDirect || delivery == DeliveryFile && !file || delivery == "" && file {
		return a, ErrPlayAnswer
	}
	pos, ok := integerOf(o["positionMs"])
	if !ok || pos < 0 || pos > MaxPositionMs {
		return a, ErrPlayAnswer
	}
	a.PositionMs = pos
	if v, there := o["durationMs"]; there {
		n, ok := integerOf(v)
		if !ok {
			return a, ErrPlayAnswer
		}
		if n >= 0 && n <= MaxPositionMs {
			a.DurationMs = n
		}
	}
	if v, there := o["audioRenditions"]; there {
		b, ok := v.(bool)
		if !ok {
			return a, ErrPlayAnswer
		}
		a.AudioRenditions = b && !file
	}
	if v, there := o["audioTracks"]; there {
		list, ok := v.([]any)
		if !ok {
			return a, ErrPlayAnswer
		}
		for _, e := range list {
			if t, ok := readPlayTrack(e); ok {
				a.AudioTracks = append(a.AudioTracks, t)
			}
		}
	}
	for _, id := range []struct {
		name string
		dst  *string
	}{{"versionId", &a.VersionID}, {"audioTrack", &a.AudioTrack}} {
		s, ok := str(id.name, false)
		if !ok {
			return a, ErrPlayAnswer
		}
		if ID(s) {
			*id.dst = s
		}
	}
	if v, there := o["maxBitrateKbps"]; there {
		n, ok := integerOf(v)
		if !ok {
			return a, ErrPlayAnswer
		}
		if n >= 0 && n <= MaxKbps {
			a.MaxBitrateKbps = int(n)
		}
	}
	a.ReadTitleAgain = catalogEtag != "" && a.Etag != catalogEtag
	return a, nil
}

// readPlayTrack reads a track of an answer: false, dropped, without an
// ID of its form; each other member of the wrong type or value dropped
// alone.
func readPlayTrack(e any) (PlayTrack, bool) {
	o, _ := e.(map[string]any)
	id, _ := o["id"].(string)
	if !ID(id) {
		return PlayTrack{}, false
	}
	t := PlayTrack{ID: id}
	if l, _ := o["language"].(string); LanguageTag(l) {
		t.Language = l
	}
	if n, ok := integerOf(o["channels"]); ok && n >= 1 && n <= 64 {
		t.Channels = int(n)
	}
	t.Role = tokenOf(o["role"])
	t.Default, _ = o["default"].(bool)
	t.Forced, _ = o["forced"].(bool)
	t.HearingImpaired, _ = o["hearingImpaired"].(bool)
	return t, true
}

// Reading is a playback's file a viewer's server is reading: its copy
// (versionId), etag, purpose, delivery and the byte it was at.
type Reading struct {
	VersionID, Etag   string
	Purpose, Delivery string
	Offset            int64
}

// RestartOutcome is what a viewer's server does with a new play of a
// file it was reading (playback section 2.7): Join, going on at Offset;
// else StopNew (a new playback was started: stop it), FailRead and
// ReadTitles (read the title again from titles).
type RestartOutcome struct {
	Join       bool  `json:"join"`
	Offset     int64 `json:"offset,omitempty"`
	StopNew    bool  `json:"stopNew,omitempty"`
	FailRead   bool  `json:"failRead,omitempty"`
	ReadTitles bool  `json:"readTitles,omitempty"`
}

// Restart says whether the bytes of a new playback may follow those of
// r (playback section 2.7): only for a reading of delivery file, a new
// play of the same copy (its versionId), purpose and delivery, answered
// 200 (status) with an answer ReadPlayAnswer keeps, of the same etag,
// compared byte for byte. root is the owning server's stream root.
func Restart(r Reading, req PlayRequest, status int, answer []byte, root string) RestartOutcome {
	fail := RestartOutcome{FailRead: true, ReadTitles: true}
	purpose := func(p string) string {
		if p == "" {
			return PurposePlay
		}
		return p
	}
	if r.Delivery != DeliveryFile || req.Delivery != DeliveryFile || req.VersionID != r.VersionID ||
		purpose(req.Purpose) != purpose(r.Purpose) || status != 200 {
		return fail
	}
	a, err := ReadPlayAnswer(answer, DeliveryFile, root, "")
	if err != nil || a.Etag != r.Etag {
		fail.StopNew = true
		return fail
	}
	return RestartOutcome{Join: true, Offset: r.Offset}
}
