# Changelog

## Unreleased: the revision of version 0 (proposal 16)

### Changed

- Titles carry their copies: `VideoTitle.Copies` and `MusicTitle.Copies`
  are `wire.Copy` and `wire.MusicCopy`, each copy's ID the host's handle
  (the engine sends the version ID `play` gives). `VideoTitle.Audio`,
  `Subtitles`, `Pictures`, `Video`, `Picture` and `BestPicture` are gone:
  `BestCopy`, `AudioLanguages` and `SubtitleLanguages` work them out from
  the copies. `MusicTitle` has music's new description (`SortName`,
  `ReleaseType`, `Compilation`, `Date`, `OriginalDate`, `Genres`,
  `Image`), no `Year` or `DurationMs`.
- Playback and media (step B2): `play` takes the title's copies as the
  catalog sends them; `Catalog.Copies` and `msfp.Copy` are gone, and
  `Playback.Describe` gives only what the catalog doesn't say
  (`CopyInfo`: previews' layout, fonts, tracks not served apart).
  `StartRequest` carries `Purpose`, `Delivery`, `Container`, `Transcode`,
  `AudioRenditions` and a music `Queue`; `Started` its `Queue`;
  `Playback.Progress` takes a `Report` (with `At`, `Title`, `Stop`,
  `Late`). `MediaPath` has `MediaASS`, `MediaFont`, `Whole`, `Entry` (a
  queue entry's stream) and `OfCopy`; for `MediaFile` the engine resolves
  the range against the copy's size and writes the headers itself.
  `Limits` gains `Downloads`, `DownloadsPerDay`, `DownloadKbps`,
  `ScansPerDay`. `Capabilities` is `wire.Capabilities`. The remote
  `Track` has no `Label`; `RemotePlayback` has no `Segments` (copies
  carry them) and gains `Purpose`, `Delivery`, `Etag`, `Size`,
  `CopyChanged`, `BurnedTrack`, `Fonts`, `Queue`.
- Media answers `HEAD` and other methods with `405` and `Allow: GET`;
  the file by byte ranges with its exact size and `ETag`; playlists only
  complete, their renditions the copy's audio tracks.
- `titles` pages are bounded by size too (5,000 titles, 3 MiB), `next`
  set whenever titles remain; copies are cleaned before they leave (names
  and track titles only when they are labels).

### Added

- Extras (`VideoTitle.ExtraType`, `Name`), sent only with their title,
  and gone with it; the `tvdb` and `local` keys; a local title's
  description (`Name`, `Year`, `Image`).
- `SetLocal` and `SetGroupLocal` (`PeerView.Local`, `GroupView.Local`):
  local titles shared, per pairing and per group, off by default.
- Downloads and scans (`PurposeDownload`, `PurposeScan`): a download's
  ticket of 24 hours without the idle rule, its reads paced at
  `downloadKbps`; a scan's of 10 minutes, its reads within 32 MiB.
  `ReportOffline` sends a download's late reports; `Restart` reads a file
  on under a new playback of the same etag (`ErrCopyChanged` otherwise);
  `PlayingTrack` names the queued track playing. `ErrDeliveryRefused`.
- The `previews` call (`Previews`, sheets fetched before a first play)
  and the `lyrics` call (`TrackLyrics`; a host's `Lyrics` gives them).
- The `image` call: a host's `Images` gives a local title's image,
  served under a ticket of its own; `TitleImage` asks a peer for one,
  checked and re-encoded. `OpaqueKeyPart` makes opaque parts of keys.

## v0.7.1 (2026-11-21)

### Added

- `CodeText`: a live code's text again, to copy or send once more, built
  with this server's address and key now.


## v0.7.0 (2026-11-21)

### Added

- What this server shows from a group is one choice for the whole group:
  everything its members offer it, but the libraries hidden with
  `SetGroupShows`; `GroupShows` lists each member's libraries and whether
  they're shown. What is read of a server is what it is shown
  individually (`SetWants`, while the individual relation holds) plus what
  its groups show of it.

- `PeerView.JoiningGroup`: a server waiting with one of our group's
  invites is a request to join that group, for the panel to show there.
- `HostLibraries`: this server's libraries, for a panel creating or
  joining a group with what it shares.

### Changed

- Automatically accepting new members (`SetGroupAnswer` true) accepts a
  request as soon as it arrives: the group's `waitDays` changes nothing
  (groups section 4.1). Requests that waited before it was turned on keep
  waiting for the operator.
- `SetWants` no longer counts for a server known only through a group:
  what its groups show does.


## v0.6.0 (2026-11-21)

Relations (MSFP core section 7.7, proposal 14): the operator manages why
two servers are linked, not the link. A link lasts while one relation
holds it: the individual relation, which both sides want, or a group.

### Added

- `individual` in `hello`: whether this side wants the individual
  relation. `AskIndividual` asks a peer linked through a group for it, or
  accepts its ask, or keeps a link a dissolution left to the operator;
  `EndIndividual` ends it or withdraws the ask; `RefuseIndividual` refuses
  an ask, privately. `PeerView` says `Individual`, `IndividualAsked`,
  `IndividualAsks` and `KeepChoice`; `RelationsChanged` tells the panel.
- `BlockPeer`: ends a link whatever holds it, and keeps the peer away from
  every group.

### Changed

- A plain code gives the individual relation; a group's code gives the
  group relation only, so the inviter is no longer an individual
  pairing of the newcomer's.
- `LeaveGroup(ctx, id, ask)`: the links only the group held end, but
  those in `ask`, asked for the individual relation first, which stay
  until their operators answer. They no longer become pairings by hand.
  Another member's `left` ends such a link on this side too.
- `keepPairings` on a dissolution leaves each link only the group held
  to each side's operator (`KeepChoice`), instead of making it a pairing
  by hand.
- Only a block keeps a server away from groups. `End` still records the
  keys for the signed 404, but a server ended so can be linked again
  through a group. Keys forgotten before this version aren't marked
  blocked: a server blocked before is no longer kept away from groups.
- Offers and limits of the individual relation count only while both
  sides want it.
- `ByHand` in `PeerView` is now `Individual`, under its former name.

### Example

- `individual`, `end-individual` and `block`.


## v0.5.0 (2026-11-21)

### Added

- "No way in" (groups section 6). `GroupView.NoWayIn` says when this
  server has no pairing through a group that has other members. Nothing
  reaches those members from here, and nothing reaches here from them.
  `GroupsCutBy(peer)` lists the groups that ending a pairing would cut
  off, so a panel can say so before it ends.
- A member back in (groups section 2). A member with no pairing through a
  group to a code's maker can use a new code for the group: by `pair`, or
  by `group-join` when the two are paired some other way. Nobody signs
  `joined`; once the maker accepts, the pairing gains the group, and
  events and mail flow again.

### Fixed

- An owner event replayed without counting didn't take its `ownerSeq`, so
  every later owner event waited forever and the owner could no longer
  act in the group. It happened when, for example, the owner named an
  admin who was leaving at the same moment, with the `left` replayed
  first. It now takes its `ownerSeq` as any replayed event takes its `seq`
  (groups section 3.5).

### Changed

- A request mail is refused with `ErrNoWayIn` when this server has no
  pairing through the group, where before it was kept but could never
  leave. `RequestMember` returns that error, and any other error from its
  request, instead of only logging it.

### Example

- `groups` says when a group has no way in.

## v0.4.1 (2026-11-21)

### Fixed

- After a stolen key, a member's group events could stay missing on some
  members for up to 10 minutes (until the next `group-sync`), and every
  event after them with them: the `revoked` and `rotated` events were
  pushed while the peers' `revoke` and `rotate` calls were on their way,
  and a peer that still held this server suspended refused them. Now, as
  soon as a peer moves to this server's new key, the engine runs
  `group-sync` with it for each group they share, which pushes what it
  lacks. The same covers a plain rotation.

### Example

- `admin <group> <member>`, `ban <group> <member>` and `dissolve <group>`.

## v0.4.0 (2026-11-20)

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
`SetGroupOffers`, `GroupOffers`, `SetGroupPool`, `SetAdmin`, `SetHeir`, `GiveGroup`,
`SetGroupSettings`, `Ban`, `Unban`, `LeaveGroup`, `DissolveGroup`,
`Block`. The calls `group-sync`, `group-push` and `group-pair`; the part
`groups`. A store keeps groups by implementing `GroupsTx` (the shipped
stores do). A link's offers and limits are what it has by hand and what
each of its groups gives it; a playback counts against the first pool
with room. `PeerRecord` gains `Groups`, `NotByHand`, `GroupOffers`,
`Pools` and `JoiningGroup`; `CodeRecord`, `Uses` and `Group`.
`PeerView` gains `Groups` and `ByHand`; `CodeView`, `Uses` and `Group`;
`CodePreview`, `Group` and `GroupName`. Errors `CodeOptions`,
`NoGroups`, `NoRight`, `NotMember`. Events `GroupRequestWaiting`,
`GroupJoined`, `GroupChanged`, `GroupLeft`. `GroupMemberView.Refused` and
`Asked`; `GroupBanView` with the banned server's `Name` and `Fingerprint`
(`wire.GroupState.WasBanned`).

A server already paired with a group code's maker joins through the
pairing (MSFP groups section 8.4, `group-join`): `RedeemCode` calls it
instead of `pair`, `ReadCode` no longer refuses such a code, and the
maker's operator answers with `AcceptJoin` or `RefuseJoin`
(`GroupView.Joins`, event `GroupJoinWaiting`). Unanswered, it is dropped
after 30 days on both sides; `LeaveGroup` gives up earlier. Error
`AlreadyMember`. `wire.JoinProof`, `wire.CheckJoinProof`.

A group's state is the same for every copy that kept the same events,
whatever order they arrived in (MSFP groups sections 3.2 and 3.5):
`wire.ComputeGroup` replays them all, `wire.CheckEvent` is what a copy
checks before keeping one, `wire.Verdict` and `wire.EventResult` say
whether each counts. `GroupState.Apply` is gone; `GroupState.Highest`
gives the seq a member signs next. Events that don't count are kept and
passed on too; `GroupRecord.Held` is no longer written (its events are
read as kept ones). `wire.ErrHeld` and `wire.ErrKept` are gone. Rotation
and revocation statements in group events are read by their exact member
names.


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
