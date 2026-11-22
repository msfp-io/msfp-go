package wire

import (
	"encoding/json"
	"slices"
	"strconv"
)

// play's request (MSFP playback sections 2.1, 2.2 and 2.7, video section
// 5.1): what a viewer's server asks to be served, and how an owning
// server reads it, refusing what step 1 of section 2.2 refuses.

// The purposes of a playback (playback section 1).
const (
	PurposePlay     = "play"
	PurposeDownload = "download"
	PurposeScan     = "scan"
)

// The deliveries a play asks for (playback section 2.7); none means the
// baseline.
const (
	DeliveryFile      = "file"
	DeliveryRemux     = "remux"
	DeliveryTranscode = "transcode"
)

// The methods of a play answer (playback section 2.3).
const (
	MethodDirect    = "direct"
	MethodRemux     = "remux"
	MethodTranscode = "transcode"
)

// The protocols of a play answer (playback section 2.3).
const (
	ProtocolHLS  = "hls"
	ProtocolFile = "file"
)

// The problem types of playback's refusals (playback sections 2.5, 3, 4).
const (
	ProblemInvalid         = "invalid-request"
	ProblemNotFound        = "not-found"
	ProblemStreamLimit     = "stream-limit-reached"
	ProblemDeliveryRefused = "delivery-refused"
	ProblemNotPlayable     = "not-playable"
	ProblemServerBusy      = "server-busy"
	ProblemStopped         = "playback-stopped"
)

// Bounds of play's numbers (playback section 2.1, video section 5.1).
const (
	MaxPositionMs = 8_640_000_000 // 100 days
	MaxKbps       = 10_000_000
)

// ChannelSteps are the values of a transcode's audioChannels (playback
// section 2.7).
var ChannelSteps = []int{2, 6, 8}

// The segment formats of HLS (playback section 2.7): a remux names one
// as its media type's subtype, video/<sub> or, for a copy with no video,
// audio/<sub>. Another well-formed media type is refused with 409
// delivery-refused.
const (
	SegmentsTS  = "mp2t" // MPEG-TS segments
	SegmentsMP4 = "mp4"  // fragmented MP4 segments
)

// The baseline every viewer's server plays (playback section 2.7): what
// the owning server serves without delivery, and the values of the
// transcode targets left out. A copy with no video's baseline is
// BaselineAudio alone.
const (
	BaselineVideo    = "h264"
	BaselineAudio    = "aac"
	BaselineChannels = 2
)

// CodecCopy is the value of a transcode's videoCodec or audioCodec that
// serves that stream unchanged (playback section 2.7): a closed
// mechanic, not a registry token.
const CodecCopy = "copy"

// The range of a transcode's picture (playback section 2.7): sdr
// (tone-mapped to SDR, 8-bit, BT.709), the default, or keep (the copy's
// transfer and primaries kept, 10-bit; HDR10+ kept where the codec
// carries it).
const (
	RangeSDR  = "sdr"
	RangeKeep = "keep"
)

// PlayRefusal is an owning server's refusal of a play, a progress or a
// stop: the status and the problem type of its signed body.
type PlayRefusal struct {
	Status  int
	Problem string
	Why     string
}

func (r *PlayRefusal) Error() string { return "msfp: " + r.Problem + ": " + r.Why }

func badPlay(why string) *PlayRefusal { return &PlayRefusal{400, ProblemInvalid, why} }

func refusedDelivery(why string) *PlayRefusal { return &PlayRefusal{409, ProblemDeliveryRefused, why} }

// TranscodeTargets are a play's transcode object (playback section 2.7,
// video section 5.1). The codecs are registry tokens, or CodecCopy for a
// stream served unchanged; Range is RangeSDR or RangeKeep. Each target
// left out takes the baseline's value: Serve gives the targets the
// owning server makes.
type TranscodeTargets struct {
	VideoCodec     string `json:"videoCodec,omitempty"`
	MaxHeight      int    `json:"maxHeight,omitempty"`
	MaxBitrateKbps int    `json:"maxBitrateKbps,omitempty"`
	AudioCodec     string `json:"audioCodec,omitempty"`
	AudioChannels  int    `json:"audioChannels,omitempty"`
	Range          string `json:"range,omitempty"`
	BurnSubtitle   string `json:"burnSubtitle,omitempty"`
}

