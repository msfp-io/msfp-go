# Changelog

## Unreleased (v0.4.0)

MSFP is renumbered version 0, a draft until version 1 (msfp-spec proposal
13): the engine speaks core 0 and every part at 0, signs with the labels
`msfp-0 …`, and makes codes `msfp-pair-0.…`. It doesn't talk to servers
of the former version 1. The vectors are the spec's `vectors/v0/`.

Pairing codes have the layout of core section 5.1 of version 0: a flag
byte after the expiry, and for a group's code the group's ID and name
(`wire.Code.Group`, `wire.Code.GroupName`). `wire.MaxCodeLifetime`: a
code works 30 days at most.

Groups (MSFP groups part): `CreateGroup`, `Groups`, `Group`,
`MakeCodeWith` (uses, lifetime, a group), `AcceptRequest`,
`RefuseRequest`, `LiftRefusal`, `RequestMember`, `SetGroupAnswer`,
`SetGroupOffers`, `SetGroupPool`, `SetAdmin`, `SetHeir`, `GiveGroup`,
`SetGroupSettings`, `Ban`, `Unban`, `LeaveGroup`, `DissolveGroup`,
`Block`. The calls `group-sync`, `group-push` and `group-pair`; the part
`groups`. A store keeps groups by implementing `GroupsTx` (the shipped
stores do). A link's offers and limits are what it has by hand and what
each of its groups gives it; a playback counts against the first pool
with room. `PeerRecord` gains `Groups`, `NotByHand`, `GroupOffers`,
`Pools` and `JoiningGroup`; `CodeRecord`, `Uses` and `Group`.


## v0.3.0 (2026-11-19)

An episode keyed by its TMDB episode ID carries `place`, its season and
episode in TMDB's numbering, required (MSFP video sections 3, 3.1 and
4.1, settled on 2026-11-19).

API changes since v0.2.0:

- `VideoTitle.Place` (`*Place`, `{Season, Episode}`): the host fills it
  for each of its episodes keyed `series/tmdb/<id>/e<episode id>`, with
  TMDB's current numbers (season 0 to 9,999, episode 0 to 99,999). Such
  an episode without a place, or with numbers out of range, isn't sent;
  `Place` on any other title isn't sent. A new place is a change of the
  title: a changes reading sends it again.
- A peer's episode keyed by its TMDB episode ID without a valid `place`
  is skipped; a kept one has `Place` set. `place` on other titles is
  ignored.
- `wire.Place`, `wire.MaxPlaceSeason`, `wire.MaxPlaceEpisode`;
  `wire.VideoTitle.Place`. vectors/v1/video.json updated from msfp-spec.

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
