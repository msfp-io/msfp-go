package describe

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	"github.com/msfp-io/msfp-go/wire"
)

// Codecs strings (MSFP video section 3.5, implementers' notes "Building
// codecs"): built from the stream's record where it has one (exact),
// else from ffprobe's fields under the spec's tolerance: profile, level
// and bit depth always the stream's own; H.264 constraint flags zero,
// HEVC compatibility flags the profile's own bit and tier L, VP9's level
// guessed from size and frame rate, when the record doesn't give them.
// A string that can't be made whole (an HEVC of profile 4 or more
// without its constraint bytes, an AAC of unknown object type) is "":
// the copy is then one a viewer's server doesn't play as it is. On
// Matroska or MPEG-TS the string is the one the stream would have in ISO
// BMFF; on Dolby Vision, the base codec's, with the base 4CC of the
// file's Dolby Vision entry.

// baseFourCC maps a sample entry ffprobe reports (codec_tag_string) to
// the 4CC a codecs string starts with: avc3 and hev1 kept, Dolby
// Vision's entries to their base (the registry's table); "" for any
// other, the codec's default then.
var baseFourCC = map[string]string{"avc1": "avc1", "avc3": "avc3", "hvc1": "hvc1", "hev1": "hev1",
	"dva1": "avc1", "dvav": "avc3", "dvh1": "hvc1", "dvhe": "hev1", "dav1": "av01", "av01": "av01"}

// avcProfiles are ffprobe's H.264 profile names: profile_idc and the
// constraint flags the name implies.
var avcProfiles = map[string][2]int{"Baseline": {66, 0}, "Constrained Baseline": {66, 0x40}, "Main": {77, 0},
	"Extended": {88, 0}, "High": {100, 0}, "Constrained High": {100, 0x0c}, "Progressive High": {100, 0x08},
	"High 10": {110, 0}, "High 10 Intra": {110, 0x10}, "High 4:2:2": {122, 0}, "High 4:2:2 Intra": {122, 0x10},
	"High 4:4:4": {244, 0}, "High 4:4:4 Predictive": {244, 0}, "High 4:4:4 Intra": {244, 0x10}, "CAVLC 4:4:4": {44, 0},
	"Multiview High": {118, 0}, "Stereo High": {128, 0}}

// VideoCodecs is a video stream's codecs string, from its record (the
// stream's Record) where it can be read, else from ffprobe's fields;
// bitDepth is the picture's (VideoBitDepth). "" for a codec of none it
// builds (h264, hevc, av1, vp9 only), or when it can't be made whole.
func VideoCodecs(s Stream, bitDepth int) string {
	var out string
	switch s.CodecName {
	case "h264":
		out = avcCodecs(s)
	case "hevc":
		out = hevcCodecs(s)
	case "av1":
		out = av1Codecs(s, bitDepth)
	case "vp9":
		out = vp9Codecs(s, bitDepth)
	}
	if out == "" || !wire.CodecsString(out) {
		return ""
	}
	return out
}

func avcCodecs(s Stream) string {
	fourCC := baseFourCC[s.CodecTag]
	if fourCC != "avc3" {
		fourCC = "avc1"
	}
	if r, err := ParseAVC(s.Record); err == nil && r.Profile > 0 && r.Level > 0 {
		return fmt.Sprintf("%s.%02X%02X%02X", fourCC, r.Profile, r.Constraints, r.Level)
	}
	p, ok := avcProfiles[s.Profile]
	if !ok || s.Level <= 0 || s.Level > 255 {
		return ""
	}
	return fmt.Sprintf("%s.%02X%02X%02X", fourCC, p[0], p[1], s.Level)
}

