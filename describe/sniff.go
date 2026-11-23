package describe

import (
	"bytes"
	"strings"
)

// Media types (MSFP video section 3.5, the registry's section 4): read
// from a file's first bytes where they tell it, else mapped from a
// container's name. Only the types sniffed here are probed: a file whose
// first bytes say none (a playlist, a script, a concat list, an SDP file
// renamed .mkv) is never handed to ffprobe.

// SniffSize is how many of a file's first bytes Sniff needs.
const SniffSize = 8192

// Sniff is the media type a file's first bytes say, the registry's
// spelling, before the audio-or-video rule (AudioOrVideo): an ISO BMFF
// file by its brands (the registry's brand table: the major brand, else
// the compatible ones in order, else video/mp4), Matroska or WebM by its
// EBML DocType, an MPEG transport stream by its packets (188 bytes
// video/mp2t, 192 video/vnd.dlna.mpeg-tts), AVI, WAV (RIFF, RF64,
// BW64), AIFF, Ogg, FLAC, MPEG-PS, FLV, MP3 and ADTS AAC by their
// headers. "" when they say none of those.
func Sniff(b []byte) string {
	riff := func(form string) bool {
		return len(b) >= 12 && (string(b[:4]) == "RIFF" || string(b[:4]) == "RF64" || string(b[:4]) == "BW64") &&
			string(b[8:12]) == form
	}
	switch {
	case len(b) >= 12 && string(b[4:8]) == "ftyp":
		return isoType(b)
	case bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return ebmlType(b)
	case packets(b, 0, 188):
		return "video/mp2t"
	case packets(b, 4, 192):
		return "video/vnd.dlna.mpeg-tts"
	case riff("AVI "):
		return "video/avi"
	case riff("WAVE"):
		return "audio/wave"
	case len(b) >= 12 && string(b[:4]) == "FORM" && (string(b[8:12]) == "AIFF" || string(b[8:12]) == "AIFC"):
		return "audio/aiff"
	case bytes.HasPrefix(b, []byte("OggS\x00")):
		return "video/ogg"
	case bytes.HasPrefix(b, []byte("fLaC")):
		return "audio/flac"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0xba}):
		return "video/mpeg"
	case bytes.HasPrefix(b, []byte("FLV\x01")):
		return "video/x-flv"
	case bytes.HasPrefix(b, []byte("ID3")) && len(b) >= 10:
		return id3Type(b)
	}
	return elementaryAudio(b)
}

// isoType is an ISO BMFF file's media type by its ftyp box's brands.
func isoType(b []byte) string {
	if t := brandType(string(b[8:12])); t != "" {
		return t
	}
	size := int(b[0])<<24 | int(b[1])<<16 | int(b[2])<<8 | int(b[3])
	for i := 16; i+4 <= min(size, len(b)); i += 4 {
		if t := brandType(string(b[i : i+4])); t != "" {
			return t
		}
	}
	return "video/mp4" // an unknown brand
}

// brandTypes are the registry's brand table (section 4.5).
var brandTypes = map[string]string{"qt  ": "video/quicktime", "mp41": "video/mp4", "mp42": "video/mp4",
	"avc1": "video/mp4", "av01": "video/mp4", "M4V ": "video/mp4", "MSNV": "video/mp4", "dby1": "video/mp4",
	"M4A ": "audio/mp4", "M4B ": "audio/mp4", "M4P ": "audio/mp4", "iamf": "audio/mp4",
	"3g2a": "video/3gpp2", "3g2b": "video/3gpp2", "mjp2": "video/mj2", "mj2s": "video/mj2",
	"f4v ": "video/mp4", "isml": "video/mp4", "XAVC": "video/mp4"}

func brandType(b string) string {
	switch {
	case brandTypes[b] != "":
		return brandTypes[b]
	case b == "isom" || len(b) == 4 && strings.HasPrefix(b, "iso"):
		return "video/mp4"
	case strings.HasPrefix(b, "dash") || strings.HasPrefix(b, "cmf") || b == "msdh" || b == "msix":
		return "video/mp4"
	case strings.HasPrefix(b, "3g"):
		return "video/3gpp"
	}
	return ""
}

