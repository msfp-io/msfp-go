package msfp

import (
	"testing"

	"github.com/msfp-io/msfp-go/wire"
)

// The table of MSFP video section 5.4, row by row (playback section
// 2.6's, of the copy and the audio, are wire's: ChooseCopy, ChooseAudio).

func TestChooseSubtitles(t *testing.T) {
	tracks := []wire.CopySubtitle{{ID: "s0", Language: "en"}, {ID: "s1", Language: "fr", Forced: true}, {ID: "s2", Language: "fr-CA"},
		{ID: "s3", Language: "de", Forced: true}}
	for _, c := range []struct {
		track, language, want string
	}{
		{"off", "en", ""},  // off: none, the language ignored
		{"s3", "en", "s3"}, // a track of the copy
		{"s9", "en", "s0"}, // not one: as if absent
		{"", "fr", "s2"},   // same base language, a forced one last
		{"", "fr-CA", "s2"},
		{"", "de", "s3"}, // only a forced one matches
		{"", "it", ""},   // none matches: none shown
		{"", "", ""},
	} {
		if got := chooseSubtitles(tracks, c.track, c.language); got != c.want {
			t.Errorf("track %q, language %q: %q, want %q", c.track, c.language, got, c.want)
		}
	}
	if got := forcedFor(tracks, "fr-CA"); got != "s1" {
		t.Errorf("forced for fr-CA audio: %q", got)
	}
	if got := forcedFor(tracks, "en"); got != "" {
		t.Errorf("forced for en audio: %q", got)
	}
}

func TestParseRange(t *testing.T) {
	for h, want := range map[string]*Range{
		"bytes=0-99": {First: 0, Last: 99}, "bytes=5-": {First: 5, Last: -1}, "bytes=-10": {Suffix: 10},
		"bytes=000-009": {First: 0, Last: 9},
		"bytes=0-1,5-9": nil, "bytes=-": nil, "bytes=5-1": nil, "items=0-9": nil, "bytes = 0-9": nil,
		"bytes=0-1234567890123456": nil, "bytes=-0": nil, "bytes=a-b": nil, "bytes=1-2 ": nil,
	} {
		got, ok := ParseRange(h)
		if ok != (want != nil) || ok && got != *want {
			t.Errorf("%q: %+v %v", h, got, ok)
		}
	}
}

// The choice hooks (MSFP playback section 2.6): what the request names
// wins; else the host's hook, the default's when it answers what the
// copy hasn't.
func TestChoiceHooks(t *testing.T) {
	copies := []wire.Copy{
		{ID: "v0", AudioTracks: []wire.CopyAudio{{ID: "a0", Language: "en"}}},
		{ID: "v1", AudioTracks: []wire.CopyAudio{{ID: "a0", Language: "fr"}, {ID: "a1", Language: "fr", Role: "karaoke"}},
			SubtitleTracks: []wire.CopySubtitle{{ID: "s0", Language: "en"}}},
	}
	e := &Engine{cfg: Config{}.withDefaults()}
	if i := e.chosenCopy(copies, wire.PlayRequest{AudioLanguage: "fr"}); i != 1 {
		t.Errorf("the default by language: %d", i)
	}
	e.cfg.ChooseCopy = func([]wire.Copy, wire.PlayRequest) int { return 7 }
	if i := e.chosenCopy(copies, wire.PlayRequest{AudioLanguage: "fr"}); i != 0 {
		t.Errorf("a hook out of range: %d", i)
	}
	if i := e.chosenCopy(copies, wire.PlayRequest{VersionID: "v1"}); i != 1 {
		t.Errorf("versionId named: %d", i)
	}
	e.cfg.ChooseTracks = func(wire.Copy, wire.PlayRequest) (string, string) { return "a1", "s9" }
	if a, s := e.chosenTracks(copies[1], wire.PlayRequest{SubtitleLanguage: "en"}); a != "a1" || s != "s0" {
		t.Errorf("the hook's audio, the default's subtitles: %q %q", a, s)
	}
	if a, s := e.chosenTracks(copies[1], wire.PlayRequest{AudioTrack: "a0", SubtitleTrack: "off"}); a != "a0" || s != "" {
		t.Errorf("named tracks: %q %q", a, s)
	}
	if DefaultCopyName("Film.2160p.WEB-DL-GRP.mkv") != "" || DefaultCopyName(" 4K  HDR ") != "4K HDR" ||
		DefaultTrackTitle("English") != "" || DefaultTrackTitle("Commentary 5.1") != "Commentary 5.1" {
		t.Error("the default names and titles")
	}
}