func hevcCodecs(s Stream) string {
	fourCC := baseFourCC[s.CodecTag]
	if fourCC != "hev1" {
		fourCC = "hvc1"
	}
	if r, err := ParseHEVC(s.Record); err == nil && r.Profile > 0 && r.Level > 0 {
		return HEVCString(fourCC, r)
	}
	profile := map[string]int{"Main": 1, "Main 10": 2, "Main Still Picture": 3}[s.Profile]
	if profile == 0 || s.Level <= 0 || s.Level > 255 {
		return "" // from profile 4 the constraint bytes are required, and only the record has them
	}
	return fmt.Sprintf("%s.%d.%X.L%d", fourCC, profile, 1<<profile, s.Level)
}

// HEVCString is an HEVC record's codecs string (ISO/IEC 14496-15 annex
// E): the profile space as a letter (none for 0), the profile, the
// compatibility flags in reverse bit order in hex, the tier and level,
// the constraint bytes, trailing zero bytes left out.
func HEVCString(fourCC string, r HEVCRecord) string {
	space := []string{"", "A", "B", "C"}[r.ProfileSpace&3]
	tier := "L"
	if r.HighTier {
		tier = "H"
	}
	out := fmt.Sprintf("%s.%s%d.%X.%s%d", fourCC, space, r.Profile, bits.Reverse32(r.Compatibility), tier, r.Level)
	last := len(r.Constraints)
	for last > 0 && r.Constraints[last-1] == 0 {
		last--
	}
	for _, b := range r.Constraints[:last] {
		out += fmt.Sprintf(".%X", b)
	}
	return out
}

func av1Codecs(s Stream, bitDepth int) string {
	r, err := ParseAV1(s.Record)
	if err != nil {
		profile, ok := map[string]int{"Main": 0, "High": 1, "Professional": 2}[s.Profile]
		if !ok || s.Level < 0 || s.Level > 31 || bitDepth == 0 {
			return ""
		}
		r = AV1Record{Profile: profile, Level: s.Level, BitDepth: bitDepth}
		switch chroma(s.PixFmt) {
		case "400":
			r.Monochrome, r.SubX, r.SubY = true, true, true
		case "420":
			r.SubX, r.SubY = true, true
		case "422":
			r.SubX = true
		}
	}
	tier := "M"
	if r.HighTier {
		tier = "H"
	}
	out := fmt.Sprintf("av01.%d.%02d%s.%02d", r.Profile, r.Level, tier, r.BitDepth)
	if r.Monochrome || !r.SubX || !r.SubY {
		// The long form: monochrome, chroma subsampling and position,
		// colour (1, BT.709, where unknown) and the full-range flag.
		mono, sx, sy := 0, 0, 0
		if r.Monochrome {
			mono = 1
		}
		if r.SubX {
			sx = 1
		}
		if r.SubY {
			sy = 1
		}
		p, t, m, full := colourDigits(s)
		out += fmt.Sprintf(".%d.%d%d%d.%02d.%02d.%02d.%d", mono, sx, sy, r.ChromaPosition, p, t, m, full)
	}
	return out
}

func vp9Codecs(s Stream, bitDepth int) string {
	r, err := ParseVP9(s.Record)
	if err != nil {
		r = VP9Record{Profile: -1, Chroma: -1}
	}
	if r.Profile < 0 {
		n, err := strconv.Atoi(strings.TrimPrefix(s.Profile, "Profile "))
		if err != nil || n < 0 || n > 3 {
			return ""
		}
		r.Profile = n
	}
	if r.BitDepth == 0 {
		r.BitDepth = bitDepth
	}
	if r.BitDepth == 0 {
		return ""
	}
	if r.Level < 10 || r.Level > 62 {
		r.Level = vp9Level(s)
	}
	out := fmt.Sprintf("vp09.%02d.%02d.%02d", r.Profile, r.Level, r.BitDepth)
	ch := r.Chroma
	if ch < 0 || ch > 3 {
		ch = map[string]int{"444": 3, "422": 2, "420": 1, "400": 1}[chroma(s.PixFmt)]
	}
	if r.Profile == 1 || r.Profile == 3 || ch >= 2 {
		p, t, m, full := colourDigits(s)
		out += fmt.Sprintf(".%02d.%02d.%02d.%02d.%02d", ch, p, t, m, full)
	}
	return out
}