// ebmlType is an EBML file's media type by its DocType: video/webm for
// "webm", video/matroska for any other (less precise for an unknown
// DocType, never wrong).
func ebmlType(b []byte) string {
	head := b[:min(len(b), 64)]
	if i := bytes.Index(head, []byte{0x42, 0x82}); i >= 0 && i+3 < len(head) && head[i+2]&0x80 != 0 {
		n := int(head[i+2] &^ 0x80)
		if i+3+n <= len(head) && string(head[i+3:i+3+n]) == "webm" {
			return "video/webm"
		}
	}
	return "video/matroska"
}

// packets says whether b holds transport stream packets of size n, their
// sync byte at offset from each packet's start: as many in a row as b
// holds, at least three.
func packets(b []byte, offset, n int) bool {
	count := 0
	for i := offset; i < len(b); i += n {
		if b[i] != 0x47 {
			return false
		}
		count++
	}
	return count >= 3
}

// id3Type is the media type of a file that starts with an ID3v2 tag:
// what follows it when b holds it, else audio/mpeg.
func id3Type(b []byte) string {
	size := 10 + (int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f))
	if b[5]&0x10 != 0 {
		size += 10 // a footer
	}
	if size < len(b) {
		if t := elementaryAudio(b[size:]); t != "" {
			return t
		}
	}
	return "audio/mpeg"
}

// elementaryAudio is an MPEG audio file's type by its first frame
// header: ADTS (layer 0) audio/aac, MPEG layers I to III audio/mpeg;
// two frame headers in a row are required for MPEG audio, whose sync is
// short.
func elementaryAudio(b []byte) string {
	if len(b) < 4 || b[0] != 0xff || b[1]&0xe0 != 0xe0 {
		return ""
	}
	if b[1]&0xf6 == 0xf0 && len(b) >= 7 {
		n := int(b[3]&3)<<11 | int(b[4])<<3 | int(b[5]>>5) // the frame's length
		if n >= 7 && n+2 <= len(b) && b[n] == 0xff && b[n+1]&0xf6 == 0xf0 {
			return "audio/aac"
		}
		return ""
	}
	version, layer := b[1]>>3&3, b[1]>>1&3
	rate, index := b[2]>>2&3, b[2]>>4
	if version == 1 || layer == 0 || rate == 3 || index == 0 || index == 15 {
		return ""
	}
	// Frame length, for layer III of MPEG-1 (the common case); other
	// layers and versions are taken on one header.
	if version == 3 && layer == 1 {
		kbps := []int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320}[index]
		hz := []int{44100, 48000, 32000}[rate]
		n := 144000*kbps/hz + int(b[2]>>1&1)
		if n+2 > len(b) || b[n] != 0xff || b[n+1]&0xe0 != 0xe0 {
			return ""
		}
	}
	return "audio/mpeg"
}

// pairedSubtypes are the subtypes whose audio/ and video/ forms name the
// same container (the registry's section 4.1).
var pairedSubtypes = map[string]bool{"mp4": true, "matroska": true, "webm": true, "ogg": true, "3gpp": true,
	"3gpp2": true, "mp2t": true}

// AudioOrVideo applies the audio-or-video rule (video section 3.5) to a
// media type of the pairs the registry lists: audio/<sub> for a file
// with no picture (no video stream other than attached pictures),
// video/<sub> for one with a picture. Other types are kept as they are
// (video/quicktime stays video/quicktime; audio/mpeg isn't video/mpeg).
func AudioOrVideo(t string, picture bool) string {
	top, sub, _ := strings.Cut(t, "/")
	if !pairedSubtypes[sub] || top != "audio" && top != "video" {
		return t
	}
	if picture {
		return "video/" + sub
	}
	return "audio/" + sub
}

