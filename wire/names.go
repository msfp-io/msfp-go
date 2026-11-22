package wire

import "slices"

// The shared vocabulary of MSFP's content parts: the names a copy's
// container and codecs take (video section 3.5, music section 3.3), which
// are also the values of play's capabilities lists (playback section 2.1,
// video section 5.1), and the other closed lists of video's copies and
// extras (video sections 3.2, 3.3 and 3.9). Every name is lowercase and
// compared byte for byte. The lists are the protocol's: callers read
// them, never change them.

// Other is the name of anything outside a names list: a container or a
// codec a viewer's server reads as "convert it", an edition, a subtitle
// format outside its list.
const Other = "other"

// The names list (video section 3.5). Each list holds, first, the names
// a viewer's device commonly plays, then those a viewer's server must
// know of to convert them; play's capabilities take both. Anything else
// is sent, and kept, as Other.
var (
	Containers = []string{
		"mp4", "m4v", "mov", "webm", "mkv", "ts", "mp3", "aac", "m4a", "flac", "ogg", "opus", "wav",
		"avi", "m2ts", "wmv", "flv", "3gp",
	}
	VideoCodecs = []string{
		"h264", "hevc", "av1", "vp8", "vp9",
		"mpeg2video", "mpeg4", "vc1", "wmv3", "h263", "theora",
	}
	AudioCodecs = []string{
		"aac", "mp3", "opus", "vorbis", "flac", "ac3", "eac3", "alac", "pcm_s16le",
		"dts", "truehd", "mp2", "wmav2", "pcm_s24le", "pcm_bluray",
	}
)

// HDRFormats are the high dynamic range formats a video stream's hdr
// and the hdr capability take, in the order a copy lists them (video
// sections 3.3 and 5.1): a format sits beside its base, HDR10+ as
// ["hdr10", "hdr10plus"], Dolby Vision on HDR10 as ["hdr10", "dv"].
var HDRFormats = []string{"hdr10", "hdr10plus", "hlg", "dv"}

// DVProfiles are the Dolby Vision profiles play's dvProfiles capability
// takes, in increasing order (video section 5.1); another is ignored. A
// copy's own dv object takes any profile from 0 to 10 (video section
// 3.3).
var DVProfiles = []int{4, 5, 7, 8, 9, 10}

// AudioProfiles are an audio track's profile values, and those of the
// audioProfiles capability (video section 3.3, playback section 2.1): a
// profile listed in capabilities means the device plays it in its codec
// (atmos in eac3 or truehd, dts-hd-ma in dts), one not listed only the
// codec's core.
var AudioProfiles = []string{"atmos", "dtsx", "dts-hd-ma", "dts-hd-hra", "dts-es", "he-aac", "lc"}

// Editions are a video copy's edition values (video section 3.2); an
// unknown string is kept as Other.
var Editions = []string{"theatrical", "directors", "extended", "unrated", "remastered", "special", "imax", Other}

// ExtraTypes are an extra's extraType values (video section 3.9); an
// unknown or missing one is kept as "clip", not Other.
var ExtraTypes = []string{"trailer", "featurette", "behindthescenes", "deletedscene", "interview", "scene", "short", "clip"}

// SubtitleFormats are a subtitle track's source formats (video section
// 3.3); an unknown or missing one is kept as Other.
var SubtitleFormats = []string{"subrip", "ass", "ssa", "webvtt", "mov_text", "ttml", "pgs", "vobsub", "dvb", Other}

// IsContainer says whether s is a container of the names list.
func IsContainer(s string) bool { return slices.Contains(Containers, s) }

// IsVideoCodec says whether s is a video codec of the names list.
func IsVideoCodec(s string) bool { return slices.Contains(VideoCodecs, s) }

// IsAudioCodec says whether s is an audio codec of the names list.
func IsAudioCodec(s string) bool { return slices.Contains(AudioCodecs, s) }

// NameOrOther is s when list holds it, Other otherwise: how a sender
// names a container, a codec, an edition or a subtitle format, and how a
// receiver keeps one it doesn't know (NameOrOther(Containers, "matroska")
// is "other": a sender maps its media server's names to the list's
// first).
func NameOrOther(list []string, s string) string {
	if slices.Contains(list, s) {
		return s
	}
	return Other
}
