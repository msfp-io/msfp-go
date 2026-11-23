package describe

import (
	"bytes"
	"errors"
)

// The codec records: the small structures in a file that hold what a
// codecs string carries and ffprobe doesn't print whole (MSFP
// implementers' notes, "Building codecs"). In MP4 they are the sample
// entry's boxes (avcC, hvcC, av1C, vpcC, the AudioSpecificConfig in
// esds); in Matroska its CodecPrivate holds the same records for H.264,
// HEVC and AV1, the AudioSpecificConfig for AAC, and a list of features
// for VP9; in MPEG-TS, ffprobe's extradata for H.264 and HEVC is the
// parameter sets in Annex B form. ffprobe gives each as a stream's
// extradata (-show_data). The parsers read untrusted bytes: they never
// panic, never allocate more than their input, and fail with ErrRecord.

// ErrRecord is a record that can't be read: too short, of another form,
// or holding no parameter set.
var ErrRecord = errors.New("describe: record can't be read")

// AVCRecord is what an H.264 stream's record says: from avcC
// (AVCDecoderConfigurationRecord) or an Annex B sequence parameter set.
// Constraints are the constraint_set flags byte; RefFrames the SPS's
// max_num_ref_frames, 0 when no SPS was read; BitDepth and Chroma
// (chroma_format_idc: 0 monochrome, 1 4:2:0, 2 4:2:2, 3 4:4:4) from the
// SPS, 0 and 1 when it doesn't say.
type AVCRecord struct {
	Profile     int
	Constraints int
	Level       int
	RefFrames   int
	BitDepth    int
	Chroma      int
}

// ParseAVC reads an H.264 record: an avcC (its first byte 1) or Annex B
// parameter sets (start codes).
func ParseAVC(b []byte) (AVCRecord, error) {
	if len(b) >= 4 && b[0] == 1 {
		r := AVCRecord{Profile: int(b[1]), Constraints: int(b[2]), Level: int(b[3]), Chroma: 1}
		if len(b) >= 8 && b[5]&0x1f >= 1 {
			n := int(b[6])<<8 | int(b[7])
			if 8+n <= len(b) && n >= 4 {
				if sps, err := parseAVCSPS(b[8 : 8+n]); err == nil {
					r.RefFrames, r.BitDepth, r.Chroma = sps.RefFrames, sps.BitDepth, sps.Chroma
				}
			}
		}
		return r, nil
	}
	for _, nal := range annexB(b) {
		if len(nal) >= 4 && nal[0]&0x1f == 7 {
			return parseAVCSPS(nal)
		}
	}
	return AVCRecord{}, ErrRecord
}

// avcHighProfiles are the profile_idc values whose SPS carries
// chroma_format_idc and the bit depths.
var avcHighProfiles = map[uint64]bool{100: true, 110: true, 122: true, 244: true, 44: true, 83: true, 86: true,
	118: true, 128: true, 138: true, 139: true, 134: true, 135: true}

// parseAVCSPS reads an H.264 SPS NAL unit (its header byte first) up to
// max_num_ref_frames.
func parseAVCSPS(nal []byte) (AVCRecord, error) {
	r := &bitReader{b: unescape(nal[1:])}
	profile := r.u(8)
	out := AVCRecord{Profile: int(profile), Constraints: int(r.u(8)), Level: int(r.u(8)), BitDepth: 8, Chroma: 1}
	r.ue() // seq_parameter_set_id
	if avcHighProfiles[profile] {
		chroma := r.ue()
		if chroma > 3 {
			return AVCRecord{}, ErrRecord
		}
		out.Chroma = int(chroma)
		if chroma == 3 {
			r.u(1) // separate_colour_plane_flag
		}
		depth := r.ue()
		r.ue() // bit_depth_chroma_minus8
		if depth > 6 {
			return AVCRecord{}, ErrRecord
		}
		out.BitDepth = 8 + int(depth)
		r.u(1)           // qpprime_y_zero_transform_bypass_flag
		if r.u(1) == 1 { // seq_scaling_matrix_present_flag
			lists := 8
			if chroma == 3 {
				lists = 12
			}
			for i := range lists {
				if r.u(1) == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					last, next := int64(8), int64(8)
					for range size {
						if next != 0 {
							next = (last + r.se() + 256) % 256
						}
						if next != 0 {
							last = next
						}
					}
				}
			}
		}
	}
	r.ue()          // log2_max_frame_num_minus4
	switch r.ue() { // pic_order_cnt_type
	case 0:
		r.ue() // log2_max_pic_order_cnt_lsb_minus4
	case 1:
		r.u(1)
		r.se()
		r.se()
		n := r.ue()
		if n > 255 {
			return AVCRecord{}, ErrRecord
		}
		for range n {
			r.se()
		}
	}
	refs := r.ue()
	if r.bad || refs > 16 {
		return AVCRecord{}, ErrRecord
	}
	out.RefFrames = int(refs)
	return out, nil
}

