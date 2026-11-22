package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The catalog part's own rules (MSFP catalog sections 3 and 4), shared by
// the content parts: pages bounded by size, the opaque parts of title
// keys, the description of a local title, and its image.

// Pages (catalog section 3.1): a page of titles holds at most
// MaxPageTitles keys (titles and gone together in a changes answer) and
// its answer body at most MaxPageBytes of JSON; after, and so next, is
// at most MaxAfter bytes.
const (
	MaxPageTitles = 5000
	MaxPageBytes  = 3 << 20
	MaxAfter      = 512
)

// PageReserve is room enough in a page for everything but its entries:
// the object's members, next (MaxAfter bytes), changes and seq. An owning
// server writing a page starts its PageBudget with it, unless it counts
// the rest of its answer itself.
const PageReserve = 1024

// PageBudget counts what an owning server puts in a page of titles, so
// that it ends the page early when the next entry would pass either
// bound (catalog section 3.1). Its zero value counts no reserve: start
// one with NewPageBudget.
type PageBudget struct {
	keys, bytes int
}

// NewPageBudget is the budget of an empty page whose answer, entries
// aside, takes reserve bytes (PageReserve, say).
func NewPageBudget(reserve int) PageBudget { return PageBudget{bytes: reserve} }

// Add counts an entry of size bytes (a title as JSON, or a key of gone,
// its quotes included), with the comma before it, and says whether it
// fits; one that doesn't is not counted, and the page ends before it.
// false on an empty page means the entry alone passes the bound: it is
// not sent at all, and the owning server goes on with the next.
func (b *PageBudget) Add(size int) bool {
	if b.keys > 0 {
		size++ // the comma
	}
	if b.keys+1 > MaxPageTitles || b.bytes+size > MaxPageBytes {
		return false
	}
	b.keys++
	b.bytes += size
	return true
}

// Keys is how many entries the page holds.
func (b *PageBudget) Keys() int { return b.keys }

// AskOn is the caller's stop rule (catalog section 3.1): it asks for the
// next page, with next as after, only when next is set (an absent one
// is "") and greater than its after in byte order. A short page with a
// next is no end.
func AskOn(after, next string) bool { return next != "" && next > after }

// Page is what an owning server's page of titles holds, for CheckPage.
type Page struct {
	Changes bool   // a changes answer, not a whole-list one
	Titles  int    // how many titles it holds
	Gone    int    // how many keys of gone
	Bytes   int    // its answer body's length
	Remain  bool   // whole list: titles remain after it on the owning server
	After   string // the request's after
	LastKey string // the page's last title key
	Next    string // its next; "" when absent
	HasNext bool   // next is present
}

// CheckPage says whether an owning server may send p (catalog section
// 3.1): at most MaxPageTitles keys and MaxPageBytes; in a whole-list
// answer, no gone, keys after after, and next the page's last key
// exactly when titles remain. In a changes answer, next is the owning
// server's call. A caller accepts any answer within the core's bound,
// whatever CheckPage says: it is for owning servers and their tests.
func CheckPage(p Page) error {
	switch {
	case p.Titles < 0 || p.Gone < 0 || p.Bytes < 0:
		return errors.New("a negative count")
	case !p.Changes && p.Gone != 0:
		return errors.New("gone in a whole-list answer")
	case p.Titles+p.Gone > MaxPageTitles && p.Changes:
		return errors.New("titles and gone together over 5,000 keys")
	case p.Titles+p.Gone > MaxPageTitles:
		return errors.New("over 5,000 titles")
	case p.Bytes > MaxPageBytes:
		return errors.New("an answer body over 3 MiB (3,145,728 bytes)")
	case p.Changes:
		return nil
	case p.Titles > 0 && p.LastKey <= p.After:
		return errors.New("the page's last key isn't after after")
	case p.Remain && p.Titles == 0:
		return errors.New("an empty page while titles remain")
	case p.Remain && !p.HasNext:
		return errors.New("next absent while titles remain")
	case p.Remain && p.Next != p.LastKey:
		return errors.New("next isn't the page's last key")
	case !p.Remain && p.HasNext:
		return errors.New("next set after the last title")
	}
	return nil
}

