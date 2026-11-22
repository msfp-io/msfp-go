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

// CopyAudio is an audio track of a copy: its ID (a<n>), its language tag,
// its role (commentary, description, or "" for the title's own sound),
// and whether the copy's file marks it default.
type CopyAudio struct {
	ID       string
	Language string
	Role     string
	Default  bool
}

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

// ownSound says whether a track is the title's own sound: no role. A
// commentary or a description is never chosen by language.
func (a CopyAudio) ownSound() bool { return a.Role == "" }

// ChooseCopy is playback section 2.6's table of the copy: the copy
// versionID names; else the first whose audio (its own sound) has
// language, the same tag looked for in every copy before the same base
// language; else the first. -1 when there are no copies (404).
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
					return a.ownSound() && a.Language != "" && same(a.Language, language)
				}) {
					return i
				}
			}
		}
	}
	return 0
}

// ChooseAudio is playback section 2.6's table of the audio: the track
// track names, when the copy has it; else the first of its own sound
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
				return a.ownSound() && a.Language != "" && same(a.Language, language)
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

// RefuseDelivery is step 7 of playback section 2.2, the refusals MSFP
// itself names: a 409 delivery-refused for file of a copy of several
// parts (whatever the purpose), for file or remux of a play whose copy's
// bitrate is above capKbps (BitrateCap), and for a burnSubtitle the copy
// doesn't have. nil when none applies; an owning server may still refuse
// for reasons of its own (its policy, what it can make of the copy).
func RefuseDelivery(r PlayRequest, c PlayCopy, capKbps int) error {
	refuse := func(why string) error { return &PlayRefusal{409, ProblemDeliveryRefused, why} }
	switch {
	case r.Delivery == DeliveryFile && c.Parts > 1:
		return refuse("a copy of several parts is never delivered as file")
	case (r.Delivery == DeliveryFile || r.Delivery == DeliveryRemux) && (r.Purpose == PurposePlay || r.Purpose == "") && c.BitrateKbps > capKbps:
		return refuse("the copy's bitrate is above the cap")
	case r.Transcode != nil && r.Transcode.BurnSubtitle != "" && !slices.Contains(c.Subtitles, r.Transcode.BurnSubtitle):
		return refuse("burnSubtitle isn't a subtitle track of the copy")
	}
	return nil
}
