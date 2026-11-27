package describe

import (
	"bytes"
	"testing"
)

// A file's media type from its first bytes (MSFP Media video 3.5, the
// registry's brand table); nothing for what isn't media.
func TestSniff(t *testing.T) {
	ftyp := func(major string, compat ...string) []byte {
		b := []byte{0, 0, 0, byte(16 + 4*len(compat))}
		b = append(b, "ftyp"+major+"\x00\x00\x00\x00"...)
		for _, c := range compat {
			b = append(b, c...)
		}
		return b
	}
	ts := func(n, off, count int) []byte {
		b := make([]byte, count*n)
		for k := range count {
			b[off+k*n] = 0x47
		}
		return b
	}
	riff := func(id, form string) []byte { return []byte(id + "\x24\x00\x00\x00" + form + "fmt ") }
	mp3 := func() []byte {
		// MPEG-1 layer III, 128 kbit/s, 44.1 kHz: 417-byte frames.
		b := make([]byte, 417*2+4)
		for _, i := range []int{0, 417} {
			copy(b[i:], []byte{0xff, 0xfb, 0x90, 0x00})
		}
		return b
	}
	adts := func() []byte {
		b := make([]byte, 40)
		hdr := []byte{0xff, 0xf1, 0x50, 0x80, 0x02, 0x1f, 0xfc} // a 16-byte frame
		copy(b, hdr)
		copy(b[16:], hdr)
		return b
	}
	for name, c := range map[string]struct {
		head []byte
		want string
	}{
		"isom":            {ftyp("isom"), "video/mp4"},
		"qt":              {ftyp("qt  "), "video/quicktime"},
		"M4A":             {ftyp("M4A "), "audio/mp4"},
		"3gp":             {ftyp("3gp5"), "video/3gpp"},
		"3g2":             {ftyp("3g2a"), "video/3gpp2"},
		"dash":            {ftyp("dash"), "video/mp4"},
		"unknown, compat": {ftyp("zzzz", "mj2s"), "video/mj2"},
		"unknown":         {ftyp("zzzz"), "video/mp4"},
		"matroska":        {append([]byte{0x1a, 0x45, 0xdf, 0xa3, 0x42, 0x82, 0x88}, "matroska"...), "video/matroska"},
		"webm":            {append([]byte{0x1a, 0x45, 0xdf, 0xa3, 0x42, 0x82, 0x84}, "webm"...), "video/webm"},
		"ebml, no type":   {[]byte{0x1a, 0x45, 0xdf, 0xa3}, "video/matroska"},
		"ts":              {ts(188, 0, 5), "video/mp2t"},
		"m2ts":            {ts(192, 4, 5), "video/vnd.dlna.mpeg-tts"},
		"ts, too short":   {ts(188, 0, 2), ""},
		"ts, broken":      {append(ts(188, 0, 3), 0x00), ""},
		"avi":             {riff("RIFF", "AVI "), "video/avi"},
		"wav":             {riff("RIFF", "WAVE"), "audio/wave"},
		"rf64":            {riff("RF64", "WAVE"), "audio/wave"},
		"aiff":            {[]byte("FORM\x00\x00\x00\x10AIFF"), "audio/aiff"},
		"ogg":             {[]byte("OggS\x00\x02"), "video/ogg"},
		"flac":            {[]byte("fLaC\x00\x00\x00\x22"), "audio/flac"},
		"mpeg-ps":         {[]byte{0, 0, 1, 0xba, 0x44}, "video/mpeg"},
		"flv":             {[]byte("FLV\x01\x05"), "video/x-flv"},
		"mp3":             {mp3(), "audio/mpeg"},
		"mp3, id3":        {append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), mp3()...), "audio/mpeg"},
		"aac, id3":        {append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), adts()...), "audio/aac"},
		"adts":            {adts(), "audio/aac"},
		"one mp3 header":  {[]byte{0xff, 0xfb, 0x90, 0x00, 1, 2, 3}, ""},
		"hls":             {[]byte("#EXTM3U\n#EXT-X-ENDLIST\n"), ""},
		"ffconcat":        {[]byte("ffconcat version 1.0\nfile a.mkv\n"), ""},
		"sdp":             {[]byte("v=0\no=- 0 0 IN IP4 127.0.0.1\n"), ""},
		"asf (open)":      {[]byte{0x30, 0x26, 0xb2, 0x75, 0x8e, 0x66, 0xcf, 0x11}, ""},
		"nothing":         {nil, ""},
	} {
		got := Sniff(c.head)
		if got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
		if got != "" && Demuxer(got) == "" {
			t.Errorf("%s: no demuxer for %q", name, got)
		}
	}
}

func FuzzSniff(f *testing.F) {
	f.Add([]byte("\x00\x00\x00\x14ftypisom\x00\x00\x00\x00mp42"))
	f.Add([]byte{0x1a, 0x45, 0xdf, 0xa3, 0x42, 0x82, 0x84, 'w', 'e', 'b', 'm'})
	f.Add(append([]byte("ID3\x04\x00\x10\x00\x00\x00\x05"), bytes.Repeat([]byte{0xff}, 20)...))
	f.Add([]byte{0xff, 0xf1, 0x50, 0x80, 0x00, 0x1f})
	f.Fuzz(func(t *testing.T, b []byte) {
		if got := Sniff(b); got != "" && Demuxer(got) == "" {
			t.Errorf("%q has no demuxer", got)
		}
	})
}
