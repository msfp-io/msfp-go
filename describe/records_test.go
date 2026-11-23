package describe

import (
	"encoding/hex"
	"testing"
)

func unhex(t testing.TB, s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The records of the fixtures' files, as ffprobe dumped them.
func TestRecords(t *testing.T) {
	avcC := unhex(t, "0164000cffe100186764000cacd941419f9f011000003e90000bb800f142996001000668ebe3cb22c0fdf8f800")
	r, err := ParseAVC(avcC)
	if err != nil || r != (AVCRecord{Profile: 100, Level: 12, RefFrames: 4, BitDepth: 8, Chroma: 1}) {
		t.Errorf("avcC: %+v %v", r, err)
	}
	annexB := unhex(t, "000001676400"+"15acd941433f2602200000030020000006"+"43e28532c000000001"+"68fba3cb22c0")
	if r, err := ParseAVC(annexB); err != nil || r.Profile != 100 || r.Level != 21 || r.RefFrames != 4 {
		t.Errorf("annex B: %+v %v", r, err)
	}
	high10 := unhex(t, "016e000cffe10019676e000ca6cd941419f9f01100000303e90000bb800f14299601000668ebe3cb")
	if r, err := ParseAVC(high10); err != nil || r.Profile != 110 || r.BitDepth != 10 {
		t.Errorf("high 10: %+v %v", r, err)
	}

	hvcC := unhex(t, "0102200000009000000000003cf000fcfdfafa00000f04")
	h, err := ParseHEVC(hvcC)
	if err != nil || h.Profile != 2 || h.HighTier || h.Level != 60 || h.Compatibility != 0x20000000 || h.Constraints != [6]byte{0x90} {
		t.Errorf("hvcC: %+v %v", h, err)
	}
	if s := HEVCString("hvc1", h); s != "hvc1.2.4.L60.90" {
		t.Errorf("hvcC string: %q", s)
	}
	// An Annex B VPS then SPS (with emulation prevention bytes).
	ts := unhex(t, "00000001"+"40010c01ffff022000000300900000030000030078959809"+"00000001"+
		"420101022000000300900000030000030078a00a080b1f")
	if h, err := ParseHEVC(ts); err != nil || h.Profile != 2 || h.Level != 120 || h.Constraints != [6]byte{0x90} {
		t.Errorf("annex B: %+v %v", h, err)
	}
	rext := HEVCRecord{Profile: 4, Compatibility: 0x08000000, Constraints: [6]byte{0x9d, 0x08}, Level: 60}
	if s := HEVCString("hev1", rext); s != "hev1.4.10.L60.9D.8" {
		t.Errorf("rext: %q", s)
	}
	if s := HEVCString("hvc1", HEVCRecord{ProfileSpace: 1, HighTier: true, Profile: 1, Compatibility: 0x60000000, Level: 150}); s != "hvc1.A1.6.H150" {
		t.Errorf("space, tier: %q", s)
	}

	if a, err := ParseAV1(unhex(t, "81004c000a0b")); err != nil || a != (AV1Record{BitDepth: 10, SubX: true, SubY: true}) {
		t.Errorf("av1C: %+v %v", a, err)
	}
	if a, err := ParseAV1(unhex(t, "8120000000")); err != nil || a.Profile != 1 || a.SubX || a.SubY || a.BitDepth != 8 {
		t.Errorf("av1C 4:4:4: %+v %v", a, err)
	}
	if a, err := ParseAV1(unhex(t, "81adfd00")); err != nil || a.Profile != 5 || a.Level != 13 || !a.HighTier || a.BitDepth != 12 || !a.Monochrome || a.ChromaPosition != 1 {
		t.Errorf("av1C bits: %+v %v", a, err)
	}

	if v, err := ParseVP9(unhex(t, "010101"+"02011f"+"03010a"+"040103")); err != nil || v != (VP9Record{Profile: 1, Level: 31, BitDepth: 10, Chroma: 3}) {
		t.Errorf("matroska vp9: %+v %v", v, err)
	}
	if v, err := ParseVP9(unhex(t, "01000000"+"02"+"29"+"a3"+"090e09"+"0000")); err != nil || v != (VP9Record{Profile: 2, Level: 41, BitDepth: 10, Chroma: 1}) {
		t.Errorf("vpcC: %+v %v", v, err)
	}

	for in, want := range map[string]int{"1188": 2, "2b92": 5, "eb09": 29, "f940": 42, "0000": 0} {
		got, _ := ParseAudioConfig(unhex(t, in))
		if got != want {
			t.Errorf("asc %s: %d, want %d", in, got, want)
		}
	}

	for _, bad := range []string{"", "01", "00000001", "000001", "0000016700", "02ffff"} {
		if _, err := ParseAVC(unhex(t, bad)); err == nil {
			t.Errorf("avc %q read", bad)
		}
		if _, err := ParseHEVC(unhex(t, bad)); err == nil {
			t.Errorf("hevc %q read", bad)
		}
		if _, err := ParseAV1(unhex(t, bad)); err == nil {
			t.Errorf("av1 %q read", bad)
		}
	}
}

// ffprobe's dump of a stream's data.
func TestHexDump(t *testing.T) {
	dump := "\n00000000: 0164 000c ffe1 0018 6764 000c acd9 4141  .d......gd....AA\n00000010: 9f9f 0110 0000 3e90 000b b800 f142 9960  ......>......B.`\n00000020: 0100 0668 ebe3 cb22 c0fd f8f8 00         ...h...\".....\n"
	b, ok := hexDump(dump, 45)
	if !ok || len(b) != 45 || b[1] != 0x64 || b[44] != 0 {
		t.Errorf("%x %v", b, ok)
	}
	if _, ok := hexDump(dump, 44); ok {
		t.Error("a length that differs")
	}
	for _, bad := range []string{"0000: 01", "00000000: zz", "00000000: 0102 0304 0506 0708 0910 1112 1314 1516 1718"} {
		if _, ok := hexDump(bad, 2); ok {
			t.Errorf("%q read", bad)
		}
	}
}

// The record parsers on any bytes: no panic, and what they read stays in
// its fields' ranges.
func FuzzRecords(f *testing.F) {
	for _, s := range []string{"0164000cffe100186764000cacd941419f9f011000003e90000bb800f142996001000668ebe3cb22c0fdf8f800",
		"0102200000009000000000003cf000fcfdfafa00000f04", "00000001420101022000000300900000030000030078a00a080b1f",
		"81004c000a0b", "010101020110", "1188", "00000167640015acd941433f260220000003002000000643e28532c0"} {
		b, _ := hex.DecodeString(s)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if r, err := ParseAVC(b); err == nil && (r.Profile > 255 || r.Level > 255 || r.RefFrames > 16 || r.BitDepth > 14) {
			t.Errorf("avc %+v", r)
		}
		if r, err := ParseHEVC(b); err == nil && (r.Profile > 31 || r.Level > 255) {
			t.Errorf("hevc %+v", r)
		} else if err == nil {
			_ = HEVCString("hvc1", r)
		}
		if r, err := ParseAV1(b); err == nil && (r.Profile > 7 || r.Level > 31) {
			t.Errorf("av1 %+v", r)
		}
		ParseVP9(b)
		ParseAudioConfig(b)
		hexDump(string(b), len(b))
		matrix(string(b))
	})
}