// aliases are the registry's other spellings of its media types
// (sections 4.3 and 4.4): a producer sends the registry's.
var aliases = map[string]string{"video/x-msvideo": "video/avi", "video/vnd.avi": "video/avi", "video/msvideo": "video/avi",
	"audio/wav": "audio/wave", "audio/x-wav": "audio/wave", "audio/vnd.wave": "audio/wave",
	"audio/x-aiff": "audio/aiff", "video/x-matroska": "video/matroska", "audio/x-matroska": "audio/matroska",
	"video/mp2ts": "video/mp2t", "video/m2ts": "video/vnd.dlna.mpeg-tts", "audio/x-flac": "audio/flac",
	"audio/mp3": "audio/mpeg", "audio/x-aac": "audio/aac"}

// CanonicalType is a media type as the registry spells it: lowercase,
// without parameters, an alias (video/x-msvideo, audio/x-wav,
// video/x-matroska…) as its registered type.
func CanonicalType(t string) string {
	t, _, _ = strings.Cut(t, ";")
	t = strings.ToLower(strings.TrimSpace(t))
	if c, ok := aliases[t]; ok {
		return c
	}
	return t
}

// containers map a container's name, as ffprobe's format_name or a
// media server (Jellyfin's Container) reports it, to the registry's
// media type, before the audio-or-video rule.
var containers = map[string]string{"mov,mp4,m4a,3gp,3g2,mj2": "video/mp4", "mp4": "video/mp4", "m4v": "video/mp4",
	"m4a": "audio/mp4", "m4b": "audio/mp4", "mov": "video/quicktime", "3gp": "video/3gpp", "3g2": "video/3gpp2",
	"matroska,webm": "video/matroska", "matroska": "video/matroska", "mkv": "video/matroska", "mka": "audio/matroska",
	"mk3d": "video/matroska", "webm": "video/webm", "mpegts": "video/mp2t", "ts": "video/mp2t", "mts": "video/vnd.dlna.mpeg-tts",
	"m2ts": "video/vnd.dlna.mpeg-tts", "avi": "video/avi", "wav": "audio/wave", "aiff": "audio/aiff", "ogg": "video/ogg",
	"ogv": "video/ogg", "oga": "audio/ogg", "opus": "audio/ogg", "flac": "audio/flac", "mp3": "audio/mpeg",
	"aac": "audio/aac", "mpeg": "video/mpeg", "mpg": "video/mpeg", "vob": "video/mpeg", "flv": "video/x-flv",
	"mj2": "video/mj2", "ac3": "audio/ac3", "eac3": "audio/eac3", "dts": "audio/vnd.dts", "truehd": "audio/vnd.dolby.mlp"}

// ContainerType is the media type of a container named as ffprobe's
// format_name ("matroska,webm", "mov,mp4,m4a,3gp,3g2,mj2", "mpegts") or
// a media server's container name ("mkv", "mp4", "m2ts"; several, comma
// separated, read in order), before the audio-or-video rule; "" for one
// the registry doesn't name. Sniff is more precise where the file is at
// hand: "mov,mp4…" can't tell QuickTime from MP4, nor "mpegts" 188-byte
// from 192-byte packets.
func ContainerType(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if t, ok := containers[name]; ok {
		return t
	}
	for n := range strings.SplitSeq(name, ",") {
		if t, ok := containers[strings.TrimSpace(n)]; ok {
			return t
		}
	}
	return ""
}

// demuxers are ffmpeg's demuxer for each sniffed type: ffprobe is run
// with -f, never left to guess.
var demuxers = map[string]string{"video/mp4": "mov", "audio/mp4": "mov", "video/quicktime": "mov", "video/3gpp": "mov",
	"video/3gpp2": "mov", "video/mj2": "mov", "video/matroska": "matroska", "video/webm": "matroska",
	"video/mp2t": "mpegts", "video/vnd.dlna.mpeg-tts": "mpegts", "video/avi": "avi", "audio/wave": "wav",
	"audio/aiff": "aiff", "video/ogg": "ogg", "audio/flac": "flac", "video/mpeg": "mpeg", "video/x-flv": "flv",
	"audio/mpeg": "mp3", "audio/aac": "aac"}

// Demuxer is the ffmpeg demuxer for a type Sniff gives; "" for another.
func Demuxer(mediaType string) string { return demuxers[mediaType] }
