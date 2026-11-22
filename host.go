package msfp

import (
	"context"
	"io"
	"net"
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// What the host implements for the engine: its storage, its catalog, its
// playback and media, sealing, and optionally a clock, a dialer, a
// notifier and a sheet encoder. Only Store is required: the shipped
// stores (store/memory, store/jsonfile) bring their own
// Sealer, and everything else has a default or may be left out.
//
// A host that leaves out Catalog, Playback and Media offers nothing and
// plays nothing here: it only watches what its peers share.

// Store keeps the engine's records. The engine owns the meaning of
// every field; the store keeps them and gives them back as they were.
//
// Read gives fn a consistent view. Update runs fn in a transaction:
// everything fn wrote is kept, or nothing (when fn returns an error, or
// the commit fails). Two Updates never interleave their effects: one at
// a time is enough. The engine never calls a peer, nor the host's media,
// inside fn, and keeps fn short. The Tx is valid only inside fn.
//
// Any error a store returns fails the engine's action; toward a peer, it
// is a signed 500. Lookups that find nothing return nil (or false), not
// an error.
type Store interface {
	Read(ctx context.Context, fn func(Tx) error) error
	Update(ctx context.Context, fn func(Tx) error) error
}

// Tx is what the engine reads and writes inside a transaction. Getters
// return copies: changing what they return changes nothing until it is
// put back.
type Tx interface {
	// Server is the one server record; nil on the very first start.
	Server() (*ServerRecord, error)
	PutServer(ServerRecord) error

	// Peers is every peer, in any state.
	Peers() ([]PeerRecord, error)
	Peer(id string) (*PeerRecord, error)
	// PeerByKey finds a peer by its current key only.
	PeerByKey(key []byte) (*PeerRecord, error)
	// PutPeer adds or replaces a peer.
	PutPeer(PeerRecord) error
	// DeletePeer removes a peer, its catalog, the keys it was sent, its
	// leftovers and its downloads (DownloadsTx). A peer that isn't there is no error.
	DeletePeer(id string) error

	Codes() ([]CodeRecord, error)
	CodeByHash(secretHash []byte) (*CodeRecord, error)
	PutCode(CodeRecord) error
	DeleteCode(id string) error

	// Forgotten is the entry of a key whose pairing ended, by the
	// SHA-256 of the key (MSFP core section 7.4); nil for none.
	Forgotten(keyHash []byte) (*ForgottenKey, error)
	Forget(ForgottenKey) error
	Unforget(keyHash []byte) error
	// TrimForgotten drops entries forgotten before then (a year old).
	TrimForgotten(before time.Time) error

	// LibraryIDs maps the host's library IDs to the public IDs peers see,
	// those of gone libraries included: a public ID is never reused.
	LibraryIDs() (map[string]string, error)
	PutLibraryID(hostID, publicID string) error

	// Sent says which of these title keys the peer was ever sent; a
	// changes answer names in gone only those (MSFP catalog section 3.2).
	Sent(peer string, keys []string) ([]string, error)
	MarkSent(peer string, keys []string) error

	// Leftovers are playbacks this server started on peers, to stop
	// after a restart.
	Leftovers() ([]Leftover, error)
	PutLeftover(Leftover) error
	DeleteLeftover(Leftover) error

	// Peers' catalogs. The engine only writes them; the host reads them
	// as it likes (counts, joins, search), in its own storage.
	//
	// PutPeerTitles adds or replaces titles, marked with the reading.
	PutPeerTitles(peer string, reading int64, titles []Title) error
	// EndReading, after a whole-list reading, drops the peer's titles not
	// marked with it.
	EndReading(peer string, reading int64) error
	// DropPeerTitles drops titles by key (a changes reading's gone).
	DropPeerTitles(peer string, keys []string) error
	// PeerTitles lists a peer's titles with keys greater than after, in
	// key order, at most limit.
	PeerTitles(peer, after string, limit int) ([]Title, error)
	// PeerTitle is one of them; nil for none.
	PeerTitle(peer, key string) (Title, error)
}

// GroupsTx is what a Tx implements, besides Tx, for the part groups
// (MSFP groups part): the groups this server is in. The shipped stores
// implement it; without it, the engine has no groups.
type GroupsTx interface {
	Groups() ([]GroupRecord, error)
	// Group is one of them; nil for none.
	Group(id string) (*GroupRecord, error)
	PutGroup(GroupRecord) error
	DeleteGroup(id string) error
}

// DownloadsTx is what a Tx implements, besides Tx, to keep downloads for
// their late reports across restarts (MSFP playback section 7): those
// of peers here, and this server's on peers. The shipped stores
// implement it; without it, downloads are kept in memory only, and a
// restart forgets them (a late report then gets 410, or ErrNotFound).
type DownloadsTx interface {
	// Downloads is every download kept, of both sides.
	Downloads() ([]DownloadRecord, error)
	// PutDownload adds or replaces one, by its Viewer and ID.
	PutDownload(DownloadRecord) error
	// DeleteDownload removes one; one that isn't there is no error.
	DeleteDownload(viewer bool, id string) error
}

// OwnLogTx is what a Tx implements, besides Tx, when the engine keeps the
// change log of the host's own catalog (a Catalog without Changes,
// host.md section 2). The shipped stores implement it.
type OwnLogTx interface {
	// OwnSnapshot is the titles as the last refresh read them, by key.
	OwnSnapshot() (map[string]Title, error)
	PutOwnSnapshot(map[string]Title) error
	// OwnChanges lists, for each key greater than after changed after
	// since, its latest change, in key order, at most limit.
	OwnChanges(since int64, after string, limit int) ([]OwnChange, error)
	AppendOwnChanges([]OwnChange) error
	// TrimOwnChanges drops changes noted before then; OwnFloor is the
	// last seq no longer kept (0 when none was dropped); OwnSeq the
	// highest seq ever appended (0 for none).
	TrimOwnChanges(before time.Time) error
	OwnFloor() (int64, error)
	OwnSeq() (int64, error)
	// BumpOwnSeq takes a new change number without a change, and gives
	// it: what a peer sees changed otherwise (its offers, the parts on).
	BumpOwnSeq() (int64, error)
}

// OwnChange is one change of the host's own catalog, in the log the
// engine keeps: a title new or changed (Title set), or gone (Title nil).
type OwnChange struct {
	Seq   int64
	Key   string
	Title Title
	At    time.Time
}

// Catalog is the host's own libraries and titles, by the host's
// library IDs. The engine maps them to public IDs, filters them by
// offers (and local titles by the local sharing switches: SetLocal,
// SetGroupLocal), pages them, and checks every key and member before it
// leaves.
//
// A title is a *VideoTitle or a *MusicTitle. Films, episodes, extras and
// tracks carry their copies, described as their files are (MSFP video
// section 3.2, music section 3.3), so that a viewer's server lists them
// as files of its own before anyone plays; each copy's ID is the host's
// handle of it, the one Playback and Media are given. play takes the
// title's copies as the catalog sends them (in the host's order, its preference; the version IDs
// made of these handles): a copy is played only as it is described. Extras are titles of their own,
// keyed under their film, series or season ("movie/tmdb/348/x/<opaque>",
// kind "extra"), listed in its libraries. Titles no public database names
// are keyed under the local scheme ("movie/local/<opaque>"), with a name
// (and a year, an image) of the host's; their opaque parts, and extras',
// are made with Engine.OpaqueKeyPart or as catalog section 4.2 says.
type Catalog interface {
	// Libraries lists the host's libraries; the operator offers some.
	Libraries(ctx context.Context) ([]Library, error)
	// Titles lists the titles held in any of these libraries, keys
	// strictly greater than after, in ascending byte order, at most
	// limit, each once. A change of anything a title carries, its copies
	// included (a copy added, gone, or its ETag changed), is a change of
	// the title: a ChangeLog lists it again.
	Titles(ctx context.Context, libraries []string, after string, limit int) (TitlePage, error)
}

// Images is what a Catalog implements, besides Catalog, when it shares
// the images of its local titles (MSFP catalog section 4.4): the local
// films, series, artists and albums whose Image it sets. Without it, no
// image is given, whatever Image says.
type Images interface {
	// Image gives the one image of a local title: a JPEG or a PNG of at
	// most 1 MiB, 1 to 4,096 pixels wide and tall. The engine checks it
	// (wire.CheckImage) and sends nothing else. ErrNotFound for none.
	Image(ctx context.Context, key string) ([]byte, error)
}

// ChangeLog is what a Catalog implements, besides Catalog, when its
// database notes changes itself. Without it, the engine keeps the log
// from snapshots of Titles (OwnLogTx).
type ChangeLog interface {
	// Seq is the change number now.
	Seq(ctx context.Context) (int64, error)
	// Changes gives what changed in these libraries after since; it
	// fails with ErrTooOld when the log no longer goes back that far.
	Changes(ctx context.Context, libraries []string, since int64, after string, limit int) (ChangePage, error)
}

// SeqBumper is what a ChangeLog implements, besides ChangeLog, when it can
// take a change number with no change of a title. After a change of what
// a peer sees (its offers, the parts on), the peer's readings are whole
// until the log moves past the number noted then (PeerRecord.OffersSeq,
// one above the log's): the engine takes one at once, so that the reading
// after the next whole one is of changes again, without waiting for a
// title to change. Without it, readings stay whole until one does.
type SeqBumper interface {
	// BumpSeq takes a new change number, above every one before, and
	// gives it; Changes never lists anything under it.
	BumpSeq(ctx context.Context) (int64, error)
}

// ErrTooOld is a ChangeLog's answer for a change number it no longer
// keeps: the engine then sends the whole list.
var ErrTooOld = &Error{Code: "TooOld"}

// Lyrics is what a Catalog implements, besides Catalog, when it shares
// the lyrics of its tracks (MSFP music section 6): the copies whose
// Lyrics says "plain" or "synced".
type Lyrics interface {
	// Lyrics gives a copy's lyrics (its handle), at most 8 sets, times in
	// milliseconds (MSFP music section 6). The engine cleans them as a
	// viewer's server keeps them (wire.ReadLyrics) before they leave.
	// ErrNotFound for none.
	Lyrics(ctx context.Context, copy string) ([]wire.Lyrics, error)
}

// Playback is the owning side of a peer's playback here. The engine has
// checked the request (wire.ReadPlay), found the title's copies as the
// catalog sends them, picked the copy and the audio (those the request
// names, else the first copy and the file's default audio), decided what
// is served (wire.Serve, StartRequest.Served), refused what MSFP
// refuses of the delivery asked and what the pool's limits refuse early;
// then it asks the host how to serve the copy apart (Describe), and to
// start.
type Playback interface {
	// Describe gives what the catalog doesn't say of a copy: its fonts,
	// the subtitle tracks it can't serve apart, the forms of those of
	// formats video section 5.4 doesn't list.
	Describe(ctx context.Context, copy string) (CopyInfo, error)
	// Start starts serving the copy for its purpose and delivery. Without
	// a delivery it serves the baseline in complete HLS, as req.Served
	// says (method remux when each stream is within the baseline and left
	// unchanged, transcode otherwise; never the file); it serves exactly the
	// delivery asked otherwise; then it calls
	// req.Admit before starting any work. Errors: ErrNotPlayable,
	// ErrDeliveryRefused, ErrServerBusy, ErrStreamLimitReached.
	Start(ctx context.Context, req StartRequest) (Started, error)
	// Progress passes on a peer's progress or stop, for the host's live
	// view and its viewers' progress; a Late one is of a download's
	// playback ended already (the viewer watched offline).
	Progress(ctx context.Context, r Report) error
	// Stop ends a playback: once for each Start that succeeded.
	Stop(ctx context.Context, handle string, reason EndReason) error
}

// Media serves the bytes of a live playback. The engine has checked the
// ticket, the request and its Range header, and cuts the response when
// the playback ends. For MediaFile it has resolved the range against
// the copy's exact size (rng nil for the whole file, else First to Last,
// both within it), writes the status and headers itself (206 or 200,
// Content-Range, ETag, Accept-Ranges) and sends only those bytes: the
// host writes the copy's bytes from First, as they are, and its Start's
// content type alone is kept. Subtitles, ASS, pictures and fonts are
// read whole and rebuilt or checked (wire) before they leave; playlists
// are checked against MSFP's subset. Errors: ErrNotFound,
// ErrRangeNotSatisfiable, ErrServerBusy.
type Media interface {
	Serve(ctx context.Context, handle string, path MediaPath, rng *Range, out ResponseSink) error
}

// Sealer seals the seeds and secrets the engine stores, each under a
// context ("msfp current key", "msfp next key", "msfp old key", "msfp
// revoked key", "msfp ids secret", "msfp code secret"): a value sealed
// under one context never opens under another. Open fails with
// ErrSealed when the box is tampered with, of another context, or of
// another key.
//
// The host chooses how: a key in its configuration (KeySealer), a key
// file apart from its data (KeyFileSealer), a secret store. PlainSealer
// seals nothing, for tests.
type Sealer interface {
	Seal(plain []byte, context string) ([]byte, error)
	Open(sealed []byte, context string) ([]byte, error)
}

// SealerProvider is what a Store implements when it brings its own
// Sealer, used when Config.Sealer is nil (the shipped stores do).
type SealerProvider interface {
	Sealer() Sealer
}

// Clock is the time. The system's by default; tests set a fake one and
// call RunOnce to make time's work happen.
type Clock interface {
	Now() time.Time
}

// Timers is what a Clock implements, besides, when its time isn't the
// system's (a test's clock): After gives a channel that receives once d
// passed on that clock. The engine waits with it where it waits on time
// in a request (a download's pace); without it, it waits d of the
// system's time.
type Timers interface {
	After(d time.Duration) <-chan time.Time
}

// Dialer makes the connections to peers. The default is the engine's
// own, which looks a name up once, refuses the destinations of MSFP core
// section 3.3, and connects to an address it checked. A host whose
// connections go out elsewhere (through a gateway) gives its own, which
// must make the same check where the connection is made. The engine still
// does TLS, pins, no redirects and the time bounds on top.
//
// network is "tcp"; addr is "host:port". Errors that wrap
// ErrRefusedDestination are told apart from a peer that can't be reached.
type Dialer interface {
	Dial(ctx context.Context, network, addr string) (net.Conn, error)
}

// Notifier is told the engine's events, for the operator's panel and the
// viewers' pages. Notify is called in order, one at a time, outside the
// engine's locks; it must return quickly (a host with slow work queues
// it). Events are notices: what they speak of is in the queries too.
type Notifier interface {
	Notify(Event)
}

// SheetEncoder re-encodes a relayed preview sheet, so that only pixels
// reach the viewer. Go's default uses image/jpeg.
type SheetEncoder interface {
	Reencode(jpeg []byte, maxWidth, maxHeight int) ([]byte, error)
}

// ResponseSink is where Media writes an answer: Start once, with the
// status, the content type, the length (-1: unknown) and the
// Content-Range ("" for none), then the body.
type ResponseSink interface {
	Start(status int, contentType string, length int64, contentRange string)
	io.Writer
}
