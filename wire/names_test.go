package wire

import (
	"slices"
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	lists := map[string][]string{
		"Containers": Containers, "VideoCodecs": VideoCodecs, "AudioCodecs": AudioCodecs,
		"HDRFormats": HDRFormats, "AudioProfiles": AudioProfiles, "Editions": Editions,
		"ExtraTypes": ExtraTypes, "SubtitleFormats": SubtitleFormats,
	}
	for name, list := range lists {
		seen := map[string]bool{}
		for _, s := range list {
			if s == "" || s != strings.ToLower(s) || seen[s] {
				t.Errorf("%s: %q empty, not lowercase or repeated", name, s)
			}
			seen[s] = true
		}
	}
	if len(Containers) != 18 || len(VideoCodecs) != 11 || len(AudioCodecs) != 15 {
		t.Errorf("the names list: %d containers, %d video codecs, %d audio codecs", len(Containers), len(VideoCodecs), len(AudioCodecs))
	}
	for _, list := range [][]string{Containers, VideoCodecs, AudioCodecs, ExtraTypes} {
		if slices.Contains(list, Other) {
			t.Errorf("%q holds other", list)
		}
	}
	if !slices.Equal(HDRFormats, []string{"hdr10", "hdr10plus", "hlg", "dv"}) || !slices.IsSorted(DVProfiles) {
		t.Errorf("HDRFormats %q, DVProfiles %v", HDRFormats, DVProfiles)
	}
	if !IsContainer("m2ts") || IsContainer("matroska") || IsContainer("MKV") || !IsVideoCodec("theora") || IsVideoCodec("aac") ||
		!IsAudioCodec("pcm_bluray") || IsAudioCodec("h264") {
		t.Error("Is*")
	}
	for _, c := range []struct {
		list    []string
		in, out string
	}{
		{Containers, "mkv", "mkv"}, {Containers, "matroska", Other}, {Containers, "", Other},
		{AudioCodecs, "truehd", "truehd"}, {AudioCodecs, "TrueHD", Other},
		{SubtitleFormats, "pgs", "pgs"}, {SubtitleFormats, "hdmv_pgs_subtitle", Other},
		{Editions, "imax", "imax"}, {Editions, "fan", Other},
	} {
		if got := NameOrOther(c.list, c.in); got != c.out {
			t.Errorf("NameOrOther(%q) = %q; want %q", c.in, got, c.out)
		}
	}
}
