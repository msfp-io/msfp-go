package wire

import (
	"errors"
	"slices"
)

// play's answer (MSFP playback section 2.3, video section 5.3), as a
// viewer's server checks and cleans it, the URLs it builds from the
// answer's ticket (media section 1, video section 6.1), and the restart
// of a file being read (playback section 2.7).

// HLSType is the media type of an HLS playlist.
const HLSType = "application/vnd.apple.mpegurl"

// The forms a subtitle track is served apart in (video section 5.3):
// text (WebVTT, under subtitles/), picture (PGS, under pictures/), and
// FormASS, the ASS a text track's ass: true also opens (under ass/).
const (
	FormText    = "text"
	FormPicture = "picture"
	FormASS     = "ass"
)

// MaxServedSubtitles is how many entries an answer's subtitles holds at
// most (video section 5.3).
const MaxServedSubtitles = 64

// ServedSubtitle is an entry of an answer's subtitles (video section
// 5.3): a track of the copy the owning server serves apart, its form,
// and for a text track whether its ASS is served too.
type ServedSubtitle struct {
	ID   string `json:"id"`
	Form string `json:"form"`
	ASS  bool   `json:"ass,omitempty"`
}

// ServedForms are video section 5.4's table: the form a track of a
// listed format is served in, and whether its ASS is too. A track in
// another format waits for the answer to know its form.
var ServedForms = map[string]ServedSubtitle{
	"subrip": {Form: FormText}, "webvtt": {Form: FormText}, "mov_text": {Form: FormText}, "ttml": {Form: FormText},
	"ass": {Form: FormText, ASS: true}, "ssa": {Form: FormText, ASS: true},
	"hdmv_pgs_subtitle": {Form: FormPicture}, "dvd_subtitle": {Form: FormPicture}, "dvb_subtitle": {Form: FormPicture},
}

// SubtitleServing is how a subtitle track of a playback is served apart
// (video section 5.3): as text (WebVTT), its ASS too, or as pictures
// (PGS); none of them when it isn't served apart.
type SubtitleServing struct{ Text, ASS, Pictures bool }

// ServeSubtitle is how an owning server serves a subtitle track of
// format apart: the form ServedForms gives a listed format, else form
// (FormText, FormPicture, or "" for none), the host's; none when
// notApart, the host's own word that it can't.
func ServeSubtitle(format, form string, notApart bool) SubtitleServing {
	if notApart {
		return SubtitleServing{}
	}
	if s, listed := ServedForms[format]; listed {
		return SubtitleServing{Text: s.Form == FormText, ASS: s.ASS, Pictures: s.Form == FormPicture}
	}
	return SubtitleServing{Text: form == FormText, Pictures: form == FormPicture}
}

// Apart says whether the track is served apart at all.
func (s SubtitleServing) Apart() bool { return s.Text || s.Pictures }

// Entry is the play answer's entry of track id served as s (Apart).
func (s SubtitleServing) Entry(id string) ServedSubtitle {
	if s.Pictures {
		return ServedSubtitle{ID: id, Form: FormPicture}
	}
	return ServedSubtitle{ID: id, Form: FormText, ASS: s.ASS}
}

// Serving is how the answer's entry says its track is served.
func (s ServedSubtitle) Serving() SubtitleServing {
	return SubtitleServing{Text: s.Form == FormText, ASS: s.ASS, Pictures: s.Form == FormPicture}
}

// PlayAnswer is what a viewer's server keeps of play's answer (playback
// section 2.3, video section 5.3), checked and cleaned (ReadPlayAnswer).
// It carries no URL: URLs builds them from the owning server's stream
// root and Ticket. AudioRenditions: the playlist carries every audio
// track as a rendition (media section 3.1); false, the owning server
// made none, and switching tracks means a new playback. ScanBytes is a
// scan's cap, the bytes its ticket may read in all. Subtitles and
// FontCount are video's; music's queue is music's (ReadQueue).
type PlayAnswer struct {
	ID              string           `json:"id"`
	Ticket          string           `json:"ticket"`
	Protocol        string           `json:"protocol"`
	Method          string           `json:"method"`
	Etag            string           `json:"etag"`
	PositionMs      int64            `json:"positionMs"`
	VersionID       string           `json:"versionId"`
	AudioTrack      string           `json:"audioTrack,omitempty"`
	AudioRenditions bool             `json:"audioRenditions,omitempty"`
	MaxBitrateKbps  *int             `json:"maxBitrateKbps,omitempty"`
	ScanBytes       int64            `json:"scanBytes,omitempty"`
	Subtitles       []ServedSubtitle `json:"subtitles,omitempty"`
	FontCount       int              `json:"fontCount,omitempty"`
	// ReadTitleAgain: the etag isn't the catalog's. The answer wins for
	// this playback; the title is read again from titles.
	ReadTitleAgain bool `json:"readTitleAgain,omitempty"`
}

