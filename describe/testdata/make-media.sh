#!/bin/sh
# The media behind the fixtures: run in an empty folder (on disk, a few
# MB), then, from the package's folder:
#   DESCRIBE_MEDIA=<that folder> go test -run TestMakeFixtures .
# handmade-dv-atmos.json is written by hand (Dolby Vision, Atmos, DTS:X,
# PGS, HDR10+ frames: ffmpeg makes none of them).
set -e
F="ffmpeg -nostdin -y -v error"
V="-f lavfi -i testsrc2=s=320x180:r=24000/1001:d=2"
A="-f lavfi -i sine=f=440:r=48000:d=2"
printf '1\n00:00:00,000 --> 00:00:01,000\nHello\n' > sub.srt
cat > chapters.txt <<'C'
;FFMETADATA1
[CHAPTER]
TIMEBASE=1/1000
START=0
END=1000
title=Opening
[CHAPTER]
TIMEBASE=1/1000
START=1000
END=2000
title=www.example-site.com
C
# H.264 High + AAC LC in MP4, rotated, with chapters
$F $V $A -i chapters.txt -map 0 -map 1 -map_chapters 2 -c:v libx264 -profile:v high -refs 3 -c:a aac unrotated.mp4
$F -display_rotation 90 -i unrotated.mp4 -map 0 -c copy h264-aac.mp4 && rm unrotated.mp4
# H.264 High 10 avc3 in MP4
$F $V -c:v libx264 -profile:v high10 -pix_fmt yuv420p10le -tag:v avc3 h264-high10.mp4
# HEVC Main 10 HDR10 in MKV, E-AC-3 5.1 fr default + AC-3 en commentary, srt forced, ass
$F $V $A $A -i sub.srt -map 0 -map 1 -map 2 -map 3 -map 3 -c:v libx265 -pix_fmt yuv420p10le -x265-params log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:master-display="G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)":max-cll=1000,400 \
 -c:a:0 eac3 -ac:a:0 6 -c:a:1 ac3 -c:s:0 srt -c:s:1 ass \
 -metadata:s:a:0 language=fre -metadata:s:a:1 language=eng -metadata:s:a:1 title="Director's Commentary" \
 -metadata:s:s:0 language=eng -metadata:s:s:1 language=fra -metadata:s:s:1 title="SDH" \
 -disposition:a:0 default -disposition:a:1 comment -disposition:s:0 forced hevc-hdr10.mkv
# HEVC HLG with ATC SEI (VUI 14, SEI 18) in TS
$F $V -c:v libx265 -pix_fmt yuv420p10le -x265-params log-level=error:colorprim=bt2020:transfer=bt2020-10:colormatrix=bt2020nc:atc-sei=18:pic-struct=0 hevc-hlg-atc.ts
# HEVC hev1 in MP4, Rext 4:2:2 10-bit
$F $V -c:v libx265 -pix_fmt yuv422p10le -tag:v hev1 -x265-params log-level=error hevc-rext.mp4
# AV1 in MP4 and 4:4:4 in MKV
$F $V -c:v libsvtav1 -preset 12 -pix_fmt yuv420p10le av1.mp4
$F $V -c:v libaom-av1 -cpu-used 8 -pix_fmt yuv444p -frames:v 4 av1-444.mkv
# VP9 in WebM with Opus, VP9 profile 1 in MP4
$F $V $A -c:v libvpx-vp9 -deadline realtime -cpu-used 8 -c:a libopus vp9-opus.webm
$F $V -c:v libvpx-vp9 -deadline realtime -cpu-used 8 -pix_fmt yuv444p vp9-444.mp4
# DTS + FLAC + TrueHD in MKV with SBS stereo
$F $V $A $A -map 0 -map 1 -map 2 -c:v libx264 -c:a:0 dca -strict -2 -c:a:1 truehd -strict -2 -ar 48000 -metadata:s:v stereo_mode=left_right dts-truehd-3d.mkv
# MPEG-2 + MP2 in M2TS (192-byte)
$F $V $A -c:v mpeg2video -c:a mp2 -mpegts_m2ts_mode 1 -f mpegts mpeg2.m2ts
# AVI with MPEG-4 + MP3? (mp3 encoder?) use pcm
$F $V $A -c:v mpeg4 -c:a pcm_s16le mpeg4.avi
# HE-AAC v2? not available; AAC in ADTS
$F $A -c:a aac adts.aac
# WAV, FLAC with cover art
$F $A -c:a pcm_s24le tone.wav
$F -f lavfi -i testsrc2=s=64x64:d=1 -frames:v 1 cover.png
$F $A -i cover.png -map 0 -map 1 -c:a flac -c:v png -disposition:v attached_pic tone.flac
# MKV with a font attachment
printf 'FAKEFONTDATA%.0s' $(seq 1 2000) > font.ttf
$F $V -i sub.srt -map 0 -map 1 -c:v libx264 -c:s ass -attach font.ttf -metadata:s:t mimetype=font/ttf with-font.mkv
# HEVC PQ in MP4 (hev1)
$F -f lavfi -i testsrc2=s=320x180:r=24:d=2 -c:v libx265 -pix_fmt yuv420p10le -tag:v hev1 -x265-params log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc hevc-pq.mp4
# interlaced H.264 in TS
$F -f lavfi -i testsrc2=s=320x180:r=25:d=1 -c:v libx264 -x264-params ref=3 -flags +ildct+ilme -top 1 h264-int.ts
# a playlist renamed .mkv
printf '#EXTM3U\n#EXT-X-ENDLIST\n' > playlist.mkv
