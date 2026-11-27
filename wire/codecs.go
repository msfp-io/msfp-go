package wire

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// What a viewer's server reads from a stream's codecs string (MSFP Media video
// 3.5): the parts players decide on, the parts it ignores left
// out, and the Dolby Vision string rebuilt from the base 4CC and the dv
// object. A stream for which ReadCodecs fails is one the viewer's server
// can't play as it is: it asks for a remux or a transcode, or doesn't
// offer the copy.

// RequiresCodecs says whether codecs is required for a stream of codec
// (MSFP Media video 3.5): h264, hevc, av1 and vp9 (video), aac and dts
// (audio).
func RequiresCodecs(codec string) bool {
	return slices.Contains([]string{"h264", "hevc", "av1", "vp9", "aac", "dts"}, codec)
}

// Codecs are the facts a viewer's server decides by, read from a codecs
// string (ReadCodecs). Profile, Tier, Level and Chroma are read from
// Codecs where its codec carries them (Profile and Level nil otherwise);
// Constraints are HEVC's constraint bytes, as sent; MP4A is AAC's object
// type, the part after mp4a. ("40.2", "67"); BitDepth is the stream's
// own field when it has one, else what Codecs says (AV1, VP9), 0 when
// neither; DolbyVision is the Dolby Vision codecs string rebuilt from
// the base 4CC and the dv object ("dvh1.08.06"), "" without Dolby
// Vision. The parts a consumer ignores (H.264 constraint flags, HEVC
// compatibility flags, VP9 level, AV1 colour) aren't read.
type Codecs struct {
	Codec       string `json:"codec"`
	Codecs      string `json:"codecs,omitempty"`
	Profile     *int   `json:"profile,omitempty"`
	Tier        string `json:"tier,omitempty"`
	Level       *int   `json:"level,omitempty"`
	BitDepth    int    `json:"bitDepth,omitempty"`
	Chroma      string `json:"chroma,omitempty"`
	Constraints string `json:"constraints,omitempty"`
	MP4A        string `json:"mp4a,omitempty"`
	DolbyVision string `json:"dolbyVision,omitempty"`
}

// The Dolby Vision entries, by the base 4CC a codecs string starts with
// (MSFP Media video 3.5).
var dvEntry = map[string]string{"hvc1": "dvh1", "hev1": "dvhe", "av01": "dav1", "avc1": "dva1", "avc3": "dvav"}

var (
	codecsAVC  = regexp.MustCompile(`^(avc1|avc3)\.([0-9A-Fa-f]{2})[0-9A-Fa-f]{2}([0-9A-Fa-f]{2})$`)
	codecsHEVC = regexp.MustCompile(`^(hvc1|hev1)\.[ABC]?([0-9]{1,2})\.[0-9A-Fa-f]{1,8}\.([LH])([0-9]{1,3})((?:\.[0-9A-Fa-f]{1,2}){0,6})$`)
	codecsAV1  = regexp.MustCompile(`^(av01)\.([0-7])\.([0-9]{2})([MH])\.([0-9]{2})(?:\.([01])\.([01][01][0-3])\.[0-9]{2}\.[0-9]{2}\.[0-9]{2}\.[01])?$`)
	codecsVP9  = regexp.MustCompile(`^(vp09)\.([0-9]{2})\.[0-9]{2}\.([0-9]{2})(?:\.([0-9]{2})(?:\.[0-9]{2}){0,4})?$`)
	codecsAAC  = regexp.MustCompile(`^mp4a\.(40\.[1-9][0-9]?|66|67|68)$`)
	codecsDTS  = regexp.MustCompile(`^(dtsc|dtsh|dtsl|dtse)$`)
)

// ErrCodecs is a codecs string a viewer's server can't read: the copy is
// one it can't play as it is.
var ErrCodecs = errors.New("msfp: can't play as it is")

func codecsRefused(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrCodecs}, a...)...)
}