// Asked is what a viewer's server asked and knows when it reads play's
// answer: the request's purpose and delivery ("" for none), the title's
// content part (PartVideo, PartMusic), and the copy as the catalog gives
// it: its etag ("" when unknown) and its subtitle tracks (their IDs, and
// their formats for video section 5.4's table).
type Asked struct {
	Purpose     string
	Delivery    string
	Part        string
	CatalogEtag string
	Subtitles   []CopySubtitle
}

// ErrPlayAnswer is an answer that breaks a "must" of playback section
// 2.3: the viewer's server sends stop for its id, when it has one, and
// gives up.
var ErrPlayAnswer = errors.New("msfp: play's answer refused")

// methodOf is the method each delivery must be answered with.
var methodOf = map[string]string{DeliveryFile: MethodDirect, DeliveryRemux: MethodRemux, DeliveryTranscode: MethodTranscode}

// ReadPlayAnswer checks and cleans play's answer for what was asked.
// Refused (ErrPlayAnswer): a body that isn't an I-JSON object, null at
// its top; id or ticket not of an ID's form; protocol not hls or file,
// or not the delivery's (file for file, hls otherwise); method not
// direct, remux or transcode, or not the delivery's; etag not of an
// ID's form; positionMs absent, not an integer or out of 0 to
// MaxPositionMs; versionId missing or not of an ID's form; audioTrack,
// audioRenditions or maxBitrateKbps of the wrong type. Cleaned: an
// audioTrack not of its form dropped; audioRenditions true kept with hls
// only; maxBitrateKbps out of range dropped; for a scan, scanBytes
// missing or out of MinScanBytes to MaxScanBytes counted as
// MinScanBytes (other purposes ignore it); for video, each subtitles
// entry kept only when its id is a subtitle track of the copy in the
// catalog, not a repeat, its form text or picture (ass kept with text
// only), and, for a format of ServedForms, the form and ass the table
// gives, at most MaxServedSubtitles; fontCount 0 to MaxFonts, otherwise
// 0. Members of earlier drafts (stream, audioTracks, durationMs,
// trickplay, fonts…) are ignored.
func ReadPlayAnswer(body []byte, ask Asked) (PlayAnswer, error) {
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
	str := func(name string) (string, bool) { s, ok := o[name].(string); return s, ok }
	a.ID, _ = str("id")
	a.Ticket, _ = str("ticket")
	a.Protocol, _ = str("protocol")
	a.Method, _ = str("method")
	a.Etag, _ = str("etag")
	a.VersionID, _ = str("versionId")
	wantProtocol := ProtocolHLS
	if ask.Delivery == DeliveryFile {
		wantProtocol = ProtocolFile
	}
	if !PlayID(a.ID) || !ID(a.Ticket) || a.Protocol != ProtocolHLS && a.Protocol != ProtocolFile ||
		KnownDelivery(ask.Delivery) && a.Protocol != wantProtocol || ask.Delivery == "" && a.Protocol != ProtocolHLS ||
		!slices.Contains([]string{MethodDirect, MethodRemux, MethodTranscode}, a.Method) ||
		methodOf[ask.Delivery] != "" && a.Method != methodOf[ask.Delivery] || !ID(a.Etag) || !ID(a.VersionID) {
		return a, ErrPlayAnswer
	}
	pos, ok := integerOf(o["positionMs"])
	if !ok || pos < 0 || pos > MaxPositionMs {
		return a, ErrPlayAnswer
	}
	a.PositionMs = pos
	if v, there := o["audioTrack"]; there {
		s, ok := v.(string)
		if !ok {
			return a, ErrPlayAnswer
		}
		if _, ok := TrackID(s, 'a'); ok {
			a.AudioTrack = s
		}
	}
	if v, there := o["audioRenditions"]; there {
		b, ok := v.(bool)
		if !ok {
			return a, ErrPlayAnswer
		}
		a.AudioRenditions = b && a.Protocol == ProtocolHLS
	}
	if v, there := o["maxBitrateKbps"]; there {
		n, ok := integerOf(v)
		if !ok {
			return a, ErrPlayAnswer
		}
		if n >= 0 && n <= MaxKbps {
			k := int(n)
			a.MaxBitrateKbps = &k
		}
	}
	if ask.Purpose == PurposeScan {
		a.ScanBytes = MinScanBytes
		if n, ok := integerOf(o["scanBytes"]); ok && n >= MinScanBytes && n <= MaxScanBytes {
			a.ScanBytes = n
		}
	}
	if ask.Part == PartVideo {
		a.Subtitles = readServedSubtitles(o["subtitles"], ask.Subtitles)
		if n, ok := integerOf(o["fontCount"]); ok && n >= 0 && n <= MaxFonts {
			a.FontCount = int(n)
		}
	}
	a.ReadTitleAgain = ask.CatalogEtag != "" && a.Etag != ask.CatalogEtag
	return a, nil
}

