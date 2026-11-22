# Changelog

## Unreleased: the revision of version 0 (proposals 16 and 17)

MSFP version 0 is revised, and stays a draft until version 1: titles
carry their copies, plays ask a delivery and a purpose (downloads and
scans), extras and local titles are shared, and a copy's file is served
by byte ranges. Proposal 17 (neutrality), as frozen on 2026-11-22
(msfp-spec a6865a7), then makes the protocol carry facts in outside
vocabularies (IANA media types, RFC 6381, ITU-T H.273, the DASH roles,
MusicBrainz's types) and MSFP's registry, with open lists; drops every
field that is only safe through guidance, said twice, or that the
receiver can work out; builds every media URL from a ticket; and leaves
the choices to the viewer's server: no capabilities, a defined
baseline. Servers on the draft before (v0.7.1 and before) and on the
revised one don't pair with each other: move every server at once. The
migration guide at the end of this section says what hosts change; its
last part is proposal 17's, and wins where the parts before name a
member it removed.

### Changed by proposal 17

- One rule for unknown values (`wire.Token`, `wire.IsMediaType`, MSFP
  core section 8.5): open fields take tokens, or media types, kept as
  they are when unknown; an optional malformed member is dropped alone,
  a required one drops its holder. No list of names in the package: the
  registry (spec/registry.md) is the authority.
- Copies (`wire.Copy`): `MediaType` (the file's media type) replaces
  `Container`; codec and subtitle tokens are the registry's (ffprobe's
  `codec_name`); `Codecs` (RFC 6381, `+` allowed, no `/`) is required
  for h264, hevc, av1, vp9, aac and dts (`wire.RequiresCodecs`) and read
  by `wire.ReadCodecs` (profile, tier, level, chroma, constraints, AAC's
  object type, the Dolby Vision string rebuilt from the base 4CC and
  `dv`; the parts consumers ignore left out); `Colour` is H.273 code
  points (`*int`, 2 never kept) with `FullRange`; `HDR` holds
  enhancement layers only (`hdr10plus`); `VideoDV` loses `BL` and
  `RPU`; new `RefFrames` (h264), `Objects` (`dolby-atmos`, `dts-x`),
  subtitle `Role`, `EndMs`, `PreviewLayout`; `Parts` is a count; track
  IDs are the stream's index in the file. Gone: `Name`, `BitrateKbps`
  (the overall rate is `Size` × 8 / `DurationMs`), `Added`, `Previews`,
  `CopyPart`, a track's `Title`, `Profile`, `Layout`, `FileIndex`,
  `CodecTag`, `Level`, a segment's `Final`, `wire.SortCopies`,
  `BetterCopy`, `PlainLabel`, `MaxCopyName`.
- Titles carry no kind on the wire (their key names it), libraries no
  kind (`Library.Kind`, `WantedLibrary.Kind`, `ShownLibrary.Kind`,
  `wire.LibraryKind` gone), extras no name; `ids` are sent only on
  episodes and on titles keyed under `tvdb` (`wire.VideoOtherID`).
- Music: copies take `MediaType` (no `Name`, `Added`, `BitrateKbps`),
  up to 16; no `SortName`; `ids` only on `mb` titles, `isrc` and
  `acoustid` gone; a track's artists, names, dates and genres leave only
  when they differ from its album's. Lyrics: no `Synced`, `Offset`,
  `DisplayArtist`, `DisplayTitle` (`wire.Lyrics.Synced()` tells it from
  the lines).
- play's request: `MediaType` (`video/mp2t`, `video/mp4`, `audio/…` for
  a copy with no video) replaces `Container`; transcode targets take
  `copy` (`wire.CodecCopy`) and a `Range` (`sdr`, `keep`); no
  `AudioLanguage`, `SubtitleLanguage`, `SubtitleTrack`; queue entries
  are objects with their own `VersionID` and delivery
  (`msfp.QueueEntry`, `wire.QueueAsk`). Progress carries no `paused`.
