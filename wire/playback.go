package wire

import (
	"regexp"
	"strconv"
	"strings"
)

// Playback (MSFP playback, MSFP Media video 5) and its media's URLs (MSFP Media fetching
// 1, MSFP Media video 6.1): the forms of what a play answer gives,
// as the viewer's server checks them.

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// PlayID says whether s is a playback ID (MSFP Media playing 2.3): the
// form of other opaque IDs (ID). An owning server makes it from at least
// 128 bits of a secure random source, so that nobody guesses another
// pairing's playback.
func PlayID(s string) bool { return ID(s) }

// ID says whether s has the form of a library's, a track's or a
// version's ID (catalog section 2, MSFP Media playing 2.3), and of a
// ticket (MSFP Media fetching 1): 1 to 64 characters of A–Z a–z 0–9 _ -.
func ID(s string) bool { return idRe.MatchString(s) }

// TrackID reads a track ID of the given kind ('a' for audio, 's' for
// subtitles; MSFP Media playing 2.1, MSFP Media video 5.1): the letter and a
// number from 0 to 999 without leading zeros (a01 is malformed); false
// for anything else.
func TrackID(id string, kind byte) (int, bool) {
	if len(id) < 2 || len(id) > 4 || id[0] != kind || strings.Trim(id[1:], "0123456789") != "" || len(id) > 2 && id[1] == '0' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	return n, err == nil
}

// StreamRoot is a server's stream root (MSFP media section 1) at address,
// without a discovery document: <address>/api/v1/stream/.
func StreamRoot(address string) string {
	return strings.TrimSuffix(address, "/") + "/api/v1/stream/"
}

// The names an owning server serves under a ticket (MSFP Media video 6.1,
// MSFP Media fetching 1): a viewer's server builds every media URL from the
// owning server's stream root, the ticket and one of these, and never
// fetches an address a peer wrote.
const (
	NameMaster = "master.m3u8"
	NameFile   = "file"
	NameImage  = "image"
)

// StreamURL is the URL of a playback's stream (MSFP Media playing 2.3):
// <root><ticket>/master.m3u8 for hls, <root><ticket>/file for file.
func StreamURL(root, ticket, protocol string) string {
	if protocol == ProtocolFile {
		return root + ticket + "/" + NameFile
	}
	return root + ticket + "/" + NameMaster
}

// TicketBase is <root><ticket>/, the base of every URL under a ticket.
func TicketBase(root, ticket string) string { return root + ticket + "/" }

// SubtitleURL is the base of a subtitle track's URLs under a ticket's
// base (MSFP Media video 6.1), to which /<part> or /whole is added:
// subtitles/<track> for the form text, pictures/<track> for picture,
// ass/<track> for a track's ASS.
func SubtitleURL(base, track, form string) string {
	switch form {
	case FormPicture:
		return base + "pictures/" + track
	case FormASS:
		return base + "ass/" + track
	}
	return base + "subtitles/" + track
}

// FontURL is the URL of font n of a copy under a ticket's base, n from 0
// to fontCount − 1 (MSFP Media video 6.1).
func FontURL(base string, n int) string { return base + "fonts/" + strconv.Itoa(n) }

// SheetURL is the URL of sheet n of a copy's previews under a ticket's
// base (MSFP Media video 6.1), n below the sheet count of its layout.
func SheetURL(base string, n int) string { return base + "trickplay/" + strconv.Itoa(n) }

// PreviewLayout is the layout of a copy's seek-bar previews, as the
// catalog gives it (MSFP Media video 5.5): the tiles' size, the grid of a
// sheet, the time between tiles and their count. Its sheets are fetched
// on demand, under the playback's ticket or a previews ticket.
type PreviewLayout struct {
	TileWidth  int `json:"tileWidth"`
	TileHeight int `json:"tileHeight"`
	Columns    int `json:"columns"`
	Rows       int `json:"rows"`
	IntervalMs int `json:"intervalMs"`
	TileCount  int `json:"tileCount"`
}

// The bounds of MSFP Media video 5.5.
const (
	maxTile       = 2048
	maxGrid       = 100
	maxSheetSide  = 4096
	MaxSheets     = 1000
	minInterval   = 100
	maxInterval   = 3_600_000
	MaxSheetBytes = 8 << 20 // a sheet, as read (MSFP Media fetching 2.4)
)

// SheetCount is how many sheets the tiles fill: ⌈tileCount / (columns ×
// rows)⌉, 0 when either is 0.
func (t PreviewLayout) SheetCount() int {
	per := t.Columns * t.Rows
	if per <= 0 || t.TileCount <= 0 {
		return 0
	}
	return (t.TileCount + per - 1) / per
}

// InBounds says whether a layout is within MSFP Media video 5.5's bounds:
// tiles 1 to 2048 pixels a side, 1 to 100 columns and rows, a sheet at
// most 4096 pixels a side, an interval of 100 ms to an hour, and 1 to
// 1,000 × columns × rows tiles.
func (t PreviewLayout) InBounds() bool {
	in := func(v, lo, hi int) bool { return v >= lo && v <= hi }
	return in(t.TileWidth, 1, maxTile) && in(t.TileHeight, 1, maxTile) && in(t.Columns, 1, maxGrid) &&
		in(t.Rows, 1, maxGrid) && t.Columns*t.TileWidth <= maxSheetSide && t.Rows*t.TileHeight <= maxSheetSide &&
		in(t.IntervalMs, minInterval, maxInterval) && in(t.TileCount, 1, MaxSheets*t.Columns*t.Rows)
}

// readPreviewLayout is a copy's previewLayout as kept: nil, dropped
// alone, when it isn't an object, or a member is missing, isn't an
// integer or is out of bounds.
func readPreviewLayout(v any) *PreviewLayout {
	o, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	n := map[string]int{}
	for _, f := range []string{"tileWidth", "tileHeight", "columns", "rows", "intervalMs", "tileCount"} {
		i, ok := videoInt(o[f], 0, 1<<31-1)
		if !ok {
			return nil
		}
		n[f] = int(i)
	}
	l := PreviewLayout{TileWidth: n["tileWidth"], TileHeight: n["tileHeight"], Columns: n["columns"], Rows: n["rows"],
		IntervalMs: n["intervalMs"], TileCount: n["tileCount"]}
	if !l.InBounds() {
		return nil
	}
	return &l
}

// RelayedMediaType is the Content-Type a viewer's server gives a
// segment it relays (MSFP Media fetching 3): the owning server's, lowercased
// and without parameters, when it is a video/ or audio/ type made of
// letters, digits and .+- only (never a playlist's),
// application/octet-stream otherwise.
func RelayedMediaType(ct string) string {
	t, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(ct)), ";")
	t = strings.TrimSpace(t)
	kind, sub, ok := strings.Cut(t, "/")
	if ok && (kind == "video" || kind == "audio") && sub != "" &&
		strings.Trim(sub, "abcdefghijklmnopqrstuvwxyz0123456789.+-") == "" && !strings.Contains(sub, "mpegurl") {
		return t
	}
	return "application/octet-stream"
}

// RelayedFileType is the Content-Type a viewer's server gives a copy's
// file it relays (MSFP Media fetching 3): the copy's own mediaType from the
// catalog, whatever its top level, when it is a media type;
// application/octet-stream when it has none.
func RelayedFileType(mediaType string) string {
	if IsMediaType(mediaType) {
		return mediaType
	}
	return "application/octet-stream"
}