// OpaqueKeyPart says whether s may be an opaque part of a title key: a
// local title's identifier, an extra's part after x/ (catalog section
// 4.2). It has a library ID's form, 1 to 64 characters of
// A–Z a–z 0–9 _ -, compared byte for byte, and follows a library ID's
// rule: chosen by the owning server, never computable from a media
// server's ID, a file, a path or a name.
func OpaqueKeyPart(s string) bool { return ID(s) }

// DescriptionBounds are the bounds of a local title's description: the
// catalog's (LocalDescription), or a content part's own for its titles
// (catalog section 4.3).
type DescriptionBounds struct {
	NameRequired     bool // the kind carries a name; without one the title is skipped
	MaxName          int  // in code points
	MinYear, MaxYear int  // MinYear at least 1
}

// LocalDescription are catalog section 4.3's bounds, for a local film or
// series: a name of at most 128 characters, required; a year from 1800
// to 9999.
var LocalDescription = DescriptionBounds{NameRequired: true, MaxName: 128, MinYear: 1800, MaxYear: 9999}

// Description is what a caller keeps of a local title's description
// (catalog section 4.3). It is the owning server's, shown as such, never
// sent on to another peer.
type Description struct {
	Name  string `json:"name,omitempty"`  // cleaned text, never markup
	Year  int    `json:"year,omitempty"`  // 0: absent
	Image bool   `json:"image,omitempty"` // the image call gives one
}

// ReadDescription reads a local title's description from the title's
// JSON object, within b: name cleaned as CleanText does, a name that
// isn't a string counting as absent; year that isn't an integer in
// range, and image that isn't a boolean, absent. false means the title
// is skipped: not an object, or, where b requires a name, none or one
// empty once cleaned. An owning server cleans its names the same way
// before sending them, and sends no file name.
func ReadDescription(title json.RawMessage, b DescriptionBounds) (Description, bool) {
	var o map[string]json.RawMessage
	if json.Unmarshal(title, &o) != nil || o == nil {
		return Description{}, false
	}
	var d Description
	var name string
	hasName := len(o["name"]) > 0 && o["name"][0] == '"' && json.Unmarshal(o["name"], &name) == nil
	if hasName {
		d.Name = CleanText(name, b.MaxName)
	}
	if b.NameRequired && d.Name == "" {
		return Description{}, false
	}
	if y := integerIn(o["year"], int64(max(b.MinYear, 1)), int64(b.MaxYear)); y > 0 {
		d.Year = int(y)
	}
	d.Image = string(o["image"]) == "true"
	return d, true
}

// imageKinds are the kinds the image call gives an image of, and the
// content part of each (catalog section 4.4).
var imageKinds = map[string]string{"movie": PartVideo, "series": PartVideo, "artist": PartMusic, "album": PartMusic}

// CheckImageRequest is the owning server's answer to an image call
// (catalog section 4.4), in the spec's order: 404 not-supported when no
// content part with local titles is on between the two servers (on
// says, for PartVideo and PartMusic), before the body is read; 400
// invalid-request when the body isn't I-JSON, an object, or its title
// is missing or isn't a string; 404 not-found for a title that isn't
// a local film, series, artist or album of a part that's on, or that
// image doesn't know: image says whether the owning server offers the
// caller key, and whether its description says image true. Else 200,
// with the title, whose image the owning server then gives under a
// ticket of its own (ImageURL). problem is the problem's type, "" for
// 200.
func CheckImageRequest(on func(part string) bool, body []byte, image func(key string) bool) (title string, status int, problem string) {
	if !on(PartVideo) && !on(PartMusic) {
		return "", 404, "not-supported"
	}
	var req struct {
		Title *string `json:"title"`
	}
	if DecodeJSON(body, &req) != nil || req.Title == nil {
		return "", 400, "invalid-request"
	}
	k := strings.Split(*req.Title, "/")
	if len(k) != 3 || k[1] != "local" || !OpaqueKeyPart(k[2]) || imageKinds[k[0]] == "" || !on(imageKinds[k[0]]) || !image(*req.Title) {
		return "", 404, "not-found"
	}
	return *req.Title, 200, ""
}

// ImageURL is the URL a viewer's server builds from an image call's
// ticket: <stream root><ticket>/image (catalog section 4.4, media
// section 1). The ticket works for 10
// minutes, for that URL only, and is no playback.
func ImageURL(root, ticket string) string { return root + ticket + "/image" }