- The owning side no longer chooses: the copy is the one `versionId`
  names, else the first; the audio the one `audioTrack` names, else the
  file's default (`wire.PickCopy`, `wire.DefaultAudio`). `wire.Serve`
  decides what is served (the defined baseline, H.264 High 4.1 8-bit SDR
  BT.709 and AAC-LC stereo 48 kHz, each stream within it left
  unchanged; remux segment formats; copied streams; ranges) and is
  passed to the host (`StartRequest.Served`, `QueuedCopy.Served`).
- play's answer carries a `ticket`, `protocol`, `subtitles` (`id`,
  `form`, `ass`), `fontCount`, `scanBytes` (scans) and no URL, track,
  length or trickplay: the viewer's side builds every URL from the
  peer's stream root and the ticket (`wire.StreamURL`, `SubtitleURL`,
  `FontURL`, `SheetURL`, `QueueURL`, `ImageURL`), never fetching an
  address a peer wrote. The owning side serves exactly those names
  (playlists' names only once a playlist gave them) and `404`s past the
  counts. `previews` answers `{ticket, etag}`, `image` `{ticket}`.
- Previews: the layout is the catalog's (`wire.PreviewLayout`,
  `Copy.PreviewLayout`); `CopyInfo.Trickplay` and `msfp.Trickplay` are
  gone; sheets are fetched on demand (`Engine.PreviewSheet`) under the
  playback's ticket or a previews ticket, cached by etag and layout;
  `Engine.Previews`, which fetched every sheet ahead, is gone.
- `RemotePlayback`: `Subtitles`, `Fonts`, `Sheets`, `ScanBytes`; its
  `DurationMs` and `MimeType` come from the catalog's copy. Gone:
  `AudioTracks`, `SubtitleTracks`, `SubtitleTrack`, `ForcedTracks`,
  `ForcedTrack`, `BurnedTrack`, `SubtitleStretchSeconds` (it is
  `wire.StretchSeconds`), `Trickplay`, and the `Track` type.
- Limits: no `scanBytes`; a pool's `Limits.ScanBytes` is its own scan
  cap, stated in each scan's answer (`wire.ScanCap`).
- Groups: no name in a mail nor in `group-pair`: a member's name is its
  profile's.
- The file is served and relayed with the copy's own `mediaType`
  (`wire.RelayedFileType`); `Started.MimeType` and
  `StartedEntry.MimeType` are gone.
- The master playlist: the engine fills `CODECS`, `RESOLUTION`,
  `VIDEO-RANGE`, `SUPPLEMENTAL-CODECS`, `BANDWIDTH` and `CHANNELS
  "16/JOC"` that the copy's facts tell when a stream is served
  unchanged, and logs the required ones still missing.

### Removed by proposal 17

- `Config.ChooseCopy`, `ChooseTracks`, `CopyName`, `TrackTitle` and
  their defaults, `wire.ChooseCopy`, `ChooseAudio`, `PlayCopy`,
  `RemuxContainers`, `TranscodeTargets.WithBaseline`, `wire.StreamBase`,
  `ReadTrickplay`, `Previews`, `Trickplay`, `Limits.ScanCap`.

### Added by proposal 17