// Sized says whether t names a download's size (playback section 7):
// maxHeight, maxBitrateKbps or both.
func (t TranscodeTargets) Sized() bool { return t.MaxHeight != 0 || t.MaxBitrateKbps != 0 }

// PlayRequest is play's request, as ReadPlay reads it and as a viewer's
// server sends it (Marshal leaves out what is empty). Purpose is
// PurposePlay when the request says none. Delivery is a token and
// MediaType a media type, of their lists or not: one not of its list is
// refused at step 7 (Serve). The request says nothing of the viewer's
// device and names no language: the viewer's server chooses the copy
// (VersionID; without it, the first) and the audio (AudioTrack; without
// it, the file's default), and without Delivery the owning server serves
// the baseline. A subtitle is named only to burn it in
// (Transcode.BurnSubtitle).
type PlayRequest struct {
	Title           string            `json:"title"`
	Purpose         string            `json:"purpose,omitempty"`
	Delivery        string            `json:"delivery,omitempty"`
	MediaType       string            `json:"mediaType,omitempty"`
	Transcode       *TranscodeTargets `json:"transcode,omitempty"`
	AudioRenditions bool              `json:"audioRenditions,omitempty"`
	StartPositionMs int64             `json:"startPositionMs,omitempty"`
	MaxBitrateKbps  int               `json:"maxBitrateKbps,omitempty"`
	AudioTrack      string            `json:"audioTrack,omitempty"`
	VersionID       string            `json:"versionId,omitempty"`
	// Queue is music's (music section 5.2), as a viewer's server sends
	// it; ReadPlay leaves it to CheckQueue.
	Queue []QueueAsk `json:"queue,omitempty"`

	// emptyTrack: the request had an audioTrack or a burnSubtitle that
	// was "" (or a burnSubtitle that wasn't a string), which reads as
	// absent but is malformed.
	emptyTrack bool
}

// TrackIDsOK says whether the request's track IDs have their forms, as
// step 4 of playback section 2.2 checks them once the copy is chosen:
// audioTrack a<n>, transcode.burnSubtitle s<n>; n from 0 to 999 without
// leading zeros.
func (r PlayRequest) TrackIDsOK() bool {
	if r.emptyTrack {
		return false
	}
	if _, ok := TrackID(r.AudioTrack, 'a'); r.AudioTrack != "" && !ok {
		return false
	}
	if r.Transcode != nil && r.Transcode.BurnSubtitle != "" {
		if _, ok := TrackID(r.Transcode.BurnSubtitle, 's'); !ok {
			return false
		}
	}
	return true
}

// KnownDelivery says whether d is a delivery of playback section 2.7's
// list: file, remux or transcode.
func KnownDelivery(d string) bool {
	return d == DeliveryFile || d == DeliveryRemux || d == DeliveryTranscode
}

// PurposeAllows is playback section 2.7's table of purpose and delivery:
// a play takes any delivery or none; a download file, or a transcode with
// a size (TranscodeTargets.Sized); a scan file only. A delivery that is a
// token not of the list is allowed here, whatever the purpose: it is
// refused at step 7, 409 delivery-refused.
func PurposeAllows(purpose, delivery string, t *TranscodeTargets) bool {
	if delivery != "" && !KnownDelivery(delivery) {
		return true
	}
	switch purpose {
	case PurposePlay, "":
		return true
	case PurposeDownload:
		return delivery == DeliveryFile || delivery == DeliveryTranscode && t != nil && t.Sized()
	case PurposeScan:
		return delivery == DeliveryFile
	}
	return false
}

// BitrateCap is the cap of playback section 2.2, step 6, for a play: the
// lowest non-zero of the request's maxBitrateKbps and its transcode's,
// the owning server's own cap and the pairing's bitrateKbps; 0, no cap,
// without any. A download's and a scan's aren't capped so (sections 7
// and 8).
func (r PlayRequest) BitrateCap(own, pairing int) int {
	caps := []int{r.MaxBitrateKbps, own, pairing}
	if r.Transcode != nil {
		caps = append(caps, r.Transcode.MaxBitrateKbps)
	}
	out := 0
	for _, c := range caps {
		if c > 0 && (out == 0 || c < out) {
			out = c
		}
	}
	return out
}

