package wire

import (
	"slices"
	"strings"
)

// What an owning server chooses for a play (MSFP playback sections 2.2,
// 2.6 and 2.7): the copy, the audio, and whether it refuses the delivery
// asked of that copy.

// PlayCopy is what the choices need of a copy of a title: its version
// ID, its files (parts; 1 for one file), its bitrate, its audio tracks
// and the IDs of its subtitle tracks.
type PlayCopy struct {
	ID          string
	Parts       int
	BitrateKbps int
	Audio       []CopyAudio
	Subtitles   []string
}

// A copy's audio tracks are video's CopyAudio (wire/copies.go): their ID
// (a<n>), language tag, role and whether the copy's file marks them
// default are what choosing one needs.
//
// What every owning server does (playback section 2.6): a versionId that
// names a copy wins, as do an audioTrack and a subtitleTrack that name a
// track of it; otherwise it chooses, and the answer says what plays.
// ChooseCopy and ChooseAudio are the recommended defaults for that
// choice: an owning server that chooses otherwise still conforms.

// baseLanguage is a tag's base language, lowercase: what comes before
// its first -.
func baseLanguage(tag string) string {
	base, _, _ := strings.Cut(tag, "-")
	return strings.ToLower(base)
}

// languageMatches are section 2.6's two matches, in order: the same tag
// (compared without case), then the same base language.
var languageMatches = []func(tag, want string) bool{
	strings.EqualFold,
	func(tag, want string) bool { return baseLanguage(tag) == baseLanguage(want) },
}

// OwnSound says whether a track is the title's own sound, as the
// recommended defaults read it (playback section 2.6): no role, or main
// or dub. A commentary, a description or a role this package doesn't
// know is never chosen by language.
func (a CopyAudio) OwnSound() bool { return a.Role == "" || a.Role == "main" || a.Role == "dub" }

// ChooseCopy is playback section 2.6: the copy versionID names, which
// wins; else the recommended default, the first whose audio (its own
// sound) has language, the same tag looked for in every copy before the
// same base language, else the first, the owning server's preferred. -1
// when there are no copies (404).
func ChooseCopy(copies []PlayCopy, versionID, language string) int {
	if len(copies) == 0 {
		return -1
	}
	if versionID != "" {
		if i := slices.IndexFunc(copies, func(c PlayCopy) bool { return c.ID == versionID }); i >= 0 {
			return i
		}
	}
	if language != "" {
		for _, same := range languageMatches {
			for i, c := range copies {
				if slices.ContainsFunc(c.Audio, func(a CopyAudio) bool {
					return a.OwnSound() && a.Language != "" && same(a.Language, language)
				}) {
					return i
				}
			}
		}
	}
	return 0
}

// ChooseAudio is playback section 2.6's audio: the track track names,
// when the copy has it, which wins; else the recommended default, the
// first of its own sound
// matching language (the same tag, then the same base language); else
// the default audio: the track the file marks default, else the first.
// "" for a copy without audio.
func ChooseAudio(audio []CopyAudio, track, language string) string {
	if track != "" && slices.ContainsFunc(audio, func(a CopyAudio) bool { return a.ID == track }) {
		return track
	}
	if language != "" {
		for _, same := range languageMatches {
			if i := slices.IndexFunc(audio, func(a CopyAudio) bool {
				return a.OwnSound() && a.Language != "" && same(a.Language, language)
			}); i >= 0 {
				return audio[i].ID
			}
		}
	}
	if i := slices.IndexFunc(audio, func(a CopyAudio) bool { return a.Default }); i >= 0 {
		return audio[i].ID
	}
	if len(audio) > 0 {
		return audio[0].ID
	}
	return ""
}

// Encoders are the codecs an owning server converts to, as ffmpeg names
// them: a transcode target of another codec is refused with 409
// delivery-refused (playback section 2.7). Nil lists mean the baseline's
// codecs only (h264, aac).
type Encoders struct {
	Video []string
	Audio []string
}

func (e Encoders) video(c string) bool {
	return c == BaselineVideo || slices.Contains(e.Video, c)
}

func (e Encoders) audio(c string) bool {
	return c == BaselineAudio || slices.Contains(e.Audio, c)
}

// RefuseDelivery is step 7 of playback section 2.2, the refusals MSFP
// itself names, each a 409 delivery-refused: a delivery not of the list,
// a remux container not of RemuxContainers, a transcode codec enc doesn't
// encode (the video codec only for a title with a picture); file of a
// copy of several parts (whatever the purpose); file or remux of a play
// whose copy's bitrate is above capKbps (BitrateCap, 0 for none); and a
// burnSubtitle the copy doesn't have. picture says whether the title has
// one (a track hasn't). nil when none applies; an owning server may
// still refuse for reasons of its own (its policy, what it can make of
// the copy).
func RefuseDelivery(r PlayRequest, c PlayCopy, capKbps int, picture bool, enc Encoders) error {
	t := TranscodeTargets{}
	if r.Transcode != nil {
		t = *r.Transcode
	}
	t = t.WithBaseline(picture)
	switch {
	case r.Delivery != "" && !KnownDelivery(r.Delivery):
		return refusedDelivery("a delivery not of the list")
	case r.Delivery == DeliveryRemux && !slices.Contains(RemuxContainers, r.Container):
		return refusedDelivery("a remux into a container it doesn't make")
	case r.Delivery == DeliveryTranscode && picture && t.VideoCodec != "" && !enc.video(t.VideoCodec):
		return refusedDelivery("a video codec it doesn't encode")
	case r.Delivery == DeliveryTranscode && !enc.audio(t.AudioCodec):
		return refusedDelivery("an audio codec it doesn't encode")
	case r.Delivery == DeliveryFile && c.Parts > 1:
		return refusedDelivery("a copy of several parts is never delivered as file")
	case (r.Delivery == DeliveryFile || r.Delivery == DeliveryRemux) && (r.Purpose == PurposePlay || r.Purpose == "") && capKbps > 0 && c.BitrateKbps > capKbps:
		return refusedDelivery("the copy's bitrate is above the cap")
	case picture && r.Transcode != nil && r.Transcode.BurnSubtitle != "" && !slices.Contains(c.Subtitles, r.Transcode.BurnSubtitle):
		return refusedDelivery("burnSubtitle isn't a subtitle track of the copy")
	}
	return nil
}
