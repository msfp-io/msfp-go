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

func TestCoarsen(t *testing.T) {
	c := coarsen(Capabilities{Containers: []string{"MP4", "mkv", "mp4", "avi"}, VideoCodecs: []string{"h264", "H265"},
		MaxAudioChannels: 7, MaxHeight: 1079, MaxBitrateKbps: 8999}, true)
	if len(c.Containers) != 3 || c.Containers[0] != "avi" || c.Containers[2] != "mp4" || len(c.VideoCodecs) != 1 ||
		c.MaxAudioChannels != 6 || c.MaxHeight != 720 || c.MaxBitrateKbps != 8000 {
		t.Errorf("coarsened: %+v", c)
	}
	if c := coarsen(Capabilities{MaxAudioChannels: 1, MaxHeight: 100, MaxBitrateKbps: 999}, true); c.MaxAudioChannels != 2 ||
		c.MaxHeight != 480 || c.MaxBitrateKbps != 0 {
		t.Errorf("coarsened low: %+v", c)
	}
	if c := coarsen(Capabilities{}, true); c != nil {
		t.Errorf("nothing said: %+v", c)
	}
	if c := coarsen(Capabilities{VideoCodecs: []string{"h264"}, MaxHeight: 1080}, false); c != nil {
		t.Errorf("a track's, with video's members only: %+v", c)
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
