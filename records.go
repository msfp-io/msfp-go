package msfp

import (
	"time"

	"github.com/msfp-io/msfp-go/wire"
)

// The records a Store keeps (host.md section 1). The engine fills every
// field; a store keeps them as they are, in JSON (the tags are for the
// shipped stores) or in columns of its own. Times are UTC; a zero time
// means "never" or "none".

// ServerRecord is this server's own: its keys, its address, its
// settings. There is one.
type ServerRecord struct {
	// Current is the current key's seed, sealed ("msfp current key").
	Current []byte `json:"current"`
	// Next is the next key's seed, sealed ("msfp next key"); nil once the
	// operator exported and deleted it. NextCommitment is SHA-256 of its
	// public key, and stays (MSFP core section 12.5).
	Next           []byte `json:"next,omitempty"`
	NextCommitment []byte `json:"nextCommitment"`
	// Old is the key before Current while peers move from it, sealed
	// ("msfp old key"), since OldSince; OldRevoked: it was revoked first
	// (MSFP core sections 12.4 and 12.7).
	Old        []byte    `json:"old,omitempty"`
	OldSince   time.Time `json:"oldSince,omitzero"`
	OldRevoked bool      `json:"oldRevoked,omitempty"`
	// Revoked is a key revoked with no next key, replaced by Current,
	// sealed ("msfp revoked key"; "msfp old key" before 2026-11-18, sealed
	// again at Open), since RevokedAt: kept 30 days for the
	// answers to the hellos of the peers whose pairing ended then (MSFP
	// core section 12.7, case 2): those whose ForgottenKey is Revoked,
	// At RevokedAt.
	Revoked   []byte    `json:"revoked,omitempty"`
	RevokedAt time.Time `json:"revokedAt,omitzero"`
	// IDsSecret is the secret of opaque IDs, sealed ("msfp ids secret").
	// It never changes.
	IDsSecret []byte `json:"idsSecret"`
	// Address is where this server is now: the address noted last, or
	// the UncallableAddress while it can't be called. Former is the one
	// before it, MovedAt when it changed, while a move lasts (MSFP core
	// section 13.1); Reached, that this server reached itself at Address,
	// so that its move is announced (until then its hellos name Former).
	// A Former of the .invalid form is never accepted as a host (no former
	// address is kept for a move from it): it is only named until Reached.
	Address string    `json:"address"`
	Former  string    `json:"former,omitempty"`
	MovedAt time.Time `json:"movedAt,omitzero"`
	Reached bool      `json:"reached,omitempty"`
	// Uncallable is the operator's setting "this server can't be called",
	// and UncallableAddress the https://msfp-<26>.invalid address made
	// for it, kept (MSFP core section 7.5).
	Uncallable        bool   `json:"uncallable,omitempty"`
	UncallableAddress string `json:"uncallableAddress,omitempty"`
}

// PeerState is where a pairing stands (MSFP core section 7.3).
type PeerState string

// The states of a peer.
const (
	// Ours: the peer used our code; our operator hasn't accepted.
	Ours PeerState = "ours"
	// Theirs: we used the peer's code; its operator hasn't accepted.
	Theirs PeerState = "theirs"
	// Paired: both sides accepted.
	Paired PeerState = "paired"
	// Gone: the peer answered, signed, that it doesn't know us.
	Gone PeerState = "gone"
)

// Inbound is whether a peer can be called (MSFP core section 7.5).
type Inbound string

// The values of Inbound.
const (
	InboundYes        Inbound = "yes"
	InboundNo         Inbound = "no"
	InboundConfirming Inbound = "confirming"
)

