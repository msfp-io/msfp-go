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
  version 0, a draft until version 1 ([spec/v0](https://github.com/msfp-io/msfp-spec/tree/main/spec/v0)).
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
| `github.com/msfp-io/msfp-go/store/jsonfile` | a ready store: the engine's records in JSON files, sealed with a key file |
| `github.com/msfp-io/msfp-go/store/memory` | a ready store in memory, for tests |
| `examples/share-folder` | a whole host in about 400 lines |

The only dependency beyond the standard library is
[filippo.io/edwards25519](https://pkg.go.dev/filippo.io/edwards25519).

## Install

```sh
go get github.com/msfp-io/msfp-go@v0.5.0
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

To share titles, give `Config` a `Catalog` (your libraries and titles), a
`Playback` (starting and stopping a stream) and a `Media` (serving it). To
play a peer's titles, call `Play`, `Progress`, `Stop` and `Fetch` from a
route of your own. `Config.Notifier` tells your server what happened, for
its panel and logs. The package documentation and
[host.md](https://github.com/msfp-io/msfp-spec/blob/main/api/host.md) have
the details.

## The example: two servers that pair

[examples/share-folder](examples/share-folder) is a small host: its records
in JSON files, a folder of films shared with the servers it pairs with,
typed commands in place of a panel. It makes a self-signed certificate at
each start and pins it.

Run two of them on one machine, in two terminals:

```sh
cd examples/share-folder
go run . -address https://127.0.0.1:8441 -listen :8441 -data a -films ~/films -loopback
go run . -address https://127.0.0.1:8442 -listen :8442 -data b -loopback
```

`-loopback` lets peers on the same machine be called (the engine refuses
local addresses otherwise). Then:

1. type `code` in the first, and `redeem <code>` in the second;
2. compare the fingerprints both print, and type `accept <id>` in the first;
3. `offer <id>` in the first shares its films; `want <id>` then
   `titles <id>` in the second lists them;
4. `play <id> <key>` in the second prints a URL that `curl -k` or
   `ffplay -tls_verify 0` plays through it; `pause <playback>` and
   `resume <playback>` say the viewer paused or resumed, and `stop
   <playback>` ends it. Until then the example reports the playback to
   the first every minute, as a host must: a peer ends a playback it
   hears nothing of for 2 minutes.

`peers` lists the pairings and `end <id>` ends one. Groups work the same
way: `group-create <name>` in the first, `invite <group>` for a code the
others redeem, `groups` to see the members and the requests waiting, and
`pair <group> <mail>` to accept one. The comment at the top of
[main.go](examples/share-folder/main.go) lists every command (key
rotation, a stolen key, moving address, a server that can't be called,
groups).

## Versions

The module follows [semantic versioning](https://semver.org). Its
major.minor version is shared with the other MSFP engines (msfp-dotnet, to
come): msfp-go 0.2.x and msfp-dotnet 0.2.x follow the same API design
(the spec's api/). Patch versions are each library's own.

The module's version is separate from the protocol's: from v0.4.0 it
speaks MSFP version 0, groups included; v0.3.0 and before speak the
same protocol numbered 1, and the two don't talk. Before 1.0, the API may change between minor versions.

## Testing

```sh
go test ./...
go test -race -timeout 30m ./...
```

The engine's tests run whole servers against each other in one process.
The protocol's test vectors under [wire/testdata](wire/testdata) come
from the spec's
[vectors/](https://github.com/msfp-io/msfp-spec/tree/main/vectors),
copied as they are: `wire/testdata/v0/` is the spec's `vectors/v0/`, and
`wire/testdata/test-draft-strict.json` its
`vectors/history/test-draft-strict.json`. To refresh
them, copy those files from a checkout of msfp-spec and run the tests:

```sh
cp ../msfp-spec/vectors/v0/*.json wire/testdata/v0/
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