// ReadCodecs reads a stream's or a track's codecs string as MSFP Media video
// 3.5 says, given its codec, its bitDepth (0 when absent) and
// its dv object (nil when absent). An error (ErrCodecs) when codecs is
// missing or malformed where it is required (RequiresCodecs), starts
// with a Dolby Vision 4CC, or isn't of its codec's form: avc1 or avc3
// and three bytes for h264; hvc1 or hev1 for hevc, its constraint bytes
// required from profile 4; av01 for av1, its long form required off
// 4:2:0; vp09 for vp9, its long form required for profiles 1 and 3;
// mp4a.40.<n> or an MPEG-2 AAC mp4a for aac; dtsc, dtsh, dtsl or dtse
// for dts. A string of another codec is passed as it is.
func ReadCodecs(codec, codecs string, bitDepth int, dv *VideoDV) (Codecs, error) {
	f := Codecs{Codec: codec}
	if CodecsString(codecs) {
		f.Codecs = codecs
	}
	if f.Codecs == "" {
		if RequiresCodecs(codec) {
			return f, codecsRefused("codecs missing or malformed, required for %s", codec)
		}
		return f, nil
	}
	for _, e := range dvEntry {
		if f.Codecs == e || strings.HasPrefix(f.Codecs, e+".") {
			return f, codecsRefused("a Dolby Vision 4CC (%s): codecs carries the base codec's", e)
		}
	}
	num := func(s string, base int) *int {
		n, err := strconv.ParseInt(s, base, 64)
		if err != nil {
			return nil
		}
		i := int(n)
		return &i
	}
	var fourCC string
	switch codec {
	case "h264":
		m := codecsAVC.FindStringSubmatch(f.Codecs)
		if m == nil {
			return f, codecsRefused("codecs isn't avc1 or avc3 and three bytes")
		}
		fourCC, f.Profile, f.Level = m[1], num(m[2], 16), num(m[3], 16)
	case "hevc":
		m := codecsHEVC.FindStringSubmatch(f.Codecs)
		if m == nil {
			return f, codecsRefused("codecs isn't an HEVC string of hvc1 or hev1")
		}
		fourCC, f.Profile, f.Tier, f.Level = m[1], num(m[2], 10), m[3], num(m[4], 10)
		f.Constraints = strings.TrimPrefix(m[5], ".")
		if *f.Profile >= 4 && f.Constraints == "" {
			return f, codecsRefused("an HEVC profile of 4 or more without its constraint bytes")
		}
	case "av1":
		m := codecsAV1.FindStringSubmatch(f.Codecs)
		if m == nil {
			return f, codecsRefused("codecs isn't an AV1 string of av01")
		}
		fourCC, f.Profile, f.Level, f.Tier = m[1], num(m[2], 10), num(m[3], 10), m[4]
		f.BitDepth = *num(m[5], 10)
		switch {
		case m[6] == "1":
			f.Chroma = "4:0:0"
		case m[7] != "":
			f.Chroma = map[string]string{"110": "4:2:0", "111": "4:2:0", "112": "4:2:0", "113": "4:2:0", "100": "4:2:2", "000": "4:4:4"}[m[7]]
		case *f.Profile == 0:
			f.Chroma = "4:2:0"
		case *f.Profile == 1:
			return f, codecsRefused("AV1 profile 1 (4:4:4) without the long form")
		}
	case "vp9":
		m := codecsVP9.FindStringSubmatch(f.Codecs)
		if m == nil {
			return f, codecsRefused("codecs isn't a VP9 string of vp09")
		}
		fourCC, f.Profile = m[1], num(m[2], 10)
		f.BitDepth = *num(m[3], 10)
		switch m[4] {
		case "00", "01":
			f.Chroma = "4:2:0"
		case "02":
			f.Chroma = "4:2:2"
		case "03":
			f.Chroma = "4:4:4"
		case "":
			if *f.Profile == 1 || *f.Profile == 3 {
				return f, codecsRefused("VP9 profile 1 or 3 (not 4:2:0) without the long form")
			}
			f.Chroma = "4:2:0"
		default:
			return f, codecsRefused("a VP9 chroma subsampling other than 00 to 03")
		}
	case "aac":
		m := codecsAAC.FindStringSubmatch(f.Codecs)
		if m == nil {
			return f, codecsRefused("codecs isn't mp4a.40.<n> or an MPEG-2 AAC mp4a")
		}
		f.MP4A = m[1]
	case "dts":
		if !codecsDTS.MatchString(f.Codecs) {
			return f, codecsRefused("codecs isn't dtsc, dtsh, dtsl or dtse")
		}
	}
	if bitDepth > 0 {
		f.BitDepth = bitDepth // the authoritative field
	}
	if dv != nil && fourCC != "" {
		f.DolbyVision = fmt.Sprintf("%s.%02d.%02d", dvEntry[fourCC], dv.Profile, dv.Level)
	}
	return f, nil
}

// PlayableAsIs says whether a viewer's server may consider playing c as
// it is, as far as MSFP decides it (MSFP Media base 3, MSFP Media video
// 3.2 and 3.5): every stream's codecs readable where required
// (ReadCodecs). Whether its device plays the media type, the codecs, the
// threeD layout or Dolby Vision without a compatible base
// (dv.compatibility 0) is the viewer's server's to decide.
func PlayableAsIs(c Copy) bool {
	if v := c.Video; v != nil {
		if _, err := ReadCodecs(v.Codec, v.Codecs, v.BitDepth, v.DV); err != nil {
			return false
		}
	}
	for _, a := range c.AudioTracks {
		if _, err := ReadCodecs(a.Codec, a.Codecs, a.BitDepth, nil); err != nil {
			return false
		}
	}
	return true
}
