package describe

import (
	"testing"

	"github.com/msfp-io/msfp-go/wire"
)

// The codecs builder, from records and from ffprobe's fields alone,
// each result read back by the wire's reader.
func TestVideoCodecs(t *testing.T) {
	avcC := unhex(t, "0142c01effe10010"+"6742c01e"+"da0280bfe5c044000003000400000300c83c58b9a0")
	hvcRext := unhex(t, "0104080000009d08000000003cf000fcfefafa00000f04")
	for _, c := range []struct {
		name string
		s    Stream
		want string
	}{
		{"h264 fields", Stream{CodecName: "h264", Profile: "High", Level: 41}, "avc1.640029"},
		{"h264 constrained baseline", Stream{CodecName: "h264", Profile: "Constrained Baseline", Level: 30}, "avc1.42401E"},
		{"h264 record wins", Stream{CodecName: "h264", Profile: "High", Level: 41, Record: avcC}, "avc1.42C01E"},
		{"h264 avc3", Stream{CodecName: "h264", CodecTag: "avc3", Profile: "Main", Level: 31}, "avc3.4D001F"},
		{"h264 dolby vision dva1", Stream{CodecName: "h264", CodecTag: "dva1", Profile: "High", Level: 40}, "avc1.640028"},
		{"h264 dvav", Stream{CodecName: "h264", CodecTag: "dvav", Profile: "High", Level: 40}, "avc3.640028"},
		{"h264 unknown profile", Stream{CodecName: "h264", Profile: "Foo", Level: 40}, ""},
		{"h264 no level", Stream{CodecName: "h264", Profile: "High", Level: -99}, ""},
		{"hevc fields", Stream{CodecName: "hevc", Profile: "Main 10", Level: 153}, "hvc1.2.4.L153"},
		{"hevc main", Stream{CodecName: "hevc", Profile: "Main", Level: 120}, "hvc1.1.2.L120"},
		{"hevc dvh1", Stream{CodecName: "hevc", CodecTag: "dvh1", Profile: "Main 10", Level: 150}, "hvc1.2.4.L150"},
		{"hevc dvhe", Stream{CodecName: "hevc", CodecTag: "dvhe", Profile: "Main 10", Level: 150}, "hev1.2.4.L150"},
		{"hevc rext without record", Stream{CodecName: "hevc", Profile: "Rext", Level: 120}, ""},
		{"hevc rext record", Stream{CodecName: "hevc", Profile: "Rext", Level: 60, Record: hvcRext}, "hvc1.4.10.L60.9D.8"},
		{"av1 fields", Stream{CodecName: "av1", Profile: "Main", Level: 8, PixFmt: "yuv420p10le"}, "av01.0.08M.10"},
		{"av1 4:2:2 fields", Stream{CodecName: "av1", Profile: "Professional", Level: 12, PixFmt: "yuv422p10le",
			ColorPrimaries: "bt2020", ColorTransfer: "smpte2084", ColorSpace: "bt2020nc"}, "av01.2.12M.10.0.100.09.16.09.0"},
		{"av1 mono", Stream{CodecName: "av1", Profile: "Main", Level: 4, PixFmt: "gray", ColorRange: "pc"}, "av01.0.04M.08.1.110.01.01.01.1"},
		{"av1 record", Stream{CodecName: "av1", Profile: "Main", Level: 99, Record: unhex(t, "81094c00")}, "av01.0.09M.10"},
		{"av1 no depth", Stream{CodecName: "av1", Profile: "Main", Level: 8}, ""},
		{"vp9 fields", Stream{CodecName: "vp9", Profile: "Profile 0", Level: -99, PixFmt: "yuv420p", Width: 1920, Height: 1080,
			AvgFrameRate: "30/1"}, "vp09.00.40.08"},
		{"vp9 profile 2", Stream{CodecName: "vp9", Profile: "Profile 2", PixFmt: "yuv420p10le", Width: 3840, Height: 2160,
			AvgFrameRate: "60/1"}, "vp09.02.51.10"},
		{"vp9 4:4:4", Stream{CodecName: "vp9", Profile: "Profile 1", PixFmt: "yuv444p", Width: 640, Height: 360}, "vp09.01.21.08.03.01.01.01.00"},
		{"vp9 matroska record", Stream{CodecName: "vp9", Profile: "Profile 0", PixFmt: "yuv420p", Width: 640, Height: 360,
			Record: unhex(t, "010100"+"02011f"+"030108")}, "vp09.00.31.08"},
		{"vp9 unknown profile", Stream{CodecName: "vp9", PixFmt: "yuv420p"}, ""},
		{"mpeg2 none", Stream{CodecName: "mpeg2video", Profile: "Main"}, ""},
	} {
		got := VideoCodecs(c.s, VideoBitDepth(c.s))
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
			continue
		}
		if got != "" {
			if _, err := wire.ReadCodecs(c.s.CodecName, got, VideoBitDepth(c.s), nil); err != nil {
				t.Errorf("%s: %q not read: %v", c.name, got, err)
			}
		}
	}
}

func TestAudioCodecs(t *testing.T) {
	for _, c := range []struct {
		s    Stream
		want string
	}{
		{Stream{CodecName: "aac", Profile: "LC"}, "mp4a.40.2"},
		{Stream{CodecName: "aac", Profile: "HE-AAC"}, "mp4a.40.5"},
		{Stream{CodecName: "aac", Profile: "HE-AACv2"}, "mp4a.40.29"},
		{Stream{CodecName: "aac", Profile: "LC", Record: unhex(t, "2b92")}, "mp4a.40.5"},     // explicit SBR in the record
		{Stream{CodecName: "aac", Profile: "HE-AAC", Record: unhex(t, "1188")}, "mp4a.40.5"}, // implicit: the decoder found it
		{Stream{CodecName: "aac", Profile: "HE-AAC", Record: unhex(t, "eb09")}, "mp4a.40.29"},
		{Stream{CodecName: "aac", Profile: "xHE-AAC"}, "mp4a.40.42"},
		{Stream{CodecName: "aac"}, ""},
		{Stream{CodecName: "dts", Profile: "DTS"}, "dtsc"},
		{Stream{CodecName: "dts", Profile: "DTS-ES"}, "dtsc"},
		{Stream{CodecName: "dts", Profile: "DTS 96/24"}, "dtsc"},
		{Stream{CodecName: "dts", Profile: "DTS-HD HRA"}, "dtsh"},
		{Stream{CodecName: "dts", Profile: "DTS-HD MA"}, "dtsh"},
		{Stream{CodecName: "dts", Profile: "DTS-HD MA + DTS:X IMAX"}, "dtsh"},
		{Stream{CodecName: "dts", Profile: "DTS Express"}, "dtse"},
		{Stream{CodecName: "dts"}, ""},
		{Stream{CodecName: "ac3"}, "ac-3"},
		{Stream{CodecName: "eac3", Profile: "Dolby Digital Plus + Dolby Atmos"}, "ec-3"},
		{Stream{CodecName: "truehd"}, "mlpa"},
		{Stream{CodecName: "flac"}, "fLaC"},
		{Stream{CodecName: "opus"}, "Opus"},
		{Stream{CodecName: "mp3"}, ""},
	} {
		got := AudioCodecs(c.s)
		if got != c.want {
			t.Errorf("%+v: %q, want %q", c.s, got, c.want)
		}
		if got != "" {
			if _, err := wire.ReadCodecs(c.s.CodecName, got, 0, nil); err != nil {
				t.Errorf("%q: %v", got, err)
			}
		}
	}
}
