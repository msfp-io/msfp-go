package wire

import "testing"

// A copied picture's codecs as a master playlist gives them: parameter
// sets out of band, Dolby Vision as supplemental with its brand in mp4
// segments, alone for profile 5.
func TestCopiedVideoCodecs(t *testing.T) {
	pq := 16
	hdr := &VideoColour{Transfer: &pq}
	for _, c := range []struct {
		v                    VideoStream
		mp4                  bool
		codecs, supplemental string
	}{
		{VideoStream{Codec: "h264", Codecs: "avc3.640028", BitDepth: 8}, true, "avc1.640028", ""},
		{VideoStream{Codec: "hevc", Codecs: "hev1.2.4.L153.B0", BitDepth: 10}, false, "hvc1.2.4.L153.B0", ""},
		{VideoStream{Codec: "hevc", Codecs: "hvc1.2.4.L153.B0", BitDepth: 10, Colour: hdr, DV: &VideoDV{Profile: 8, Level: 6, Compatibility: 1}}, true,
			"hvc1.2.4.L153.B0", "dvh1.08.06/db1p"},
		{VideoStream{Codec: "hevc", Codecs: "hvc1.2.4.L153.B0", BitDepth: 10, Colour: hdr, DV: &VideoDV{Profile: 8, Level: 6, Compatibility: 1}}, false,
			"hvc1.2.4.L153.B0", ""},
		{VideoStream{Codec: "hevc", Codecs: "hvc1.2.4.L153.B0", BitDepth: 10, DV: &VideoDV{Profile: 5, Level: 6}}, true, "dvh1.05.06", ""},
	} {
		codecs, supplemental := CopiedVideoCodecs(c.v, c.mp4)
		if codecs != c.codecs || supplemental != c.supplemental {
			t.Errorf("%+v %v: %q %q, want %q %q", c.v, c.mp4, codecs, supplemental, c.codecs, c.supplemental)
		}
	}
}

func TestKeptVideoRange(t *testing.T) {
	pq, hlg, sdr := TransferPQ, TransferHLG, 1
	for _, c := range []struct {
		v      VideoStream
		dvKept bool
		want   string
	}{
		{VideoStream{Colour: &VideoColour{Transfer: &pq}}, false, "PQ"},
		{VideoStream{Colour: &VideoColour{Transfer: &hlg}}, false, "HLG"},
		{VideoStream{Colour: &VideoColour{Transfer: &sdr}}, true, "SDR"},
		{VideoStream{DV: &VideoDV{Profile: 5}}, true, "PQ"},
		{VideoStream{DV: &VideoDV{Profile: 5}}, false, "SDR"},
		{VideoStream{DV: &VideoDV{Profile: 8, Compatibility: 4}}, true, "SDR"},
	} {
		if got := KeptVideoRange(c.v, c.dvKept); got != c.want {
			t.Errorf("%+v %v: %q, want %q", c.v, c.dvKept, got, c.want)
		}
	}
}

func TestBitsPerSecond(t *testing.T) {
	if got := (Copy{Size: 1000, DurationMs: 1000}).BitsPerSecond(); got != 8000 {
		t.Errorf("%d", got)
	}
	if got := (Copy{Size: 1000}).BitsPerSecond(); got != 0 {
		t.Errorf("%d", got)
	}
}

// What a subtitle track is served as: the table's form for a listed
// format, the host's for another, none when it can't.
func TestServeSubtitle(t *testing.T) {
	for _, c := range []struct {
		format, form string
		notApart     bool
		want         SubtitleServing
	}{
		{"subrip", "", false, SubtitleServing{Text: true}},
		{"ass", "", false, SubtitleServing{Text: true, ASS: true}},
		{"hdmv_pgs_subtitle", "", false, SubtitleServing{Pictures: true}},
		{"dvd_subtitle", "", true, SubtitleServing{}},
		{"eia_608", FormText, false, SubtitleServing{Text: true}},
		{"eia_608", "", false, SubtitleServing{}},
	} {
		got := ServeSubtitle(c.format, c.form, c.notApart)
		if got != c.want {
			t.Errorf("%s %q %v: %+v", c.format, c.form, c.notApart, got)
		}
		if got.Apart() && got.Entry("s1").Serving() != got {
			t.Errorf("%s: entry %+v doesn't read back", c.format, got.Entry("s1"))
		}
	}
}