// PeerRecord is a server this one is paired, or pairing, with.
type PeerRecord struct {
	// Identity.
	//
	// ID is the engine's own, random and stable; peers never see it. Key
	// is the peer's current public key; PreviousKey its key before its
	// latest rotation, still heard until PreviousUntil.
	ID            string    `json:"id"`
	Key           []byte    `json:"key"`
	PreviousKey   []byte    `json:"previousKey,omitempty"`
	PreviousUntil time.Time `json:"previousUntil,omitzero"`
	// Name is the peer's name, cleaned.
	Name    string    `json:"name"`
	Created time.Time `json:"created"`

	// State.
	//
	// WasPaired: it was paired once, so that a gone peer's paired hello
	// brings it back. Paired is when it became paired, Seen when it was
	// last heard from, Suspended when it revoked its key.
	State     PeerState `json:"state"`
	WasPaired bool      `json:"wasPaired,omitempty"`
	Paired    time.Time `json:"paired,omitzero"`
	Seen      time.Time `json:"seen,omitzero"`
	Suspended time.Time `json:"suspended,omitzero"`

	// Address.
	//
	// Address is where to call it, Base its base path from its discovery
	// document ("" for the default, /api/v1/federation/). Move is an
	// address it announced, not confirmed yet. OurAddress is the host of
	// ours (host[:port]) its requests were last signed for: it moved to
	// our new address once that is our current host. Inbound says whether
	// it can be called; CallsFailed, that its latest hello answer said our
	// calls to it couldn't connect.
	Address     string    `json:"address"`
	Base        string    `json:"base,omitempty"`
	Move        *PeerMove `json:"move,omitempty"`
	OurAddress  string    `json:"ourAddress,omitempty"`
	Inbound     Inbound   `json:"inbound"`
	CallsFailed bool      `json:"callsFailed,omitempty"`

	// Versions is what its latest signed hello listed (nil before the
	// first); Pins its certificates' pins, from the same.
	Versions *wire.Versions `json:"versions,omitempty"`
	Pins     []string       `json:"pins,omitempty"`

	// Its keys.
	//
	// Commitment is its next key commitment, and CommitmentFrom where it
	// came from ("pair", "rotation"; "" for none). Pending is a rotation
	// to a key with no commitment, waiting for our operator; Rotations the
	// statements kept; Refused a rotation refused, for the operator.
	Commitment     []byte          `json:"commitment,omitempty"`
	CommitmentFrom string          `json:"commitmentFrom,omitempty"`
	Pending        *wire.Rotation  `json:"pending,omitempty"`
	Rotations      []wire.Rotation `json:"rotations,omitempty"`
	Refused        *RefusedKey     `json:"refused,omitempty"`

	// Our keys toward it.
	//
	// OurKey is what it knows us by: KeyCurrent (""), KeyOld ("old": it
	// hasn't moved to our new one yet) or KeyLost ("lost": it never did).
	// RotationAnswer is its last answer to our rotate ("pending",
	// "refused" or ""); RotateSent when we last sent one; CommitmentTold,
	// that it holds our next key's commitment.
	OurKey         string    `json:"ourKey,omitempty"`
	RotationAnswer string    `json:"rotationAnswer,omitempty"`
	RotateSent     time.Time `json:"rotateSent,omitzero"`
	CommitmentTold bool      `json:"commitmentTold,omitempty"`

	// Sharing.
	//
	// Offers are the host libraries offered to it, OffersSeq the change
	// number they were set at; Wants are its libraries we want; Limits
	// ours to it, TheirLimits its to us (nil until it said); CatalogRead
	// where our reading of its titles stands. Local says the individual
	// relation shares the host's local titles too, in the libraries it
	// offers (MSFP catalog section 5: off by default).
	Offers      []string        `json:"offers,omitempty"`
	Local       bool            `json:"local,omitempty"`
	OffersSeq   int64           `json:"offersSeq,omitempty"`
	Wants       []WantedLibrary `json:"wants,omitempty"`
	Limits      Limits          `json:"limits"`
	TheirLimits *Limits         `json:"theirLimits,omitempty"`
	CatalogRead CatalogRead     `json:"catalogRead,omitzero"`

	// Groups (MSFP groups part).
	//
	// Groups are the groups this link was made or kept through: its group
	// relations (MSFP core section 7.7). NotByHand says this side doesn't
	// want the individual relation; TheirIndividual is what the peer last
	// said of it in hello (nil: never said, counted as ours). KeepChoice:
	// our operator is choosing whether to keep the link (a dissolution with
	// keepPairings). IndividualRefused: our operator refused the peer's
	// ask, privately. GroupOffers and Pools are what its
	// groups give it, copied from them whenever they change: the host
	// libraries each offers, and each group's pool; GroupLocal, the host
	// libraries whose local titles they share (those of the groups that
	// share local titles, among the libraries each offers). JoiningGroup is the
	// group a code of ours invited it to, until our operator accepts it.
	Groups            []string    `json:"groups,omitempty"`
	NotByHand         bool        `json:"notByHand,omitempty"`
	TheirIndividual   *bool       `json:"theirIndividual,omitempty"`
	KeepChoice        bool        `json:"keepChoice,omitempty"`
	IndividualRefused bool        `json:"individualRefused,omitempty"`
	GroupOffers       []string    `json:"groupOffers,omitempty"`
	GroupLocal        []string    `json:"groupLocal,omitempty"`
	Pools             []GroupPool `json:"pools,omitempty"`
	JoiningGroup      string      `json:"joiningGroup,omitempty"`
}