- `wire.ReadCodecs`, `Codecs`, `RequiresCodecs`, `PlayableAsIs`,
  `CodecsString`; `wire.IsMediaType`, `SameContainer`,
  `PairedSubtypes`; `wire.Serve`, `Served`, `Encoders.Ranges`,
  `Encoders.Segments`, `DefaultSegments`, `Encoders.Carries`,
  `Encoders.CheckCodecs`, `InBaselineVideo`, `InBaselineAudio`;
  `wire.Asked`, `ServedSubtitle`, `ServedForms`, `PlayAnswer.URLs`;
  `wire.PreviewsAnswer`; `wire.QueueAsk.Request`, `MusicRemuxTypes`;
  `wire.MusicCopy.AsCopy`; `CopyInfo.Forms` (the form of a track whose
  format video section 5.4 doesn't list); `Engine.PreviewSheet`.

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
- The owning side re-encodes the host's preview sheets before they
  leave, checked against the layout (one outside it isn't sent); serves
  each playlist the same on every fetch under a ticket (the first kept);
  answers `HEAD` and other methods with `405` on every media URL form,
  whatever the ticket (live, ended or never given: the fallback of
  `StreamHandler` hears `GET` only there).
- A scan's cap counts the bytes sent: a range is held against it while
  it is served, and what wasn't sent is given back.
- A download's pace follows `Config.Clock`, and its timers when it has
  some (`Timers`).
- Example: one library, `folder`, of films and series (`-films` is now
  `-folder`); extras, local titles, previews, copies described with
  ffprobe; commands `local`, `group-local`, `limits`, `download`,
  `late`, `previews`.

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
- `DownloadsTx` and `DownloadRecord`: downloads kept in the store for
  their late reports, both sides, so a restart keeps them; the shipped
  stores implement it (a `state.json` from before reads as none kept).
- `Timers`: what a test's `Clock` implements so that the engine's waits
  follow it.

### Migration guide

**A host that shares (the owning side):**

1. `Catalog.Titles` gives each film, episode and extra its copies
   (`VideoTitle.Copies`, `wire.Copy`; a music track's `MusicCopy`), best
   first: the copy's ID is your handle (it never leaves), its
   `DurationMs`, `Container`, exact `Size`, an `ETag` that changes when
   and only when its bytes change, its `AudioTracks` (may be empty), and
   what else your media server knows. Series and seasons carry none.
   Remove `Catalog.Copies`.
2. `Playback.Describe` gives only `CopyInfo`: the previews' layout, the
   fonts of ASS tracks, the tracks you can't serve apart.
3. `Playback.Start` reads `Purpose`, `Delivery`, `Container`,
   `Transcode`, `AudioRenditions` and a music `Queue` from the
   `StartRequest`; it answers `ErrDeliveryRefused` for a delivery it
   won't make, and fills `Started.Queue` for a queue.
4. `Playback.Progress` takes a `Report`: `At`, `Title`, `Stop`, and
   `Late` for a download's late report after its playback ended.
5. `Media.Serve`: for `MediaFile` the engine gives the exact range and
   writes the headers itself; serve the bytes. New paths: `MediaASS`,
   `MediaFont`, `Whole` (a whole text track), `Entry` (a queue entry's
   stream), and `OfCopy` (a sheet under a previews ticket: the handle is
   the copy's). Playlists are complete (`VOD`, `#EXT-X-ENDLIST`), their
   audio renditions named by the copy's track IDs. Sheets are JPEGs
   within the layout; the engine re-encodes them.
6. Extras: titles of kind `extra`, keyed `<film, series or season
   key>/x/<opaque>`, with `ExtraType` and an optional `Name` (theme
   songs and videos too, since proposal 17).
7. Local titles: `movie/local/<opaque>`, `series/local/<opaque>`, with
   `Name`, `Year` and `Image`; make `<opaque>` with `OpaqueKeyPart`.
   Implement `Images` to give their images. The operator shares them per
   pairing (`SetLocal`) and per group (`SetGroupLocal`); show the
   switches in your panel, off by default.
8. Downloads and scans: `Limits` gains `Downloads`, `DownloadsPerDay`,
   `DownloadKbps` and `ScansPerDay`, all 0 (none) by default: show them
   in your panel.
9. A store of your own implements `DownloadsTx`, or downloads' late
   reports don't survive a restart.
10. Your own clients' `HEAD` on media: answer it on a route of your own;
    the engine answers `405` on its stream route, your tickets there
    included.

**A host that plays peers' titles (the viewer's side):**

1. Read copies from the titles kept (`BestCopy`, `AudioLanguages`,
   `SubtitleLanguages` replace `Audio`, `Subtitles`, `Pictures`,
   `Video`, `Picture`, `BestPicture`); build your library from them,
   never by reading a peer's file.
2. `PlayRequest` gains `Purpose`, `Delivery`, `Container`, `Transcode`,
   `AudioRenditions` and `Queue`; on `ErrDeliveryRefused`, ask again
   with another delivery, or none.
3. `RemotePlayback` gains `Purpose`, `Delivery`, `Etag`, `Size`,
   `CopyChanged`, `BurnedTrack`, `Fonts` and `Queue`; `Segments` is gone
   (the copy carries them), and so is a `Track`'s `Label`.
4. To present a copy as a file of your own: answer your clients' `HEAD`
   from the catalog (its size and etag); when a read fails, `Restart`
   reads on under a new playback of the same etag (`ErrCopyChanged`
   otherwise).
5. Downloads: send their late reports with `ReportOffline`, for 30 days.
   Previews before a first play: `Previews`; lyrics: `TrackLyrics`;
   local titles' images: `TitleImage`, shown as the owner's. Music:
   `PlayingTrack` names the queued track playing.

**Proposal 17, a host that shares:**

1. Describe each copy in outside vocabularies, as facts: `MediaType`
   read from the file's first bytes (the ISO BMFF brand table of the
   registry, the EBML DocType, the TS packet size; `audio/…` when there
   is no picture but cover art), the registry's codec and subtitle
   tokens (ffprobe's `codec_name`), `Codecs` for h264, hevc, av1, vp9,
   aac and dts at least (built from your media server's profile and
   level or the file's records: examples/share-folder/probe.go does it
   from ffprobe), `Colour` as H.273 numbers with `FullRange` (the
   implementers' notes map ffmpeg's names), `DV` whenever there is Dolby
   Vision, `HDR` only `hdr10plus`, `Objects`, `RefFrames`, exact
   values; track IDs `a<n>`/`s<n>` with n the stream's index. Drop
   names, titles, bitrates, dates, profiles, layouts and file indexes.
2. `ETag` changes when and only when the bytes change.
3. Put the previews' layout on the copy (`PreviewLayout`), not in
   `CopyInfo`; a change of it is a change of the title.
4. The first copy is the one played when a peer names none; the audio
   the file's default when it names none. There are no choice or name
   hooks.
5. `Playback.Start`: read `MediaType` (a remux's segment format,
   `Served.Segments`), `Transcode` (`copy` streams left unchanged, a
   `Range` to make) and `Served` (what the engine decided, the
   baseline's streams left unchanged when within it); no
   `SubtitleTrack`. Queue entries carry their own delivery. Set
   `Config.Encoders` (codecs, ranges, the codecs your segment formats
   carry). In fMP4 segments write HEVC `hvc1`, Dolby Vision `dvh1`
   (`dav1` on AV1), parameter sets out of band; give each master
   playlist variant `BANDWIDTH`, `CODECS` and, with video,
   `RESOLUTION`, plus `VIDEO-RANGE` and `SUPPLEMENTAL-CODECS` where
   they apply. Drop `Started.MimeType`.
6. Serve every subtitle track of a format video section 5.4 lists in
   its form; list in `NotApart` those you can't, and don't list in the
   catalog an external track you can't serve apart; give `Forms` for
   other formats you serve apart.
7. A pool's scan cap is `Limits.ScanBytes` (32 MiB suggested); it is
   no longer sent in limits.
8. Drop `Library.Kind` and extras' `Name`.

**Proposal 17, a host that plays peers' titles:**

1. Choose the copy (`VersionID`), the audio (`AudioTrack`) and the
   delivery from the copy's facts and your device: a copy whose media
   type, codec or 3D layout you don't know, or whose `Codecs` can't be
   read (`wire.PlayableAsIs`, `wire.ReadCodecs`), is one you can't play
   as it is: ask a `remux` (`MediaType` `video/mp2t` or `video/mp4`), a
   `transcode` (registry tokens, `copy` for a stream your device plays,
   `Range` `keep` for HDR), or nothing for the baseline.
2. Choose the subtitles yourself, among the copy's tracks and
   `RemotePlayback.Subtitles` (served apart); name one only to burn it
   in.
3. Label copies and tracks from their facts (height, range, edition,
   codecs; role, flags, channels, objects); show "recently added" from
   the day you first saw a copy; type an empty library by its first
   titles; show an extra by its type and a number.
4. Fetch previews only for a copy someone opened, with
   `PreviewSheet`; never for a whole library.
5. A scan's cap is `RemotePlayback.ScanBytes`.
6. Music queues are `[]QueueEntry`, each with its own copy and
   delivery if you like; a track's absent artists, dates and genres are
   its album's.

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