// playMembers are the members of play's request this part and video
// know: null in any of them, anywhere, refuses the request (core section
// 2.3).
var playMembers = []string{"title", "purpose", "delivery", "mediaType", "transcode", "audioRenditions",
	"startPositionMs", "maxBitrateKbps", "audioTrack", "versionId"}

// ReadPlay reads play's request as step 1 of playback section 2.2 checks
// it: an I-JSON object; title a key playable says play takes (nil:
// ParseKey's); purpose of its list; delivery a token and mediaType a
// media type (core section 8.5), mediaType with delivery remux only, and
// then required; a transcode object with delivery transcode only, its
// codecs and range tokens and its numbers of their values; numbers in
// range; purpose and delivery together as PurposeAllows says. A refusal
// is a *PlayRefusal (400). Unknown members are ignored, those of earlier
// drafts among them (capabilities, container, audioLanguage,
// subtitleLanguage, subtitleTrack). Track IDs are checked later, at step
// 4 (TrackIDsOK); a delivery, a media type, a codec or a range the
// owning server doesn't make, at step 7 (Serve). Music's queue is left
// to CheckQueue.
func ReadPlay(body []byte, playable func(title string) bool) (PlayRequest, error) {
	o, err := requestObject(body, playMembers)
	if err != nil {
		return PlayRequest{}, err
	}
	var r PlayRequest
	title, ok, err := o.text("title")
	if err != nil {
		return r, err
	}
	if playable == nil {
		playable = func(t string) bool { k, ok := ParseKey(t); return ok && k.Playable }
	}
	if !ok || !playable(title) {
		return r, badPlay("title isn't the key of a title play takes")
	}
	r.Title = title
	var have bool
	if r.Purpose, have, err = o.text("purpose"); err != nil {
		return r, err
	} else if !have {
		r.Purpose = PurposePlay
	} else if !slices.Contains([]string{PurposePlay, PurposeDownload, PurposeScan}, r.Purpose) {
		return r, badPlay("purpose isn't play, download or scan")
	}
	if err := o.readDelivery(&r); err != nil {
		return r, err
	}
	if r.AudioRenditions, _, err = o.boolean("audioRenditions"); err != nil {
		return r, err
	}
	if r.StartPositionMs, err = o.integer("startPositionMs", MaxPositionMs); err != nil {
		return r, err
	}
	kbps, err := o.integer("maxBitrateKbps", MaxKbps)
	if err != nil {
		return r, err
	}
	r.MaxBitrateKbps = int(kbps)
	if r.VersionID, _, err = o.text("versionId"); err != nil {
		return r, err
	}
	if r.AudioTrack, have, err = o.text("audioTrack"); err != nil {
		return r, err
	} else if have && r.AudioTrack == "" {
		r.emptyTrack = true
	}
	if !PurposeAllows(r.Purpose, r.Delivery, r.Transcode) {
		return r, badPlay("purpose " + r.Purpose + " doesn't take that delivery")
	}
	return r, nil
}

// readDelivery reads delivery, mediaType and transcode into r: a
// request's, or a queue entry's own (music section 5.2).
func (o members) readDelivery(r *PlayRequest) error {
	var have bool
	var err error
	if r.Delivery, have, err = o.text("delivery"); err != nil {
		return err
	} else if have && !Token(r.Delivery) {
		return badPlay("delivery isn't a token")
	}
	if r.MediaType, have, err = o.text("mediaType"); err != nil {
		return err
	}
	switch {
	case have && r.Delivery != DeliveryRemux:
		return badPlay("mediaType without delivery remux")
	case have && !IsMediaType(r.MediaType):
		return badPlay("mediaType isn't a media type")
	case !have && r.Delivery == DeliveryRemux:
		return badPlay("delivery remux without mediaType")
	}
	if v, ok := o["transcode"]; ok {
		if r.Delivery != DeliveryTranscode {
			return badPlay("a transcode object without delivery transcode")
		}
		t, empty, err := readTargets(v)
		if err != nil {
			return err
		}
		r.Transcode, r.emptyTrack = &t, r.emptyTrack || empty
	}
	return nil
}

