# Security

## Reporting a vulnerability

Please report vulnerabilities privately, through GitHub's private
vulnerability reporting: the **Security** tab of this repository, then
**Report a vulnerability**. Don't open a public issue for one.

Say what is affected (a version or a commit), what an attacker can do,
and how to reproduce it if you can. You will get an answer in the
report's thread.

A flaw in the protocol itself, which any implementation would have,
belongs to [msfp-io/msfp-spec](https://github.com/msfp-io/msfp-spec):
report it there the same way.

## What a host should know

The engine checks every call and everything a peer's media brings
before the host sees it, but some of what crosses still reaches the
host's people's devices, or the host's own disks and bandwidth. With
version 0 as revised (proposals 16 and 17):

- **Fonts and ASS from peers.** A peer's ASS subtitles are rebuilt, not
  filtered: known sections, lines and override tags only, embedded fonts
  and pictures dropped, sizes and counts capped. Its fonts are checked
  as TrueType or OpenType files (tables within the file, no
  collections, no WOFF, at most 10 MiB). A checked font is still a
  font: font parsers have had flaws. A host that renders ASS with a
  peer's fonts should do so where a renderer's flaw does least harm
  (the viewer's own device, a sandboxed process), and keep its renderer
  up to date. A host that doesn't want them doesn't fetch them: the
  subtitles play with its own fonts.
- **Images and preview sheets.** A peer's preview sheets (a JPEG within
  the layout) and a local title's image (a JPEG or a PNG within its
  bounds) are checked before they are decoded, then re-encoded, so that
  only their pixels go further. Decoding still runs Go's image decoders
  on a peer's bytes. The sheets a host gives are re-encoded before they
  leave too.
- **Local titles' images.** A local title's image is the owner's
  choice of a picture, shown as the owner's. It may show anything the
  owner chooses: a host shows it as the peer's, never as its own or a
  public database's. Local titles are off until the operator shares
  them, per pairing and per group: they may be personal recordings.
- **Downloads.** A download lets a peer keep a whole copy, for as long
  as it likes: its ticket lives 24 hours without the idle rule, its
  speed capped by `downloadKbps`. Downloads are off until the operator
  allows them (`Limits.Downloads`); a peer that downloads has the file,
  and ending the pairing doesn't take it back. A download's late
  reports are kept in the store for 30 days, with the peer and the
  title.
- **Scan access.** A scan lets a peer's media server read up to the
  pool's `Limits.ScanBytes` (4 MiB unless the operator sets more, at
  most 1 GiB), which the scan's answer states, of a copy by ranges, to list it, for 10 minutes. It is off
  until the operator allows it (`Limits.ScansPerDay`). The engine counts the bytes
  sent under each scan ticket and never passes the cap; scans count
  against no stream, so a host that meters its bandwidth counts them
  too.
- **URLs built, never followed.** Answers carry no URL: the viewer's
  side builds every media URL from the peer's stream root, the answer's
  ticket and a fixed name, so a peer can't point it anywhere else. The
  owning side serves exactly those names, and a playlist's names only
  once a playlist it served gave them.
- **Nothing that needs guidance to be safe.** A copy carries no name,
  its tracks no title, an extra no name, a copy no date: what a media
  server fills from file names never leaves. A local title's `name`
  stays (nothing else names it), behind the local-titles switch.
- **Previews on demand.** A viewer's side fetches a copy's sheets only
  when someone opens it (`PreviewSheet`), never for a whole library: a
  previews call outside a playback tells the owner that someone on that
  server opened that copy, and the sheets fetched roughly where.
- **HEAD on media URLs.** Every media URL answers `HEAD` and other
  methods with `405`, whatever its ticket: a host's own tickets on the
  same route included. A host that answers its own clients' `HEAD`
  does so on a route of its own.

## Supported versions

Only the latest release gets fixes while the module is before 1.0.
