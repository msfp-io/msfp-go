package wire

import (
	"slices"
	"strconv"
	"strings"
)

// What an owning server serves for a play (MSFP playback sections 2.2,
// 2.6 and 2.7): the copy and the audio the request names, else the
// first copy and the file's default audio, and what it makes of that
// copy for the delivery asked, or its refusal.

// PickCopy is the copy a play plays (playback section 2.6): the one
// versionID names, else the first, the owning server's preferred; -1
// when there are none (404).
func PickCopy(copies []Copy, versionID string) int {
	if len(copies) == 0 {
		return -1
	}
	if i := slices.IndexFunc(copies, func(c Copy) bool { return c.ID == versionID }); versionID != "" && i >= 0 {
		return i
	}
	return 0
}

// DefaultAudio is the audio a play plays of a copy (playback section
// 2.6): the track track names, when the copy has it; else the file's
// default audio, the track it marks default, else its first. "" for a
// copy without audio.
func DefaultAudio(audio []CopyAudio, track string) string {
	if track != "" && slices.ContainsFunc(audio, func(a CopyAudio) bool { return a.ID == track }) {
		return track
	}
	if i := slices.IndexFunc(audio, func(a CopyAudio) bool { return a.Default }); i >= 0 {
		return audio[i].ID
	}
	if len(audio) > 0 {
		return audio[0].ID
	}
	return ""
}

// Encoders are what an owning server's transcoder makes (playback
// section 2.7): the codecs it encodes (registry tokens; nil: the
// baseline's only, h264 and aac), the ranges it makes (nil: sdr only),
// and the codecs each segment format carries, by subtype (mp2t, mp4;
// nil: DefaultSegments). A transcode codec it doesn't encode, a range it
// doesn't make, and a remux or a copied stream a segment format can't
// carry are refused with 409 delivery-refused.
type Encoders struct {
	Video    []string
	Audio    []string
	Ranges   []string
	Segments map[string][]string
}

// DefaultSegments are the codecs each HLS segment format carries when
// Encoders names none: what MPEG-TS and fragmented MP4 segments
// commonly hold.
var DefaultSegments = map[string][]string{
	SegmentsTS:  {"h264", "hevc", "aac", "ac3", "eac3", "mp3"},
	SegmentsMP4: {"h264", "hevc", "av1", "vp9", "aac", "ac3", "eac3", "dts", "opus", "flac", "alac", "mp3"},
}

func (e Encoders) video(c string) bool { return c == BaselineVideo || slices.Contains(e.Video, c) }
func (e Encoders) audio(c string) bool { return c == BaselineAudio || slices.Contains(e.Audio, c) }
func (e Encoders) makes(r string) bool { return r == RangeSDR || slices.Contains(e.Ranges, r) }

// Carries says whether the segment format sub (mp2t or mp4) carries
// every codec given.
func (e Encoders) Carries(sub string, codecs ...string) bool {
	seg := e.Segments
	if seg == nil {
		seg = DefaultSegments
	}
	for _, c := range codecs {
		if !slices.Contains(seg[sub], c) {
			return false
		}
	}
	return true
}

// CheckCodecs is the refusal, 409 delivery-refused, of a transcode's
// codecs the owning server doesn't encode (the video codec only for a
// copy with video, video): nil when it encodes them, or they are copy.
// Serve checks it; for a queue entry's own transcode, a codec not made
// refuses the request as the request's own would (music section 5.2).
func (e Encoders) CheckCodecs(t TranscodeTargets, video bool) error {
	switch {
	case video && t.VideoCodec != CodecCopy && t.VideoCodec != "" && !e.video(t.VideoCodec):
		return refusedDelivery("a video codec the owning server doesn't encode")
	case t.AudioCodec != CodecCopy && t.AudioCodec != "" && !e.audio(t.AudioCodec):
		return refusedDelivery("an audio codec the owning server doesn't encode")
	}
	return nil
}

