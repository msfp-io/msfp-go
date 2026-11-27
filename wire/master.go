package wire

import "strings"

// What a master playlist says of a copy's streams served as they are
// (MSFP Media fetching 3.1, MSFP Media playing 2.7): their codecs strings,
// the picture's range, the copy's bits a second. An owning server writes
// its own attributes; these are the facts of the copy, the same for any.

// FixedAudioCodecs is the codecs string of an audio codec whose string is
// the same for every track (the registry's fixed codes): AC-3, E-AC-3,
// TrueHD, FLAC, Opus and ALAC; "" for any other (AAC and DTS depend on
// the track).
func FixedAudioCodecs(codec string) string {
	return map[string]string{"ac3": "ac-3", "eac3": "ec-3", "flac": "fLaC", "opus": "Opus", "truehd": "mlpa", "alac": "alac"}[codec]
}

// dvBrands are the compatibility brands of a Dolby Vision picture with a
// base layer of another range, by its compatibility: SDR (1), HDR10 (6),
// HLG (4), and 2 (SDR, Blu-ray).
var dvBrands = map[int]string{1: "db1p", 2: "db2g", 4: "db4h", 6: "db1p"}

// CopiedVideoCodecs are a copied picture's CODECS and SUPPLEMENTAL-CODECS
// in a master playlist (MSFP Media fetching 3.1, MSFP Media playing 2.7): its
// codecs string with the sample entry of parameter sets out of band
// (hev1 written hvc1, avc3 avc1), as segments carry it; for Dolby Vision
// with a base layer of another range (profile 8, compatibility 1, 2, 4
// or 6), that and, in mp4 segments, its Dolby Vision entry with its brand
// ("dvh1.08.06/db1p"); for Dolby Vision alone (profile 5), its Dolby
// Vision entry and none. A Dolby Vision ReadCodecs can't read is left out.
func CopiedVideoCodecs(v VideoStream, mp4 bool) (codecs, supplemental string) {
	codecs = v.Codecs
	if four, rest, ok := strings.Cut(codecs, "."); ok {
		switch four {
		case "hev1":
			codecs = "hvc1." + rest
		case "avc3":
			codecs = "avc1." + rest
		}
	}
	if v.DV == nil {
		return codecs, ""
	}
	parsed, err := ReadCodecs(v.Codec, codecs, v.BitDepth, v.DV)
	if err != nil || parsed.DolbyVision == "" {
		return codecs, ""
	}
	brand := dvBrands[v.DV.Compatibility]
	switch {
	case brand == "":
		return parsed.DolbyVision, ""
	case mp4:
		return codecs, parsed.DolbyVision + "/" + brand
	}
	return codecs, ""
}

// KeptVideoRange is the VIDEO-RANGE of a picture served in the copy's own
// range: PQ or HLG by its transfer; PQ for Dolby Vision with no transfer
// told whose base layer is PQ or none (compatibility 0, 1 or 6), when
// Dolby Vision is kept (dvKept); SDR otherwise.
func KeptVideoRange(v VideoStream, dvKept bool) string {
	if c := v.Colour; c != nil && c.Transfer != nil {
		switch *c.Transfer {
		case TransferPQ:
			return "PQ"
		case TransferHLG:
			return "HLG"
		}
	}
	if dv := v.DV; dv != nil && dvKept && (dv.Compatibility == 0 || dv.Compatibility == 1 || dv.Compatibility == 6) {
		return "PQ"
	}
	return "SDR"
}

// BitsPerSecond is the copy's average rate, its size over its duration; 0
// when its duration isn't known.
func (c Copy) BitsPerSecond() int64 {
	if c.DurationMs <= 0 {
		return 0
	}
	return c.Size * 8 * 1000 / c.DurationMs
}