// HEVCRecord is what an HEVC stream's record says: the general
// profile_tier_level of its hvcC (HEVCDecoderConfigurationRecord) or of
// an Annex B SPS. Compatibility holds general_profile_compatibility_flag
// [0..31] with flag 0 as its top bit, as the bitstream orders them;
// Constraints the six bytes from general_progressive_source_flag on.
type HEVCRecord struct {
	ProfileSpace  int
	HighTier      bool
	Profile       int
	Compatibility uint32
	Constraints   [6]byte
	Level         int
}

// ParseHEVC reads an HEVC record: an hvcC (its first byte 1) or Annex B
// parameter sets.
func ParseHEVC(b []byte) (HEVCRecord, error) {
	if len(b) >= 13 && b[0] == 1 {
		return hevcPTL(b[1:13]), nil
	}
	for _, nal := range annexB(b) {
		if len(nal) >= 3 && (nal[0]>>1)&0x3f == 33 {
			sps := unescape(nal[2:])
			// sps_video_parameter_set_id, sps_max_sub_layers_minus1 and
			// sps_temporal_id_nesting_flag fill one byte; the general
			// profile_tier_level follows, byte-aligned.
			if len(sps) < 13 {
				return HEVCRecord{}, ErrRecord
			}
			return hevcPTL(sps[1:13]), nil
		}
	}
	return HEVCRecord{}, ErrRecord
}

// hevcPTL reads the 12 bytes of a general profile_tier_level.
func hevcPTL(p []byte) HEVCRecord {
	r := HEVCRecord{ProfileSpace: int(p[0] >> 6), HighTier: p[0]&0x20 != 0, Profile: int(p[0] & 0x1f),
		Compatibility: uint32(p[1])<<24 | uint32(p[2])<<16 | uint32(p[3])<<8 | uint32(p[4]), Level: int(p[11])}
	copy(r.Constraints[:], p[5:11])
	return r
}

// AV1Record is an av1C's (AV1CodecConfigurationRecord's) facts: the
// sequence header's profile, level and tier of operating point 0, its
// bit depth, and its chroma: monochrome, subsampling in x and y, and the
// chroma sample position (0 unknown, 1 vertical, 2 colocated).
type AV1Record struct {
	Profile        int
	Level          int
	HighTier       bool
	BitDepth       int
	Monochrome     bool
	SubX, SubY     bool
	ChromaPosition int
}

// ParseAV1 reads an av1C: its marker bit and version 1 first.
func ParseAV1(b []byte) (AV1Record, error) {
	if len(b) < 4 || b[0] != 0x81 {
		return AV1Record{}, ErrRecord
	}
	r := AV1Record{Profile: int(b[1] >> 5), Level: int(b[1] & 0x1f), HighTier: b[2]&0x80 != 0, BitDepth: 8,
		Monochrome: b[2]&0x10 != 0, SubX: b[2]&0x08 != 0, SubY: b[2]&0x04 != 0, ChromaPosition: int(b[2] & 0x03)}
	switch {
	case b[2]&0x40 != 0 && b[2]&0x20 != 0:
		r.BitDepth = 12
	case b[2]&0x40 != 0:
		r.BitDepth = 10
	}
	return r, nil
}

// VP9Record is what a VP9 stream's record says: from a vpcC
// (VPCodecConfigurationRecord, MP4) or Matroska's CodecPrivate features.
// Level and BitDepth are 0, Chroma -1, when the record doesn't say;
// Chroma is the vpcC's chromaSubsampling (0 and 1 4:2:0, 2 4:2:2, 3
// 4:4:4). The colour of a vpcC isn't kept: the stream's own colour is.
type VP9Record struct {
	Profile  int
	Level    int
	BitDepth int
	Chroma   int
}

