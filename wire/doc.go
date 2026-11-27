// Package wire implements MSFP, the Media Server Federation Protocol
// (https://github.com/msfp-io/msfp-spec), version 0 as revised by
// proposals 16, 17 and 19, and the parts of MSFP Media version 0
// (https://github.com/msfp-io/msfp-media) that MSFP carries between
// servers: the language's rules are cited as MSFP Media's, the
// federation's as MSFP's. Of MSFP's core, each
// server's own key and its fingerprint, the one-time pairing code, signed
// requests and answers and their nonces, MSFP's strict JSON, addresses
// (HTTPS only), certificate pins, hello and the versions of the core and
// its parts, key rotation and revocation (their statements, the next key
// commitment), changes of address (the former host accepted, the
// announced address checked), servers that can't be called (their
// address, their hello schedule, the statements their peers' answers
// carry), the one rule for unknown values (Token and IsMediaType: open
// fields keep any well-formed value, the lists being MSFP's registry's
// or an outside vocabulary's); of its catalog, video and music parts,
// title keys and what a server keeps of a peer's titles and copies
// (media types, registry tokens, RFC 6381 codecs strings and what
// ReadCodecs reads from them, H.273 colour); of playback and media,
// play's request, what an owning server serves for it (PickCopy,
// DefaultAudio, Serve: the baseline, remux media types, transcode
// targets with copy and range), play's answer and the URLs built from
// its ticket (no peer-written address is ever fetched), limits, scan
// caps, previews' layout and answer; of media and video, the checks of
// the playlists, WebVTT, ASS, PGS pictures and fonts a server relays
// from another. Of MSFP Media alone, for a host that also serves its own
// players with it: the capability list's forms (PlayReader with
// Capabilities; MSFP refuses the list, and its matching is the host's),
// the titles request with its filters (ReadTitlesRequest, TitleFilter),
// and the reading of refusals (Meaning, ReadPlayRefusal, ReadInstead).
//
// A server proves who it is with its key only, never with a web
// certificate, a name or an address. Two servers learn each other's keys
// when their operators pair them, and from then on every request between
// them is signed by the sender, and every answer by the server answering.
//
// The package is the protocol alone, the toolkit of the module
// github.com/msfp-io/msfp-go: it imports only the standard library
// and filippo.io/edwards25519, never the server that uses it
// (imports_test.go checks this). The protocol's behaviour (storage,
// limits, connections, the calls' handlers) is the engine above it,
// package msfp.
package wire
