# Changelog

## v0.2.0 (2026-11-18)

MSFP's platform neutrality, decided on 2026-11-18 (msfp-spec's
api/platforms.md), so that hosts on media servers other than Jellyfin
fit.

API changes since v0.1.0:

- `CopyInfo.Parts`: the files a copy is made of, played one after the
  other as one (0 or 1: one file). A copy of several parts is served as
  `hls`: the engine refuses a `Started` with `file` for it (the host's
  playback is stopped, the peer gets a `500`).
- `StartRequest.Parts`: the copy's parts, as `Describe` said (1 for one
  file), for the host's choice of the protocol.
- `wire.SentTag`, `wire.SentTags`: a media server's language code as
  MSFP sends it: an ISO 639-2 code, /B or /T, of a language with an ISO
  639-1 code as that code (`fre`, `fra` → `fr`; `fre-ca` → `fr-CA`), in
  BCP 47's case conventions; `und`, `mul`, `zxx`, `mis` not sent. Tested
  against `languageCodes` in vectors/v1/strict.json.
- The engine sends the host's languages through them: titles' `Audio`
  and `Subtitles`, tracks' `Language`, `Info`'s languages, and a copy's
  `Audio` when it chooses the copy. A host may give its media server's
  codes as they are.
- `Segment.EndMs` 0 is an end the host doesn't know (credits that run to
  the end): the engine sends the copy's `DurationMs`.
- Documented, no change in code: a host lists only the subtitle tracks
  it can serve (a title's `Subtitles` the same), the default audio is the
  file's, `Role` is best effort.

## v0.1.0 (2026-11-18)

The MSFP v1 engine for Go, extracted from Overfin.
