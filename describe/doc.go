// Package describe turns a media file into an MSFP copy (wire.Copy), as
// a Go host needs it to list its files in its catalog (MSFP Media video
// 3.2 to 3.6; the registry; the implementers' notes, "Facts from
// a media server"), with ffprobe:
//
//   - Sniff reads the media type from a file's first bytes (ISO BMFF
//     brands, the EBML DocType, MPEG-TS packets of 188 and 192 bytes,
//     AVI, WAV, AIFF, Ogg, FLAC, MPEG-PS, FLV, MP3, ADTS); AudioOrVideo,
//     CanonicalType and ContainerType apply the registry's rules,
//     aliases and container names;
//   - Prober.Probe runs ffprobe on a local regular file, safely: real
//     path resolved, the file opened read-only and handed to the child
//     process as it was checked, the sniffed demuxer forced, the file
//     protocol only, bounded probe size, time and output; two runs, the
//     second reading the codec records of the video (and AAC) streams
//     only, so attachments and cover art are never read; optionally a
//     frame read of HDR pictures for HDR10+ and the alternative transfer
//     characteristics SEI (NeedsFrames, ReadFrames);
//   - ParseAVC, ParseHEVC, ParseAV1, ParseVP9 and ParseAudioConfig read
//     the codec records (avcC, hvcC, av1C, vpcC, Matroska's
//     CodecPrivate, Annex B parameter sets, AudioSpecificConfig);
//     VideoCodecs and AudioCodecs build RFC 6381 codecs strings from
//     them, or from ffprobe's fields under the spec's tolerance rules;
//   - Shape turns a probe into the copy: registry tokens, H.273 colour,
//     Dolby Vision, HDR10+, Atmos and DTS:X, frame rate, sample aspect,
//     rotation, reference frames, 3D layout, tracks by stream index with
//     their flags and DASH roles, external subtitles numbered above the
//     file's streams, chapters with clean names. Prober.Describe does
//     both;
//   - Probe.Raw keeps ffprobe's output for a host's own uses (an
//     identifier reading the title tag), without the file's path or
//     attachments; Parse reads kept outputs again.
//
// The copy's id and etag are the host's: a stable version ID, and an
// identity that changes whenever the file's bytes change. Path policy
// (which folders a host may read) is the host's too: Probe only checks
// that the path is a regular file.
//
// Nothing here is a server: the package reads files and runs ffprobe,
// nothing else, and never names a path in its errors.
package describe