// readServedSubtitles keeps an answer's subtitles entries as video
// section 5.3 says.
func readServedSubtitles(v any, tracks []CopySubtitle) []ServedSubtitle {
	list, _ := v.([]any)
	var out []ServedSubtitle
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok || len(out) == MaxServedSubtitles {
			continue
		}
		id, _ := m["id"].(string)
		form, _ := m["form"].(string)
		i := slices.IndexFunc(tracks, func(t CopySubtitle) bool { return t.ID == id })
		if i < 0 || form != FormText && form != FormPicture || slices.ContainsFunc(out, func(s ServedSubtitle) bool { return s.ID == id }) {
			continue
		}
		s := ServedSubtitle{ID: id, Form: form, ASS: m["ass"] == true && form == FormText}
		if want, listed := ServedForms[tracks[i].Format]; listed && (want.Form != s.Form || want.ASS != s.ASS) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Base is <root><ticket>/, the base of the answer's URLs.
func (a PlayAnswer) Base(root string) string { return TicketBase(root, a.Ticket) }

// StreamURL is the URL of the answer's stream: <root><ticket>/master.m3u8
// for hls, <root><ticket>/file for file.
func (a PlayAnswer) StreamURL(root string) string { return StreamURL(root, a.Ticket, a.Protocol) }

// URLs are every URL the viewer's server may build from the answer's
// ticket, in order: the stream; each subtitle track's base (subtitles/,
// then ass/ when it is served, or pictures/), to which /<part> or /whole
// is added; each font, fonts/0 to fontCount − 1; and sheets sheets,
// trickplay/0 to sheets − 1 (the sheet count of the copy's
// previewLayout, 0 for none).
func (a PlayAnswer) URLs(root string, sheets int) []string {
	base := a.Base(root)
	out := []string{a.StreamURL(root)}
	for _, s := range a.Subtitles {
		if s.Form == FormPicture {
			out = append(out, SubtitleURL(base, s.ID, FormPicture))
			continue
		}
		out = append(out, SubtitleURL(base, s.ID, FormText))
		if s.ASS {
			out = append(out, SubtitleURL(base, s.ID, FormASS))
		}
	}
	for n := 0; n < a.FontCount; n++ {
		out = append(out, FontURL(base, n))
	}
	for n := 0; n < sheets; n++ {
		out = append(out, SheetURL(base, n))
	}
	return out
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
// compared byte for byte.
func Restart(r Reading, req PlayRequest, status int, answer []byte) RestartOutcome {
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
	a, err := ReadPlayAnswer(answer, Asked{Purpose: purpose(r.Purpose), Delivery: DeliveryFile})
	if err != nil || a.Etag != r.Etag {
		fail.StopNew = true
		return fail
	}
	return RestartOutcome{Join: true, Offset: r.Offset}
}