// ParseVP9 reads a VP9 record: Matroska's features (ID, length 1, value;
// IDs 1 profile, 2 level, 3 bit depth, 4 chroma subsampling) when the
// bytes are wholly of that form, else a vpcC box's payload (version 1).
func ParseVP9(b []byte) (VP9Record, error) {
	if len(b) >= 3 && len(b)%3 == 0 {
		r, ok := VP9Record{Profile: -1, Chroma: -1}, true
		for i := 0; i < len(b) && ok; i += 3 {
			ok = b[i] >= 1 && b[i] <= 4 && b[i+1] == 1
			if ok {
				switch v := int(b[i+2]); b[i] {
				case 1:
					r.Profile = v
				case 2:
					r.Level = v
				case 3:
					r.BitDepth = v
				case 4:
					r.Chroma = v
				}
			}
		}
		if ok && r.Profile >= 0 && r.Profile <= 3 {
			return r, nil
		}
	}
	if len(b) >= 7 && b[0] == 1 {
		return VP9Record{Profile: int(b[4]), Level: int(b[5]), BitDepth: int(b[6] >> 4), Chroma: int(b[6]>>1) & 7}, nil
	}
	return VP9Record{}, ErrRecord
}

// ParseAudioConfig reads an AAC AudioSpecificConfig: its audio object
// type (2 LC, 5 HE-AAC, 29 HE-AAC v2, 42 xHE-AAC…), the escape (31)
// read on.
func ParseAudioConfig(b []byte) (int, error) {
	r := &bitReader{b: b}
	ot := int(r.u(5))
	if ot == 31 {
		ot = 32 + int(r.u(6))
	}
	if r.bad || ot == 0 {
		return 0, ErrRecord
	}
	return ot, nil
}

// annexB splits Annex B bytes into NAL units, their start codes
// removed; none when b doesn't start with one.
func annexB(b []byte) [][]byte {
	start := func(b []byte) int {
		switch {
		case bytes.HasPrefix(b, []byte{0, 0, 1}):
			return 3
		case bytes.HasPrefix(b, []byte{0, 0, 0, 1}):
			return 4
		}
		return 0
	}
	n := start(b)
	if n == 0 {
		return nil
	}
	var out [][]byte
	b = b[n:]
	for len(b) > 0 && len(out) < 64 {
		i := bytes.Index(b, []byte{0, 0, 1})
		if i < 0 {
			out = append(out, b)
			break
		}
		end := i
		if end > 0 && b[end-1] == 0 {
			end--
		}
		out = append(out, b[:end])
		b = b[i+3:]
	}
	return out
}

// unescape removes the emulation prevention bytes (00 00 03) of a NAL
// unit's payload.
func unescape(b []byte) []byte {
	if !bytes.Contains(b, []byte{0, 0, 3}) {
		return b
	}
	out := make([]byte, 0, len(b))
	zeros := 0
	for _, c := range b {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, c)
	}
	return out
}

// bitReader reads bits most significant first; past the end it reads
// zeros and sets bad.
type bitReader struct {
	b   []byte
	pos int
	bad bool
}

func (r *bitReader) u(n int) uint64 {
	var v uint64
	for range n {
		if r.pos >= len(r.b)*8 {
			r.bad = true
			return v
		}
		v = v<<1 | uint64(r.b[r.pos/8]>>(7-r.pos%8)&1)
		r.pos++
	}
	return v
}

// ue reads an unsigned Exp-Golomb code, at most 31 leading zeros.
func (r *bitReader) ue() uint64 {
	zeros := 0
	for r.u(1) == 0 {
		if r.bad || zeros == 31 {
			r.bad = true
			return 0
		}
		zeros++
	}
	return 1<<zeros - 1 + r.u(zeros)
}

// se reads a signed Exp-Golomb code.
func (r *bitReader) se() int64 {
	k := r.ue()
	if k%2 == 1 {
		return int64(k+1) / 2
	}
	return -int64(k / 2)
}
