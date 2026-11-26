package describe

import "testing"

// spsBits writes an SPS bit by bit, as H.264 section 7.3.2.1.1 lays it out.
type spsBits struct {
	out  []byte
	n    int
	last byte
}

func (b *spsBits) u(width int, v uint64) {
	for i := width - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.out = append(b.out, 0)
		}
		if v>>uint(i)&1 == 1 {
			b.out[len(b.out)-1] |= 0x80 >> uint(b.n%8)
		}
		b.n++
	}
}

func (b *spsBits) ue(v uint64) {
	v++
	width := 0
	for x := v; x > 1; x >>= 1 {
		width++
	}
	b.u(width, 0)
	b.u(width+1, v)
}

func (b *spsBits) se(v int64) {
	if v > 0 {
		b.ue(uint64(2*v - 1))
	} else {
		b.ue(uint64(-2 * v))
	}
}

// nal is the SPS as a NAL unit: its header, the bits with the stop bit,
// emulation prevention bytes put in.
func (b *spsBits) nal() []byte {
	b.u(1, 1)
	out := []byte{0x67}
	zeros := 0
	for _, c := range b.out {
		if zeros == 2 && c <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// A High 10 SPS with scaling matrices (a 4×4 list read whole, an 8×8
// one ending early), picture order count type 1 with its cycle, and
// 4:4:4 with 12 lists: each read through to the reference frames. (FFmpeg's
// trace_headers reads these units the same, field by field.)
func TestAVCSPSWithScalingMatrices(t *testing.T) {
	high := func(chroma uint64, lists []int, pocType uint64) []byte {
		b := &spsBits{}
		b.u(8, 110) // High 10
		b.u(8, 0)
		b.u(8, 41)
		b.ue(0)      // seq_parameter_set_id
		b.ue(chroma) // chroma_format_idc
		if chroma == 3 {
			b.u(1, 0) // separate_colour_plane_flag
		}
		b.ue(2) // bit_depth_luma_minus8: 10 bits
		b.ue(2)
		b.u(1, 0)
		b.u(1, 1) // seq_scaling_matrix_present_flag
		for _, kind := range lists {
			switch kind {
			case 0:
				b.u(1, 0) // not present
			case 1: // present, read whole: 16 or 64 deltas of 1
				b.u(1, 1)
				for range 16 {
					b.se(1)
				}
			case 2: // present, ends at once: next_scale 0
				b.u(1, 1)
				b.se(-8)
			}
		}
		b.ue(0) // log2_max_frame_num_minus4
		b.ue(pocType)
		if pocType == 1 {
			b.u(1, 0)
			b.se(-3)
			b.se(2)
			b.ue(3) // num_ref_frames_in_pic_order_cnt_cycle
			b.se(1)
			b.se(-1)
			b.se(5)
		} else {
			b.ue(2)
		}
		b.ue(4) // max_num_ref_frames
		b.ue(0)
		b.u(1, 0)
		return b.nal()
	}
	for name, c := range map[string]struct {
		nal    []byte
		chroma int
	}{
		"4:2:0, 8 lists, poc 1":  {high(1, []int{1, 0, 2, 0, 0, 0, 2, 0}, 1), 1},
		"4:2:2, poc 0":           {high(2, []int{0, 0, 0, 0, 0, 0, 0, 0}, 0), 2},
		"4:4:4, 12 lists, poc 1": {high(3, []int{2, 0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 2}, 1), 3},
	} {
		got, err := parseAVCSPS(c.nal)
		if err != nil || got != (AVCRecord{Profile: 110, Level: 41, RefFrames: 4, BitDepth: 10, Chroma: c.chroma}) {
			t.Errorf("%s: %+v %v", name, got, err)
		}
	}
	// A 4×4 list read whole needs its 16 deltas: cut short, refused.
	b := &spsBits{}
	b.u(8, 100)
	b.u(8, 0)
	b.u(8, 40)
	b.ue(0)
	b.ue(1)
	b.ue(0)
	b.ue(0)
	b.u(1, 0)
	b.u(1, 1)
	b.u(1, 1)
	b.se(1)
	if _, err := parseAVCSPS(b.nal()); err == nil {
		t.Error("a scaling list cut short: no error")
	}
}