// ReadImageAnswer reads an image call's answer (catalog section 4.4):
// {"ticket"}, the ticket of ID's form; false drops the answer, and no
// image is fetched. The image's URL is built from it (ImageURL); a url
// member of earlier drafts is ignored.
func ReadImageAnswer(body []byte) (ticket string, ok bool) {
	var a struct {
		Ticket *string `json:"ticket"`
	}
	if DecodeAnswer(body, &a) != nil || a.Ticket == nil || !ID(*a.Ticket) {
		return "", false
	}
	return *a.Ticket, true
}

// A local title's image (catalog section 4.4): at most MaxImageBytes (an
// image of exactly that size is accepted), 1 to MaxImageSide pixels wide
// and tall.
const (
	MaxImageBytes = 1 << 20
	MaxImageSide  = 4096
)

// The image's formats, as CheckImage gives them.
const (
	ImageJPEG = "jpeg"
	ImagePNG  = "png"
)

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

// CheckImage checks a local title's image, read whole, before it is
// decoded (catalog section 4.4): within MaxImageBytes, a JPEG or a PNG
// by its content, whatever its Content-Type, whose header (a PNG's IHDR,
// a JPEG's frame header) gives a width and a height of 1 to
// MaxImageSide; a JPEG of height 0, left to a DNL marker, is refused. It
// gives the format, ImageJPEG or ImagePNG. The viewer's server then
// decodes it with these bounds and re-encodes it in the same format, so
// that only its pixels reach its people.
func CheckImage(img []byte) (string, error) { return checkImage(img, len(img)) }

// checkImage is CheckImage of an image of size bytes, given its first
// bytes, head.
func checkImage(head []byte, size int) (string, error) {
	if size > MaxImageBytes {
		return "", errors.New("over 1 MiB (1,048,576 bytes)")
	}
	var format string
	var w, h int
	switch {
	case bytes.HasPrefix(head, pngSignature):
		format = ImagePNG
		ihdr := head[len(pngSignature):]
		if len(ihdr) < 16 || !bytes.Equal(ihdr[:8], []byte{0, 0, 0, 13, 'I', 'H', 'D', 'R'}) {
			return "", errors.New("a PNG whose first chunk isn't IHDR")
		}
		w, h = int(uint32be(ihdr[8:])), int(uint32be(ihdr[12:]))
	case bytes.HasPrefix(head, []byte{0xff, 0xd8, 0xff}):
		format = ImageJPEG
		var err error
		if w, h, err = jpegSize(head); err != nil {
			return "", err
		}
	default:
		return "", errors.New("neither a JPEG nor a PNG by its content")
	}
	if w < 1 || h < 1 {
		return "", errors.New("a width or a height of 0")
	}
	if w > MaxImageSide || h > MaxImageSide {
		return "", errors.New("wider or taller than 4,096 pixels")
	}
	return format, nil
}

func uint32be(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// jpegSize reads a JPEG's markers, after its SOI, up to its frame header
// (SOF0 to SOF15, but DHT, JPG and DAC), and gives its width and height.
func jpegSize(b []byte) (w, h int, err error) {
	i := 2
	for {
		if i >= len(b) || b[i] != 0xff {
			return 0, 0, errors.New("no marker where one is due")
		}
		for i < len(b) && b[i] == 0xff {
			i++ // the marker's FF, and fill bytes before it
		}
		if i >= len(b) {
			return 0, 0, errors.New("no frame header in its header")
		}
		m := b[i]
		i++
		switch {
		case m == 0x01 || m >= 0xd0 && m <= 0xd7:
			continue // TEM and RSTn have no length
		case m == 0xd9 || m == 0xda:
			return 0, 0, errors.New("no frame header before the image data")
		}
		if i+2 > len(b) {
			return 0, 0, errors.New("no frame header in its header")
		}
		n := int(b[i])<<8 | int(b[i+1])
		if n < 2 {
			return 0, 0, fmt.Errorf("a segment length of %d", n)
		}
		if m >= 0xc0 && m <= 0xcf && m != 0xc4 && m != 0xc8 && m != 0xcc {
			// Length, precision, height, width.
			if n < 8 || i+7 > len(b) {
				return 0, 0, errors.New("a frame header cut short")
			}
			return int(b[i+5])<<8 | int(b[i+6]), int(b[i+3])<<8 | int(b[i+4]), nil
		}
		i += n
	}
}
