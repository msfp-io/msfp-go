package msfp

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/msfp-io/msfp-go/wire"
)

// What the owning server chooses for a peer, by default (MSFP playback
// section 2.6, video section 3.6): the copy and the tracks a play plays
// when it names none, and what a copy's name and a track's title say.
// Each is a Config hook; these are the recommended defaults, and an
// owning server that chooses otherwise still conforms.

// DefaultChooseCopy is Config.ChooseCopy's default, playback section
// 2.6's recommended default (wire.ChooseCopy): the first copy whose
// audio, the title's own sound, has the request's audioLanguage (the
// same tag looked for in every copy before the same base language),
// else the first, the host's preferred.
func DefaultChooseCopy(copies []wire.Copy, req wire.PlayRequest) int {
	choices := make([]wire.PlayCopy, len(copies))
	for i, c := range copies {
		choices[i] = wire.PlayCopy{ID: c.ID, Audio: c.AudioTracks}
	}
	return max(wire.ChooseCopy(choices, "", req.AudioLanguage), 0)
}

// DefaultChooseTracks is Config.ChooseTracks' default, playback section
// 2.6's and video section 5.4's recommended defaults: the audio of the
// title's own sound in audioLanguage (the same tag, then the same base
// language), else the track the file marks default, else the first
// (wire.ChooseAudio); the subtitles in subtitleLanguage, a regular track
// before a forced one, else none.
func DefaultChooseTracks(c wire.Copy, req wire.PlayRequest) (audio, subtitle string) {
	return wire.ChooseAudio(c.AudioTracks, "", req.AudioLanguage), chooseSubtitles(c.SubtitleTracks, "", req.SubtitleLanguage)
}

// chosenCopy is the copy a play plays (playback section 2.6): the one
// versionID names, which wins; else the hook's, the first when it
// answers out of range.
func (e *Engine) chosenCopy(copies []wire.Copy, in wire.PlayRequest) int {
	if i := slices.IndexFunc(copies, func(c wire.Copy) bool { return c.ID == in.VersionID }); in.VersionID != "" && i >= 0 {
		return i
	}
	if i := e.cfg.ChooseCopy(copies, in); i >= 0 && i < len(copies) {
		return i
	}
	return 0
}

// chosenTracks are the audio and the subtitles a play plays of the copy
// (playback section 2.6, video section 5.4): an audioTrack and a
// subtitleTrack that name a track of it win, as does subtitleTrack off;
// else the hook's, the default's when it names no track of the copy.
func (e *Engine) chosenTracks(c wire.Copy, in wire.PlayRequest) (audio, subtitle string) {
	hasAudio := func(id string) bool {
		return slices.ContainsFunc(c.AudioTracks, func(a wire.CopyAudio) bool { return a.ID == id })
	}
	hasSub := func(id string) bool {
		return slices.ContainsFunc(c.SubtitleTracks, func(s wire.CopySubtitle) bool { return s.ID == id })
	}
	audio, subtitle = in.AudioTrack, in.SubtitleTrack
	namedAudio, namedSub := hasAudio(audio), subtitle == "off" || hasSub(subtitle)
	if !namedAudio || !namedSub {
		a, s := e.cfg.ChooseTracks(c, in)
		da, ds := DefaultChooseTracks(c, in)
		if !namedAudio {
			if audio = a; !hasAudio(a) {
				audio = da
			}
		}
		if !namedSub {
			if subtitle = s; s != "" && !hasSub(s) {
				subtitle = ds
			}
		}
	}
	if subtitle == "off" {
		subtitle = ""
	}
	return audio, subtitle
}

// fileLike are the signs of a file name or a release's name in a label:
// a path, an extension, words joined by dots or underscores, brackets, a
// release group after a dash, a release's source or encoder.
var fileLike = regexp.MustCompile(`[/\\_\[\]{}]|\.[A-Za-z0-9]{2,4}$|\w\.\w+\.\w|\S-[A-Za-z0-9]+$|(?i)\b(x26[45]|h\.?26[45]|web-?dl|web-?rip|blu-?ray|bdrip|brrip|hdtv|dvdrip)\b`)

// DefaultCopyName is Config.CopyName's default, a copy's name as sent
// (MSFP video section 3.6): an edition or quality label, plain text of at
// most 64 characters; "" for one that looks like a file name or a
// release's name, which is never sent. It is the owner's choice, made
// for its privacy: a host may keep names another way.
func DefaultCopyName(name string) string {
	name = wire.CleanText(name, len(name))
	if fileLike.MatchString(name) {
		return ""
	}
	return wire.CleanText(name, wire.MaxCopyName)
}

// trackWords are the words a track's title may hold (MSFP video section
// 3.6: a description of the track, such as "Commentary", "SDH", "Signs &
// Songs" or "Forced"), lowercase. A title with any other word (a
// language's name, a release group's) is dropped: the engine can't tell
// a description from the rest otherwise.
var trackWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`
		commentary commentaries director director's directors cast crew writer writers producer producers composer
		actor actors and & + with by of the track tracks
		sdh cc hi hoh hearing impaired deaf captions closed
		forced full complete signs songs sign song dialogue dialog dialogues lyrics karaoke
		audio description descriptive described narration narrated visually
		original dub dubbed dubbing stereo mono surround atmos lossless
		music score isolated effects only simplified traditional
		commentaire commentaires forcé forcés forcée forcées complets sourds malentendants audiodescription
		kommentar erzwungen comentario comentarios forzados audiodescripción`) {
		trackWords[w] = true
	}
}

var trackNumber = regexp.MustCompile(`^[0-9]{1,2}(\.[0-9])?$`)

// DefaultTrackTitle is Config.TrackTitle's default, a track's title as
// sent (MSFP video section 3.6): plain text of at most 64 characters,
// made only of trackWords and channel counts ("5.1"); "" for any other,
// a title that is only a language's name or carries a release group's
// among them. It is the owner's choice, made for its privacy: a host may
// keep titles another way.
func DefaultTrackTitle(title string) string {
	title = wire.CleanText(title, wire.MaxCopyName)
	words := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '.' && r != '&' && r != '+'
	})
	if len(words) == 0 {
		return ""
	}
	for _, w := range words {
		if !trackWords[w] && !trackNumber.MatchString(w) {
			return ""
		}
	}
	return title
}