// Served is what an owning server makes of a copy for a play (playback
// section 2.7): the method; what it serves of the video (none for a copy
// with no video) and of the audio, CodecCopy for a stream left
// unchanged, else the codec it becomes; for a transcode, its targets
// with the baseline's values for those left out (MaxHeight 0: the copy's
// own height) and without those a copied stream or a copy with no video
// doesn't take; for a remux, the segment format (mp2t or mp4); and
// whether it counts as a transcode (step 8: a transcode whose video and
// audio are both copied doesn't).
type Served struct {
	Method     string
	Video      string
	Audio      string
	Transcode  *TranscodeTargets
	Segments   string
	Transcodes bool
}

// InBaselineVideo says whether a picture is within the baseline
// (playback section 2.7): H.264 at most High profile and level 4.1
// (codecs avc1 or avc3), 8-bit 4:2:0, progressive, SDR in BT.709 (H.273
// primaries, transfer and matrix 1). A fact the copy doesn't state isn't
// known to be within.
func InBaselineVideo(v VideoStream) bool {
	c := v.Colour
	is1 := func(p *int) bool { return p != nil && *p == 1 }
	return v.Codec == BaselineVideo && avcWithin(v.Codecs) && v.BitDepth == 8 && !v.Interlaced &&
		c != nil && is1(c.Primaries) && is1(c.Transfer) && is1(c.Matrix)
}

// avcWithin: an avc1 or avc3 codecs string of Baseline, Main or High
// profile and a level up to 4.1.
func avcWithin(codecs string) bool {
	fourcc, hex, ok := strings.Cut(codecs, ".")
	if !ok || fourcc != "avc1" && fourcc != "avc3" || len(hex) != 6 {
		return false
	}
	b, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return false
	}
	profile, level := b>>16, b&0xff
	return (profile == 66 || profile == 77 || profile == 100) && level >= 10 && level <= 41
}

// InBaselineAudio says whether a track is within the baseline (playback
// section 2.7): AAC-LC (mp4a.40.2), at most 2 channels, at most 48 kHz.
func InBaselineAudio(a CopyAudio) bool {
	return a.Codec == BaselineAudio && a.Codecs == "mp4a.40.2" && a.Channels >= 1 && a.Channels <= 2 &&
		a.SampleRate >= 1 && a.SampleRate <= 48000
}