// HiddenLibrary is a member's library a group doesn't show here: the
// member's ID in the group, and the library's public ID.
type HiddenLibrary struct {
	Member  string `json:"member"`
	Library string `json:"library"`
}

// GroupPool is a group's pool, as a link through it uses it.
type GroupPool struct {
	Group  string `json:"group"`
	Limits Limits `json:"limits"`
}

// GroupRecord is a group this server is in (MSFP groups part): its events
// as received, those waiting for earlier ones, the mail it carries, and
// this server's own choices in it. The engine owns every field.
type GroupRecord struct {
	ID string `json:"id"`
	// Me is this server's member ID in it: the key it joined with.
	Me string `json:"me"`
	// Via is the peer whose code made this server join (its inviter),
	// until the list arrives from it.
	Via    string        `json:"via,omitempty"`
	Events []StoredEvent `json:"events"`
	Held   []StoredEvent `json:"held,omitempty"`
	Mail   []StoredMail  `json:"mail,omitempty"`
	// MailKey is this server's X25519 private key for the group's mail,
	// sealed ("msfp group mail key").
	MailKey []byte `json:"mailKey"`
	// Accept: this server automatically accepts new members, at once;
	// false: each request waits for its operator (groups section 4.1).
	Accept bool     `json:"accept,omitempty"`
	Offers []string `json:"offers,omitempty"`
	// Local: this server shares its local titles with the group, in the
	// libraries it offers it (MSFP groups section 9: off by default).
	Local bool   `json:"local,omitempty"`
	Pool  Limits `json:"pool"`
	// Hidden are the members' libraries this server doesn't show from the
	// group: it shows everything else they offer it (SetGroupShows).
	Hidden []HiddenLibrary `json:"hidden,omitempty"`
	// Requests wait for this server's operator; Refused are members
	// whose requests it refused; Sent are the requests and answers it
	// sent, until used or expired; Used the mail IDs a group-pair used.
	Requests []GroupRequest `json:"requests,omitempty"`
	Refused  []string       `json:"refused,omitempty"`
	Sent     []SentMail     `json:"sent,omitempty"`
	Used     []string       `json:"used,omitempty"`
	Synced   time.Time      `json:"synced,omitzero"`
	// Joining is when this server called group-join to join through Via,
	// a peer already (zero: it used pair). It gives up 30 days after.
	Joining time.Time `json:"joining,omitzero"`
	// Joins are peers already paired with this server that used its code
	// for this group (group-join), waiting for its operator.
	Joins []GroupJoin `json:"joins,omitempty"`
}

// GroupJoin is a peer waiting for this server's operator to accept it
// into a group (MSFP groups section 8.4).
type GroupJoin struct {
	Peer     string    `json:"peer"`
	Received time.Time `json:"received"`
}

// StoredEvent is an event's envelope, and when it arrived.
type StoredEvent struct {
	Event     string    `json:"event"`
	Signature string    `json:"signature"`
	Received  time.Time `json:"received"`
}

// StoredMail is a mail's envelope, its ID and when it expires.
type StoredMail struct {
	ID        string `json:"id"`
	Mail      string `json:"mail"`
	Signature string `json:"signature"`
	Expires   int64  `json:"expires"`
}

