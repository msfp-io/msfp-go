<img src="logo.png" alt="" width="128" align="right">

# msfp-go

The MSFP engine for Go. [MSFP](https://github.com/msfp-io/msfp-spec), the
Media Server Federation Protocol, lets media servers share their libraries
with each other: one server's people browse and play titles another server
holds, through their own server. Two operators pair their servers by hand,
and each server trusts only the servers it paired with. Groups make that
easier among friends: a server joins a group with one code, then pairs
with each member who accepts, without codes.

This module runs the protocol inside your media server, the **host**. The
engine signs and checks every call, keeps the pairings, keys, addresses and
limits, reads peers' catalogs, starts and relays playbacks, and does what
time asks (key rotations, address moves, retries). Your server implements
storage, catalog, playback and media through a few interfaces; it writes
none of the protocol itself.

- The protocol: [msfp-io/msfp-spec](https://github.com/msfp-io/msfp-spec),
  version 0 as revised by proposals 16, 17 and 19, still a draft until
  version 1 ([spec/v0](https://github.com/msfp-io/msfp-spec/tree/main/spec/v0)).
  v0.8.0 and later speak the revised draft; v0.7.1 and before speak the
  draft before it, and servers on the two don't pair.
- The media language MSFP carries between servers:
  [MSFP Media](https://github.com/msfp-io/msfp-media) version 0 (titles,
  copies, listing, playing, fetching), split out of MSFP by proposal 19.
  The language's rules (`wire`'s keys, copies, `Serve`, play's request
  and answer, refusals) cite it; the federation's (pairing, limits, what
  is shared, scans) cite MSFP. A host that serves its own players with
  MSFP Media may use `wire`'s readers for them too (`wire.PlayReader`
  with `Capabilities`, `wire.ReadTitlesRequest`); the engine itself only
  speaks MSFP.
  The [changelog](CHANGELOG.md) says what hosts change.
- The engine's API in words: [api/](https://github.com/msfp-io/msfp-spec/tree/main/api)
  in the spec's repository, mainly
  [engine.md](https://github.com/msfp-io/msfp-spec/blob/main/api/engine.md)
  (what the engine offers) and
  [host.md](https://github.com/msfp-io/msfp-spec/blob/main/api/host.md)
  (what the host implements).
- The Go reference: [pkg.go.dev/github.com/msfp-io/msfp-go](https://pkg.go.dev/github.com/msfp-io/msfp-go).

## Packages

| Package | What it is |
|---------|------------|
| `github.com/msfp-io/msfp-go` (package `msfp`) | the engine: `msfp.Open`, its handlers, pairing, catalog, playback |
| `github.com/msfp-io/msfp-go/wire` | the protocol's pieces: keys, pairing codes, signatures, strict JSON, statements, the media checks |
| `github.com/msfp-io/msfp-go/describe` | a media file to a copy (`wire.Copy`) with ffprobe: media type sniffed, codec records, codecs strings, colour, tracks, chapters |
| `github.com/msfp-io/msfp-go/store/jsonfile` | a ready store: the engine's records in JSON files, sealed with a key file |
| `github.com/msfp-io/msfp-go/store/memory` | a ready store in memory, for tests |
| `examples/share-folder` | a whole host in about 1,000 lines |

The only dependency beyond the standard library is
[filippo.io/edwards25519](https://pkg.go.dev/filippo.io/edwards25519).

## Install

```sh
go get github.com/msfp-io/msfp-go@v0.8.0
```

It needs the Go version in [go.mod](go.mod) or later.

## The minimum

A server that only pairs needs two things: its **address** (where peers
call it, `https://` only) and a **store** for the engine's records. Then
it mounts the engine's routes and lets it run:

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/msfp-io/msfp-go"
	"github.com/msfp-io/msfp-go/store/jsonfile"
)

func main() {
	ctx := context.Background()

	store, err := jsonfile.Open("data/msfp") // its records, sealed with a key file
	if err != nil {
		log.Fatal(err)
	}
	eng, err := msfp.Open(ctx, msfp.Config{
		Address: "https://media.example.org", // where peers call it
		Store:   store,
	})
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/api/v1/federation/", eng.FederationHandler())
	mux.Handle("/api/v1/stream/", eng.StreamHandler(nil))
	go eng.Run(ctx) // what time does: rotations, moves, retries

	log.Fatal(http.ListenAndServeTLS(":443", "cert.pem", "key.pem", mux))
}
```

Mount the handlers on the raw path, with no authentication in front and no
compression of their answers. Pairing is then three calls, from your
server's panel:

```go
code, _, err := eng.MakeCode(ctx, "for Alice") // send it to the other operator
peer, err := eng.RedeemCode(ctx, pasted)        // a code the other operator sent
_, err = eng.Accept(ctx, id)                    // once both compared fingerprints
```

To share titles, give `Config` a `Catalog` (your libraries and titles,
each film, episode and extra with its copies described as facts: the
file's media type, the MSFP registry's codec and subtitle tokens,
RFC 6381 codecs strings, H.273 colour, exact values, the layout of its
previews; your copies in your order of preference), a `Playback`
(starting and stopping a stream) and a `Media` (serving it). A copy's
ID in the catalog is your handle of it: name the copy alone, not where
its file is. Each call given a handle for a peer is given the peer and
your libraries offered to it (`PeerRef.Libraries`): resolve the handle
to a file in one of them, `ErrNotFound` when it is in none. The engine
serves a copy's file by byte ranges itself, against the size and etag
the catalog gave, serves exactly the names a playback's answer opened,
and checks or rebuilds what leaves (playlists, subtitles, fonts, preview
sheets re-encoded). It maps no names: a media server that names things
otherwise maps them, and a value MSFP doesn't know is sent and kept as
it is. Your server chooses nothing for a peer: its play names the copy
and the audio, else the first copy and the file's default audio, and
`wire.Serve` says what is served; `Config.Encoders` says what your
transcoder makes (codecs, ranges, what each segment format carries).

To play a peer's titles, call `Play`, `Progress`, `Stop` and `Fetch`
from a route of your own. Your server has every copy's facts in the
catalog and knows its device: it chooses the copy, its audio and its
subtitles, and asks a delivery (the file as it is, a remux to
`video/mp2t` or `video/mp4` segments, a transcode, a stream of which
may be `copy`, to SDR or keeping the range), or none for the baseline
every server plays, H.264 and AAC in HLS; and a purpose (a play, a
download, a scan). Nothing of the device is sent, and every media URL
is built from the peer's ticket: none a peer wrote is fetched.
`wire.ReadCodecs` and `wire.PlayableAsIs` read a copy's codecs strings;
`PreviewSheet` fetches a copy's seek-bar previews when someone opens
it, never ahead.

Two things are off until the operator turns them on: **local titles**
(those no public database knows, `SetLocal` per pairing and
`SetGroupLocal` per group) and **downloads and scans** (`Limits`:
`Downloads`, `DownloadsPerDay`, `DownloadKbps`, `ScansPerDay`, and
`ScanBytes`, a pool's scan cap, stated in each scan's answer: 4 MiB
unless you say more, 32 MiB suggested). Theme songs and theme videos are extras like the others:
whether you offer them is your operator's choice. A
download's late reports count for 30 days, across restarts: the shipped
stores keep downloads (`DownloadsTx`). `Config.Notifier` tells your server what happened, for
its panel and logs. `CodeText` gives a live code's text again, to send
once more. The package documentation and
[host.md](https://github.com/msfp-io/msfp-spec/blob/main/api/host.md) have
the details.

## When a peer's titles can't be read

A reading of a peer's titles that fails (`ReadNow`, the scheduled
readings, `TheirLibraries`, `SetWants`) keeps what was read before, and
says why in `PeerView.Reading`: when it ended, and a `ReadFailure`, one
of a closed set of codes, the same in msfp-dotnet, for your pages to name
in your own words. It never holds an address, a URL, a file name or
anything the peer wrote; the error itself has the detail, for the logs.
It is kept in memory: nil until a reading ends after the start.
`CatalogReadEnded` carries the same code.

| Code | The peer's titles weren't read because |
|------|-----------------------------------------|
| `unreachable` | it didn't answer (no route, refused, a name that doesn't resolve, TLS failing) |
| `timeout` | it, or the reading, took too long |
| `certificate-refused` | its certificate is neither publicly trusted nor one it named |
| `refused-unsigned` | an answer not signed by its key: it doesn't hear this server's key (it ended the pairing, or lost its records), or something else answers at its address |
| `refused` | it refused, signed; `Refusal` is the problem type (`not-found`, `rate-limited`, `server-busy`…, MSFP core section 8.2) |
| `not-paired` | it isn't paired with this server, or must pair again |
| `suspended` | it revoked its key |
| `uncallable` | it can't be called |
| `no-common-version` | the two have no core version in common (`PeerView.Core` is -1) |
| `catalog-off` | the catalog part isn't on between the two |
| `older-draft` | it runs an engine of the draft before this revision of version 0 (v0.7.1 and before): it must be updated |
| `answer-not-understood` | an answer that couldn't be read (coded, too large, not JSON, not of the call's form, or breaking the catalog's rules) |
| `internal` | this server's own failure (its store, say) |

Both drafts announce core 0 with the same parts, so the hello can't tell
them apart; the reading does. `older-draft` needs three marks at once:
every library of the libraries answer carries a `kind`, every title of a
page carries a `kind` matching its key (the old draft's required
members, gone from this one), and that page holds a film, an episode or
a track, none with `copies` (required now, unknown then). A title
without copies alone is an owner's mistake, skipped as before. While a
peer's libraries carry kinds, its readings are whole lists, whose titles
tell; this draft's libraries carry none, so it costs them nothing.

## Describing files

A host lists each file as a copy: facts in outside vocabularies (MSFP
Media video 3.2 to 3.6). The `describe` package makes one from a
local file with ffprobe:

```go
var p describe.Prober // ffprobe from PATH; Frames: true adds the HDR frame read
c, probe, err := p.Describe(ctx, path, nil) // external subtitles, numbered above the file's streams
if err != nil {
	// describe.ErrUnknownType: not a media file it knows (a playlist renamed .mkv…), never probed
	return err
}
c.ID, c.ETag = versionID, etag // the host's: a stable ID, an identity that changes with the bytes
raw, _ := probe.Raw(folder)    // ffprobe's output without path or attachments, for the host's own uses
```

The file is sniffed first (only media types the package knows are
probed), opened read-only as a regular file and handed to ffprobe as it
was checked, with the sniffed demuxer, the file protocol only, and
bounds on probing, time and output. Its codec records (avcC, hvcC,
av1C, Matroska's CodecPrivate, Annex B parameter sets) give the codecs
strings; attachments and cover art are never read. Which folders a host
may read is its own policy.

## The example: two servers that pair

[examples/share-folder](examples/share-folder) is a small host: its records
in JSON files, a folder of films and series shared with the servers it
pairs with, typed commands in place of a panel. It makes a self-signed
certificate at each start and pins it. The folder holds films and series
named with their TMDB (or TheTVDB) IDs, their extras in `Trailers/` and
`Extras/`, local titles (no ID, an image beside them), `.vtt` subtitles,
HLS copies and previews made beforehand; copies are described from the
files with the `describe` package, when `ffprobe` is installed. The comment at the top of
[folder.go](examples/share-folder/folder.go) shows the layout.

Run two of them on one machine, in two terminals:

```sh
cd examples/share-folder
go run . -address https://127.0.0.1:8441 -listen :8441 -data a -folder ~/videos -loopback
go run . -address https://127.0.0.1:8442 -listen :8442 -data b -loopback
```

`-loopback` lets peers on the same machine be called (the engine refuses
local addresses otherwise). Then:

1. type `code` in the first, and `redeem <code>` in the second;
2. compare the fingerprints both print, and type `accept <id>` in the first;
3. `offer <id>` in the first shares its folder (`local <id> on` its
   local titles too); `want <id>` then `titles <id>` in the second lists
   them;
4. `play <id> <key>` in the second prints a URL that `curl -k` or
   `ffplay -tls_verify 0` plays through it; `pause <playback>` and
   `resume <playback>` say the viewer paused or resumed, and `stop
   <playback>` ends it. Until then the example reports the playback to
   the first every minute, as a host must: a peer ends a playback it
   hears nothing of for 2 minutes;
5. `limits <id> 2 1 1 3 8000 2` in the first allows the second 2
   streams, 1 transcode, 1 download at once, 3 a day at 8,000 kbit/s,
   and 2 scans a day; `download <id> <key>` in the second then prints the
   URL of the file to save, and `late <download> <ms>` sends a late
   report of it.

`peers` lists the pairings and `end <id>` ends one. Groups work the same
way: `group-create <name>` in the first, `invite <group>` for a code the
others redeem, `groups` to see the members and the requests waiting, and
`pair <group> <mail>` to accept one. The comment at the top of
[main.go](examples/share-folder/main.go) lists every command (key
rotation, a stolen key, moving address, a server that can't be called,
groups, relations, previews).

## Groups and relations

A link between two servers lasts while one relation holds it (MSFP core
section 7.7): the individual one, made by hand with a code, or a group
they are both in. `AskIndividual` asks a peer linked through a group for
the individual relation (or accepts its ask), `EndIndividual` ends it
(the link stays while a group holds it), and `BlockPeer` ends the link
and keeps the peer away from every group. What this server shows from a
group is one choice for the whole group: `GroupShows` lists each
member's libraries and `SetGroupShows` hides some. In the example:
`individual <id>`, `end-individual <id>` and `block <id>`.

## Versions

The module follows [semantic versioning](https://semver.org). Its
major.minor version is shared with the other MSFP engine,
[msfp-dotnet](https://github.com/msfp-io/msfp-dotnet): msfp-go 0.8.x and
msfp-dotnet 0.8.x-preview follow the same API design (the spec's api/). Patch versions are each library's own.

The module's version is separate from the protocol's: from v0.4.0 it
speaks MSFP version 0, groups included; v0.3.0 and before speak the
same protocol numbered 1, and the two don't talk. From v0.8.0 it
speaks version 0 as revised (proposals 16 and 17), which doesn't talk with v0.7.1
and before either. Before 1.0, the API may change between minor versions.

## Testing

```sh
go test ./...
go test -race -timeout 30m ./...
```

The engine's tests run whole servers against each other in one process.
The test vectors under [wire/testdata](wire/testdata) come from the
specs' `vectors/`, copied as they are: `wire/testdata/msfp/v0/` is
[msfp-spec's](https://github.com/msfp-io/msfp-spec/tree/main/vectors)
`vectors/v0/` (MSFP's own cases), `wire/testdata/media/v0/` is
[msfp-media's](https://github.com/msfp-io/msfp-media/tree/main/vectors)
`vectors/v0/` (MSFP Media's), and `wire/testdata/test-draft-strict.json`
msfp-spec's `vectors/history/test-draft-strict.json`. To refresh them,
copy those files from checkouts of both and run the tests:

```sh
cp ../msfp-spec/vectors/v0/*.json wire/testdata/msfp/v0/
cp ../msfp-media/vectors/v0/*.json wire/testdata/media/v0/
cp ../msfp-spec/vectors/history/test-draft-strict.json wire/testdata/
go test ./...
```

CI runs gofmt, `go vet`, staticcheck and the tests, with and without the
race detector.

## Security

See [SECURITY.md](SECURITY.md) to report a vulnerability.

## How it's made

msfp-go is designed by its maintainer, who makes the architecture, security
and product decisions, and written by Claude (Anthropic's AI); Claude's
commits carry a `Co-Authored-By: Claude` trailer. The code is checked by
its tests and linters; it hasn't had an independent security review yet.

## License

[MIT](LICENSE).