// vp9Levels are VP9's levels: the most luma samples a picture and a
// second may hold.
var vp9Levels = []struct {
	level     int
	picture   int64
	perSecond int64
}{{10, 36864, 829440}, {11, 73728, 2764800}, {20, 122880, 4608000}, {21, 245760, 9216000},
	{30, 552960, 20736000}, {31, 983040, 36864000}, {40, 2228224, 83558400}, {41, 2228224, 160432128},
	{50, 8912896, 311951360}, {51, 8912896, 588251136}, {52, 8912896, 1176502272}, {60, 35651584, 1176502272},
	{61, 35651584, 2353004544}, {62, 35651584, 4706009088}}

// vp9Level guesses a VP9 stream's level from its size and frame rate
// (a consumer ignores VP9's level: video section 3.5).
func vp9Level(s Stream) int {
	picture := int64(s.Width) * int64(s.Height)
	rate := 30.0
	if fps := fpsOf(s); fps != "" {
		a, b, _ := strings.Cut(fps, "/")
		x, _ := strconv.ParseFloat(a, 64)
		y, _ := strconv.ParseFloat(b, 64)
		rate = x / y
	}
	for _, l := range vp9Levels {
		if picture <= l.picture && float64(picture)*rate <= float64(l.perSecond) {
			return l.level
		}
	}
	return 62
}

// colourDigits are the colour parts of an AV1 or VP9 long form: H.273
// primaries, transfer and matrix, 1 (BT.709) where unknown, and the
// full-range flag, 0 where unknown.
func colourDigits(s Stream) (p, t, m, full int) {
	p, t, m = 1, 1, 1
	if n, ok := h273Primaries[s.ColorPrimaries]; ok {
		p = n
	}
	if n, ok := h273Transfer[s.ColorTransfer]; ok {
		t = n
	}
	if n, ok := h273Matrix[s.ColorSpace]; ok {
		m = n
	}
	if s.ColorRange == "pc" {
		full = 1
	}
	return
}

// aacProfiles are ffprobe's AAC profile names to the audio object type.
var aacProfiles = map[string]int{"Main": 1, "LC": 2, "SSR": 3, "LTP": 4, "HE-AAC": 5, "HE-AACv2": 29, "LD": 23,
	"ELD": 39, "xHE-AAC": 42, "USAC": 42}

// AudioCodecs is an audio track's codecs string: AAC's object type, from
// its AudioSpecificConfig (the stream's Record) and ffprobe's profile
// (which tells an implicit HE-AAC the record doesn't), DTS's flavour
// from its profile, the registry's fixed codes for AC-3, E-AC-3, TrueHD,
// FLAC, Opus and ALAC; "" when not known.
func AudioCodecs(s Stream) string {
	switch s.CodecName {
	case "aac":
		ot, err := ParseAudioConfig(s.Record)
		if err != nil {
			ot = 0
		}
		// ffprobe's decoder finds SBR and PS the record may not signal.
		if p := aacProfiles[s.Profile]; ot == 0 || p == 29 || p == 5 && ot == 2 {
			ot = p
		}
		if ot == 0 {
			return ""
		}
		return "mp4a.40." + strconv.Itoa(ot)
	case "dts":
		switch p := s.Profile; {
		case strings.HasPrefix(p, "DTS-HD"):
			return "dtsh"
		case p == "DTS Express":
			return "dtse"
		case p == "DTS" || p == "DTS-ES" || p == "DTS 96/24":
			return "dtsc"
		}
		return ""
	}
	return map[string]string{"ac3": "ac-3", "eac3": "ec-3", "flac": "fLaC", "opus": "Opus", "truehd": "mlpa", "alac": "alac"}[s.CodecName]
}