// readTargets reads a transcode object (playback section 2.7, video
// section 5.1): the codecs and the range tokens, the numbers of their
// values, else refused (400); a codec or a range the owning server
// doesn't make is refused at step 7 (Serve). empty says burnSubtitle
// was "" or not a string (malformed, refused at step 4 with the track
// IDs).
func readTargets(v any) (t TranscodeTargets, empty bool, err error) {
	m, ok := v.(map[string]any)
	if !ok {
		return t, false, badPlay("transcode isn't an object")
	}
	o := members(m)
	for _, s := range []struct {
		name string
		dst  *string
	}{{"videoCodec", &t.VideoCodec}, {"audioCodec", &t.AudioCodec}, {"range", &t.Range}} {
		val, have, err := o.text(s.name)
		if err != nil {
			return t, false, err
		}
		if have && !Token(val) {
			return t, false, badPlay("transcode." + s.name + " isn't a token")
		}
		*s.dst = val
	}
	for _, n := range []struct {
		name string
		ok   func(int) bool
		dst  *int
	}{
		{"maxHeight", func(n int) bool { return slices.Contains(Heights, n) }, &t.MaxHeight},
		{"maxBitrateKbps", func(n int) bool { return n >= 1000 && n <= MaxKbps && n%1000 == 0 }, &t.MaxBitrateKbps},
		{"audioChannels", func(n int) bool { return slices.Contains(ChannelSteps, n) }, &t.AudioChannels},
	} {
		raw, have := o[n.name]
		if !have {
			continue
		}
		v, ok := integerOf(raw)
		if !ok || v < 0 || v > MaxKbps || !n.ok(int(v)) {
			return t, false, badPlay("transcode." + n.name + " isn't one of its values")
		}
		*n.dst = int(v)
	}
	// A burnSubtitle of another form, a string or not, is refused at
	// step 4, with the track IDs.
	raw, have := o["burnSubtitle"]
	val, isString := raw.(string)
	t.BurnSubtitle = val
	return t, have && (!isString || val == ""), nil
}

// members is a JSON object as strictTree reads it, its numbers
// json.Number.
type members map[string]any

// requestObject reads body as a request's object: I-JSON, an object,
// with no null anywhere in the members known (core section 2.3).
func requestObject(body []byte, known []string) (members, error) {
	tree, err := strictTree(body, true)
	if err != nil {
		return nil, badPlay("not I-JSON")
	}
	o, ok := tree.(map[string]any)
	if !ok {
		return nil, badPlay("not a JSON object")
	}
	for _, name := range known {
		if v, ok := o[name]; ok && holdsNull(v) {
			return nil, badPlay(name + " holds null")
		}
	}
	return o, nil
}

// text reads a string member: whether it is there, refused (400) when it
// isn't a string.
func (o members) text(name string) (string, bool, error) {
	v, ok := o[name]
	if !ok {
		return "", false, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", true, badPlay(name + " isn't a string")
	}
	return s, true, nil
}

// boolean reads a boolean member.
func (o members) boolean(name string) (bool, bool, error) {
	v, ok := o[name]
	if !ok {
		return false, false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, true, badPlay(name + " isn't a boolean")
	}
	return b, true, nil
}

// integer reads an integer member from 0 to top, 0 when absent.
func (o members) integer(name string, top int64) (int64, error) {
	v, ok := o[name]
	if !ok {
		return 0, nil
	}
	n, ok := integerOf(v)
	if !ok || n < 0 || n > top {
		return 0, badPlay(name + " isn't an integer from 0 to " + strconv.FormatInt(top, 10))
	}
	return n, nil
}

// integerOf reads an MSFP integer (core section 2.3): a JSON number
// without fraction or exponent, within ±(2^53 − 1).
func integerOf(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || !isInteger(string(n)) {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil
}