// Serve is step 7 of playback section 2.2 for the copy c and its audio
// track audio (DefaultAudio): what the owning server makes, or the
// refusals MSFP names, each a 409 delivery-refused: a delivery not of
// the list; a remux to a media type of another subtype than mp2t or
// mp4, of another top level than the copy's (video/ with video,
// audio/ without), or to a segment format that can't carry its streams;
// a transcode codec enc doesn't encode (the video codec only for a copy
// with video), a copied stream no segment format carries, burnSubtitle
// with video copy, a range other than sdr or keep, or one enc doesn't
// make; file of a copy of several parts (whatever the purpose); file or
// remux of a play whose overall rate (Size × 8 / DurationMs) is above
// capKbps (BitrateCap, 0 for none); and a burnSubtitle the copy doesn't
// have. Without delivery, the baseline: each stream already within it
// left unchanged, the others converted to its codecs. An owning server
// may still refuse for reasons of its own (its policy, what it can make
// of the copy).
func Serve(r PlayRequest, c Copy, audio string, capKbps int, enc Encoders) (Served, error) {
	var track CopyAudio
	if i := slices.IndexFunc(c.AudioTracks, func(a CopyAudio) bool { return a.ID == audio }); i >= 0 {
		track = c.AudioTracks[i]
	}
	hasVideo := c.Video != nil
	s := Served{Method: map[string]string{DeliveryFile: MethodDirect, DeliveryRemux: MethodRemux, DeliveryTranscode: MethodTranscode}[r.Delivery]}
	if r.Delivery != "" && !KnownDelivery(r.Delivery) {
		return s, refusedDelivery("a delivery the owning server doesn't make")
	}
	if r.Delivery == DeliveryRemux {
		top, sub, _ := strings.Cut(r.MediaType, "/")
		want := "video"
		if !hasVideo {
			want = "audio"
		}
		streams := []string{track.Codec}
		if hasVideo {
			streams = append(streams, c.Video.Codec)
		}
		switch {
		case sub != SegmentsTS && sub != SegmentsMP4:
			return s, refusedDelivery("a remux to a media type of another subtype than mp2t or mp4")
		case top != want:
			return s, refusedDelivery("a remux to " + r.MediaType + " of a copy that calls for " + want + "/")
		case !enc.Carries(sub, streams...):
			return s, refusedDelivery("a remux to a segment format that can't carry the copy's streams")
		}
		s.Segments, s.Video, s.Audio = sub, CodecCopy, CodecCopy
	}
	var t TranscodeTargets
	if r.Delivery == DeliveryTranscode {
		if r.Transcode != nil {
			t = *r.Transcode
		}
		if err := enc.CheckCodecs(t, hasVideo); err != nil {
			return s, err
		}
		videoCopy, audioCopy := hasVideo && t.VideoCodec == CodecCopy, t.AudioCodec == CodecCopy
		var copied []string
		if videoCopy {
			copied = append(copied, c.Video.Codec)
		}
		if audioCopy {
			copied = append(copied, track.Codec)
		}
		switch {
		case len(copied) > 0 && !enc.Carries(SegmentsTS, copied...) && !enc.Carries(SegmentsMP4, copied...):
			return s, refusedDelivery("a copied stream no segment format can carry")
		case videoCopy && t.BurnSubtitle != "":
			return s, refusedDelivery("burnSubtitle with video copy")
		case hasVideo && !videoCopy && t.Range != "" && t.Range != RangeSDR && t.Range != RangeKeep:
			return s, refusedDelivery("a range other than sdr or keep")
		case hasVideo && !videoCopy && t.Range != "" && !enc.makes(t.Range):
			return s, refusedDelivery("a range the owning server can't make")
		}
	}
	switch {
	case r.Delivery == DeliveryFile && c.Parts > 1:
		return s, refusedDelivery("a copy of several parts is never delivered as file")
	case (r.Delivery == DeliveryFile || r.Delivery == DeliveryRemux) && (r.Purpose == PurposePlay || r.Purpose == "") &&
		capKbps > 0 && c.DurationMs > 0 && c.Size*8 > int64(capKbps)*c.DurationMs:
		return s, refusedDelivery("the copy's overall rate is above the cap")
	case r.Delivery == DeliveryTranscode && t.BurnSubtitle != "" &&
		!slices.ContainsFunc(c.SubtitleTracks, func(st CopySubtitle) bool { return st.ID == t.BurnSubtitle }):
		return s, refusedDelivery("burnSubtitle isn't a subtitle track of the copy")
	}
	switch r.Delivery {
	case DeliveryFile:
		s.Video, s.Audio = CodecCopy, CodecCopy
	case DeliveryTranscode:
		videoCopy, audioCopy := hasVideo && t.VideoCodec == CodecCopy, t.AudioCodec == CodecCopy
		switch {
		case !hasVideo:
			t.VideoCodec, t.MaxHeight, t.Range, t.BurnSubtitle = "", 0, "", ""
		case videoCopy:
			t.MaxHeight, t.Range = 0, ""
		default:
			if t.VideoCodec == "" {
				t.VideoCodec = BaselineVideo
			}
			if t.Range == "" {
				t.Range = RangeSDR
			}
		}
		if audioCopy {
			t.AudioChannels = 0
		} else {
			if t.AudioCodec == "" {
				t.AudioCodec = BaselineAudio
			}
			if t.AudioChannels == 0 {
				t.AudioChannels = BaselineChannels
			}
		}
		s.Transcode, s.Video, s.Audio = &t, t.VideoCodec, t.AudioCodec
		s.Transcodes = !(videoCopy && audioCopy)
	case "":
		s.Method, s.Audio = MethodRemux, BaselineAudio
		if InBaselineAudio(track) {
			s.Audio = CodecCopy
		}
		if hasVideo {
			s.Video = BaselineVideo
			if InBaselineVideo(*c.Video) {
				s.Video = CodecCopy
			}
		}
		if s.Audio != CodecCopy || hasVideo && s.Video != CodecCopy {
			s.Method, s.Transcodes = MethodTranscode, true
		}
	}
	return s, nil
}