// GroupRequest is a member's request waiting for this server's operator,
// opened.
type GroupRequest struct {
	Mail           string    `json:"mail"`
	From           string    `json:"from"`
	Name           string    `json:"name,omitempty"`
	Address        string    `json:"address"`
	NextCommitment []byte    `json:"nextCommitment"`
	Inbound        bool      `json:"inbound"`
	Pins           []string  `json:"pins,omitempty"`
	Received       time.Time `json:"received"`
}

// SentMail is a request or an answer this server sent, waiting for the
// group-pair it leads to.
type SentMail struct {
	ID      string    `json:"id"`
	To      string    `json:"to"`
	Kind    string    `json:"kind"`
	Expires time.Time `json:"expires"`
}

// PeerMove is an address a peer announced, being checked (MSFP core
// section 13.2): since when, when a confirming hello was last sent there
// (Tried), since when those failed (FailingSince), and whether the
// operator was warned of it (after a day).
type PeerMove struct {
	Address      string    `json:"address"`
	Since        time.Time `json:"since"`
	Tried        time.Time `json:"tried,omitzero"`
	FailingSince time.Time `json:"failingSince,omitzero"`
	Warned       bool      `json:"warned,omitempty"`
}

// RefusedKey is a key a peer tried to rotate to, refused.
type RefusedKey struct {
	Key []byte    `json:"key"`
	At  time.Time `json:"at"`
}

// CatalogRead is where the reading of a peer's titles stands: the change
// number read up to, and the wants it was read with.
type CatalogRead struct {
	Seq   int64    `json:"seq,omitempty"`
	Wants []string `json:"wants,omitempty"`
}

// CodeRecord is a pairing code this server made.
type CodeRecord struct {
	ID         string    `json:"id"`
	Secret     []byte    `json:"secret"` // sealed ("msfp code secret")
	SecretHash []byte    `json:"secretHash"`
	Note       string    `json:"note,omitempty"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires"`
	// Uses is how many more servers may use it: 0 for a record made
	// before uses existed, which counts as 1 (MSFP core section 5.1).
	Uses int `json:"uses,omitempty"`
	// Group is the group (its ID, base64url) a group's code invites to;
	// "" for a pairing by hand.
	Group string `json:"group,omitempty"`
}

// ForgottenKey is a key whose pairing ended (MSFP core section 7.4), by
// its SHA-256: its requests get a signed 404. Revoked: the pairing ended
// when this server's key was revoked with no next key (section 12.7,
// case 2), at At, the ServerRecord's RevokedAt: for 30 days its hellos
// get the revocation. An entry of an earlier replacement has another At.
type ForgottenKey struct {
	KeyHash []byte    `json:"keyHash"`
	At      time.Time `json:"at"`
	Revoked bool      `json:"revoked,omitempty"`
	// Blocked: the operator blocked it, which keeps it away from every
	// group (MSFP core section 7.4); other ended keys are kept only for
	// the signed 404.
	Blocked bool `json:"blocked,omitempty"`
}

// Leftover is a playback this server started on a peer (the peer's
// playback ID), to stop after a restart.
type Leftover struct {
	Peer       string `json:"peer"`
	PlaybackID string `json:"playbackId"`
}

// DownloadRecord is a download kept for its late reports, 30 days after
// it started (MSFP playback section 7), so that a restart keeps them.
// Of a peer's download here (Viewer false): ID is the playback ID given
// to the peer, Handle the host's, LastAt the at of the latest report
// kept. Of this server's download on a peer (Viewer true): ID is the
// engine's ID of it, TheirID the peer's playback ID.
type DownloadRecord struct {
	ID       string    `json:"id"`
	Viewer   bool      `json:"viewer,omitempty"`
	Peer     string    `json:"peer"`
	TheirID  string    `json:"theirId,omitempty"`
	Title    string    `json:"title"`
	Queue    []string  `json:"queue,omitempty"`
	Handle   string    `json:"handle,omitempty"`
	PlayedAt time.Time `json:"playedAt"`
	LastAt   time.Time `json:"lastAt,omitzero"`
}
