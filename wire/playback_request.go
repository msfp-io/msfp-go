package wire

import (
	"encoding/json"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// play's request (MSFP playback sections 2.1, 2.2 and 2.7, video section
// 5.1): what a viewer's server sends of a device, coarsened, and how an
// owning server reads it, refusing what step 1 of section 2.2 refuses.

// The purposes of a playback (playback section 1).
const (
	PurposePlay     = "play"
	PurposeDownload = "download"
	PurposeScan     = "scan"
)

// The deliveries a play asks for (playback section 2.7); none means the
// owning server decides.
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
	MaxPositionMs  = 8_640_000_000 // 100 days
	MaxKbps        = 10_000_000
	DefaultCapKbps = 120_000 // the cap of section 2.2, step 6, without any other

	maxCapHeight     = 100_000
	maxCapChannels   = 64
	maxCapSampleRate = 768_000
	maxCapBitDepth   = 64
)

// The steps capabilities are coarsened to (playback section 2.1); the
// heights are video's (Heights).
var (
	ChannelSteps    = []int{2, 6, 8}
	SampleRateSteps = []int{44_100, 48_000, 96_000, 192_000}
	BitDepthSteps   = []int{16, 24, 32}
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

// Capabilities are what a viewer's device plays (playback section 2.1,
// video section 5.1). Each list holds only values of its names list,
// each once, sorted (DVProfiles in increasing order); 0 is not said.
type Capabilities struct {
	Containers         []string `json:"containers,omitempty"`
	VideoCodecs        []string `json:"videoCodecs,omitempty"`
	AudioCodecs        []string `json:"audioCodecs,omitempty"`
	AudioProfiles      []string `json:"audioProfiles,omitempty"`
	HDR                []string `json:"hdr,omitempty"`
	DVProfiles         []int    `json:"dvProfiles,omitempty"`
	MaxHeight          int      `json:"maxHeight,omitempty"`
	MaxAudioChannels   int      `json:"maxAudioChannels,omitempty"`
	MaxAudioSampleRate int      `json:"maxAudioSampleRate,omitempty"`
	MaxAudioBitDepth   int      `json:"maxAudioBitDepth,omitempty"`
	MaxBitrateKbps     int      `json:"maxBitrateKbps,omitempty"`
}

// Empty says whether c says nothing, as capabilities left out.
func (c Capabilities) Empty() bool {
	return len(c.Containers)+len(c.VideoCodecs)+len(c.AudioCodecs)+len(c.AudioProfiles)+len(c.HDR)+len(c.DVProfiles) == 0 &&
		c.MaxHeight == 0 && c.MaxAudioChannels == 0 && c.MaxAudioSampleRate == 0 && c.MaxAudioBitDepth == 0 && c.MaxBitrateKbps == 0
}

// capLists are the string lists of capabilities, with their names lists.
// subtitles has no values yet (video section 5.1): read for its type, kept
// empty.
var capLists = []struct {
	name string
	list []string
	of   func(*Capabilities) *[]string
}{
	{"containers", Containers, func(c *Capabilities) *[]string { return &c.Containers }},
	{"videoCodecs", VideoCodecs, func(c *Capabilities) *[]string { return &c.VideoCodecs }},
	{"audioCodecs", AudioCodecs, func(c *Capabilities) *[]string { return &c.AudioCodecs }},
	{"audioProfiles", AudioProfiles, func(c *Capabilities) *[]string { return &c.AudioProfiles }},
	{"hdr", HDRFormats, func(c *Capabilities) *[]string { return &c.HDR }},
	{"subtitles", nil, func(*Capabilities) *[]string { return new([]string) }},
}

// capNumbers are the integers of capabilities, with their tops and steps.
var capNumbers = []struct {
	name  string
	top   int
	steps []int
	of    func(*Capabilities) *int
}{
	{"maxHeight", maxCapHeight, Heights, func(c *Capabilities) *int { return &c.MaxHeight }},
	{"maxAudioChannels", maxCapChannels, ChannelSteps, func(c *Capabilities) *int { return &c.MaxAudioChannels }},
	{"maxAudioSampleRate", maxCapSampleRate, SampleRateSteps, func(c *Capabilities) *int { return &c.MaxAudioSampleRate }},
	{"maxAudioBitDepth", maxCapBitDepth, BitDepthSteps, func(c *Capabilities) *int { return &c.MaxAudioBitDepth }},
	{"maxBitrateKbps", MaxKbps, nil, func(c *Capabilities) *int { return &c.MaxBitrateKbps }},
}

// listed keeps of entries those list holds, each once, sorted.
func listed(entries, list []string) []string {
	var out []string
	for _, e := range entries {
		if slices.Contains(list, e) && !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	sort.Strings(out)
	return out
}

// dvListed keeps of profiles those DVProfiles holds, each once, in
// increasing order.
func dvListed(profiles []int) []int {
	var out []int
	for _, p := range profiles {
		if slices.Contains(DVProfiles, p) && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// StepDown is v stepped down to the greatest of steps not above it, the
// first below the first; 0 (or less) is not said, and stays 0, never
// stepped up.
func StepDown(v int, steps []int) int {
	if v <= 0 {
		return 0
	}
	out := steps[0]
	for _, s := range steps {
		if s <= v {
			out = s
		}
	}
	return out
}

// Coarsen is what a viewer's server sends of its device's capabilities
// (playback section 2.1, video section 5.1): the lists lowercased, only
// their listed values, each once, sorted; maxHeight, maxAudioChannels,
// maxAudioSampleRate and maxAudioBitDepth stepped down; maxBitrateKbps
// rounded down to a whole 1,000, below 1,000 left out; 0 left out.
func (c Capabilities) Coarsen() Capabilities {
	var out Capabilities
	for _, l := range capLists {
		var lower []string
		for _, e := range *l.of(&c) {
			lower = append(lower, strings.ToLower(e))
		}
		*l.of(&out) = listed(lower, l.list)
	}
	out.DVProfiles = dvListed(c.DVProfiles)
	for _, n := range capNumbers {
		if n.steps != nil {
			*n.of(&out) = StepDown(*n.of(&c), n.steps)
		}
	}
	if c.MaxBitrateKbps >= 1000 {
		out.MaxBitrateKbps = min(c.MaxBitrateKbps, MaxKbps) / 1000 * 1000
	}
	return out
}

// CoarsenBitrate is the request's maxBitrateKbps as a viewer's server
// sends it (playback section 2.1): rounded down to a whole 1,000 from
// 1,000 up, kept as it is below; never above MaxKbps, never below 0.
func CoarsenBitrate(kbps int) int {
	if kbps >= 1000 {
		return min(kbps, MaxKbps) / 1000 * 1000
	}
	return max(kbps, 0)
}

// TranscodeTargets are a play's transcode object (playback section 2.7,
// video section 5.1): each left out is the owning server's choice.
type TranscodeTargets struct {
	VideoCodec     string `json:"videoCodec,omitempty"`
	MaxHeight      int    `json:"maxHeight,omitempty"`
	MaxBitrateKbps int    `json:"maxBitrateKbps,omitempty"`
	AudioCodec     string `json:"audioCodec,omitempty"`
	AudioChannels  int    `json:"audioChannels,omitempty"`
	BurnSubtitle   string `json:"burnSubtitle,omitempty"`
}

// Sized says whether t names a download's size (playback section 7):
// maxHeight, maxBitrateKbps or both.
func (t TranscodeTargets) Sized() bool { return t.MaxHeight != 0 || t.MaxBitrateKbps != 0 }

// PlayRequest is play's request, as ReadPlay reads it and as a viewer's
// server sends it (Marshal leaves out what is empty). Purpose is
// PurposePlay when the request says none. Music's queue is music's.
type PlayRequest struct {
	Title           string            `json:"title"`
	Purpose         string            `json:"purpose,omitempty"`
	Delivery        string            `json:"delivery,omitempty"`
	Container       string            `json:"container,omitempty"`
	Transcode       *TranscodeTargets `json:"transcode,omitempty"`
	AudioRenditions bool              `json:"audioRenditions,omitempty"`
	Capabilities    *Capabilities     `json:"capabilities,omitempty"`
	StartPositionMs int64             `json:"startPositionMs,omitempty"`
	MaxBitrateKbps  int               `json:"maxBitrateKbps,omitempty"`
	AudioLanguage   string            `json:"audioLanguage,omitempty"`
	AudioTrack      string            `json:"audioTrack,omitempty"`
	VersionID       string            `json:"versionId,omitempty"`
	// Video's (video section 5.1).
	SubtitleLanguage string `json:"subtitleLanguage,omitempty"`
	SubtitleTrack    string `json:"subtitleTrack,omitempty"`

	// emptyTrack: the request had an audioTrack, a subtitleTrack or a
	// burnSubtitle that was "", which reads as absent but is malformed.
	emptyTrack bool
}

// TrackIDsOK says whether the request's track IDs have their forms, as
// step 4 of playback section 2.2 checks them once the copy is chosen:
// audioTrack a<n>, subtitleTrack s<n> or off, transcode.burnSubtitle
// s<n> (never off); n from 0 to 999 without leading zeros.
func (r PlayRequest) TrackIDsOK() bool {
	if r.emptyTrack {
		return false
	}
	if _, ok := TrackID(r.AudioTrack, 'a'); r.AudioTrack != "" && !ok {
		return false
	}
	if _, ok := TrackID(r.SubtitleTrack, 's'); r.SubtitleTrack != "" && r.SubtitleTrack != "off" && !ok {
		return false
	}
	if r.Transcode != nil && r.Transcode.BurnSubtitle != "" {
		if _, ok := TrackID(r.Transcode.BurnSubtitle, 's'); !ok {
			return false
		}
	}
	return true
}

// PurposeAllows is playback section 2.7's table of purpose and delivery:
// a play takes any delivery or none; a download file, or a transcode with
// a size (TranscodeTargets.Sized); a scan file only.
func PurposeAllows(purpose, delivery string, t *TranscodeTargets) bool {
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
// lowest non-zero of the request's maxBitrateKbps, its capabilities' and
// its transcode's, the owning server's own cap and the pairing's
// bitrateKbps; DefaultCapKbps without any. A download's and a scan's
// aren't capped so (sections 7 and 8).
func (r PlayRequest) BitrateCap(own, pairing int) int {
	caps := []int{r.MaxBitrateKbps, own, pairing}
	if r.Capabilities != nil {
		caps = append(caps, r.Capabilities.MaxBitrateKbps)
	}
	if r.Transcode != nil {
		caps = append(caps, r.Transcode.MaxBitrateKbps)
	}
	out := 0
	for _, c := range caps {
		if c > 0 && (out == 0 || c < out) {
			out = c
		}
	}
	if out == 0 {
		return DefaultCapKbps
	}
	return out
}

// playMembers are the members of play's request this part and video
// know: null in any of them, anywhere, refuses the request (core section
// 2.3).
var playMembers = []string{"title", "purpose", "delivery", "container", "transcode", "audioRenditions", "capabilities",
	"startPositionMs", "maxBitrateKbps", "audioLanguage", "audioTrack", "versionId", "subtitleLanguage", "subtitleTrack"}

// ReadPlay reads play's request as step 1 of playback section 2.2 checks
// it: an I-JSON object; title a key playable says play takes (nil:
// ParseKey's); purpose, delivery and container of their lists, container
// with delivery remux only, and then required; a transcode object with
// delivery transcode only, each target of its values; numbers in range;
// languages valid tags (LanguageTag); capabilities as section 2.1 reads
// them, unknown entries ignored; purpose and delivery together as
// PurposeAllows says. A refusal is a *PlayRefusal (400). Track IDs are
// checked later, at step 4 (TrackIDsOK).
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
	if r.Delivery, have, err = o.text("delivery"); err != nil {
		return r, err
	} else if have && !slices.Contains([]string{DeliveryFile, DeliveryRemux, DeliveryTranscode}, r.Delivery) {
		return r, badPlay("delivery isn't file, remux or transcode")
	}
	if r.Container, have, err = o.text("container"); err != nil {
		return r, err
	}
	switch {
	case have && r.Delivery != DeliveryRemux:
		return r, badPlay("container without delivery remux")
	case have && !IsContainer(r.Container):
		return r, badPlay("container isn't one of containers")
	case !have && r.Delivery == DeliveryRemux:
		return r, badPlay("delivery remux without container")
	}
	if v, ok := o["transcode"]; ok {
		if r.Delivery != DeliveryTranscode {
			return r, badPlay("a transcode object without delivery transcode")
		}
		t, empty, err := readTargets(v)
		if err != nil {
			return r, err
		}
		r.Transcode, r.emptyTrack = &t, r.emptyTrack || empty
	}
	if r.AudioRenditions, _, err = o.boolean("audioRenditions"); err != nil {
		return r, err
	}
	if v, ok := o["capabilities"]; ok {
		c, err := readCapabilities(v)
		if err != nil {
			return r, err
		}
		if !c.Empty() {
			r.Capabilities = &c
		}
	}
	if r.StartPositionMs, err = o.integer("startPositionMs", MaxPositionMs); err != nil {
		return r, err
	}
	kbps, err := o.integer("maxBitrateKbps", MaxKbps)
	if err != nil {
		return r, err
	}
	r.MaxBitrateKbps = int(kbps)
	for _, l := range []struct {
		name string
		dst  *string
	}{{"audioLanguage", &r.AudioLanguage}, {"subtitleLanguage", &r.SubtitleLanguage}} {
		if *l.dst, have, err = o.text(l.name); err != nil {
			return r, err
		} else if have && !LanguageTag(*l.dst) {
			return r, badPlay(l.name + " isn't a valid tag")
		}
	}
	if r.VersionID, _, err = o.text("versionId"); err != nil {
		return r, err
	}
	for _, t := range []struct {
		name string
		dst  *string
	}{{"audioTrack", &r.AudioTrack}, {"subtitleTrack", &r.SubtitleTrack}} {
		if *t.dst, have, err = o.text(t.name); err != nil {
			return r, err
		} else if have && *t.dst == "" {
			r.emptyTrack = true
		}
	}
	if !PurposeAllows(r.Purpose, r.Delivery, r.Transcode) {
		return r, badPlay("purpose " + r.Purpose + " doesn't take that delivery")
	}
	return r, nil
}

// readCapabilities reads capabilities as an owning server does (playback
// section 2.1, video section 5.1): an array of the wrong type or with an
// entry of the wrong type, or a number out of range, refuses; an unknown
// entry, in another case included, is ignored; dv counts only with
// dvProfiles.
func readCapabilities(v any) (Capabilities, error) {
	var c Capabilities
	o, ok := v.(map[string]any)
	if !ok {
		return c, badPlay("capabilities isn't an object")
	}
	for _, l := range capLists {
		raw, ok := o[l.name]
		if !ok {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			return c, badPlay("capabilities." + l.name + " isn't an array")
		}
		var entries []string
		for _, e := range arr {
			s, ok := e.(string)
			if !ok {
				return c, badPlay("capabilities." + l.name + " has an entry that isn't a string")
			}
			entries = append(entries, s)
		}
		*l.of(&c) = listed(entries, l.list)
	}
	if raw, ok := o["dvProfiles"]; ok {
		arr, ok := raw.([]any)
		if !ok {
			return c, badPlay("capabilities.dvProfiles isn't an array")
		}
		var profiles []int
		for _, e := range arr {
			n, ok := integerOf(e)
			if !ok {
				return c, badPlay("capabilities.dvProfiles has an entry that isn't an integer")
			}
			if n >= 0 && n <= 10 {
				profiles = append(profiles, int(n))
			}
		}
		c.DVProfiles = dvListed(profiles)
	}
	co := members(o)
	for _, n := range capNumbers {
		v, err := co.integer(n.name, int64(n.top))
		if err != nil {
			return c, badPlay("capabilities." + n.name + " isn't an integer from 0 to " + strconv.Itoa(n.top))
		}
		*n.of(&c) = int(v)
	}
	if len(c.DVProfiles) == 0 {
		c.HDR = slices.DeleteFunc(c.HDR, func(s string) bool { return s == "dv" })
		if len(c.HDR) == 0 {
			c.HDR = nil
		}
	}
	return c, nil
}

// readTargets reads a transcode object (playback section 2.7, video
// section 5.1): each target present of its values, else refused; empty
// says burnSubtitle was "" (malformed, refused at step 4 with the track
// IDs).
func readTargets(v any) (t TranscodeTargets, empty bool, err error) {
	m, ok := v.(map[string]any)
	if !ok {
		return t, false, badPlay("transcode isn't an object")
	}
	o := members(m)
	for _, s := range []struct {
		name string
		list []string
		dst  *string
	}{{"videoCodec", VideoCodecs, &t.VideoCodec}, {"audioCodec", AudioCodecs, &t.AudioCodec}} {
		val, have, err := o.text(s.name)
		if err != nil {
			return t, false, err
		}
		if have && !slices.Contains(s.list, val) {
			return t, false, badPlay("transcode." + s.name + " isn't one of its list")
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
	val, have, err := o.text("burnSubtitle")
	if err != nil {
		return t, false, err
	}
	t.BurnSubtitle = val
	return t, have && val == "", nil
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
