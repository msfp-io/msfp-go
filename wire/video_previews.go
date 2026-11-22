package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Seek-bar previews (MSFP video sections 5.5 and 5.6): the previews
// call's request, and the trickplay object of its answer and of play's,
// as the viewer's server checks them.

// PreviewsRequest is the body of a previews call (video section 5.6):
// the key of a film, an episode or an extra, and its copy's ID.
type PreviewsRequest struct {
	Title     string `json:"title"`
	VersionID string `json:"versionId"`
}

// ReadPreviewsRequest reads a previews request as the owning server
// does (video section 5.6): an error, answered 400, for a body that
// isn't an I-JSON object (null refused, DecodeJSON), a title that isn't
// the canonical key of a film, an episode or an extra, or a versionId
// that isn't of a copy ID's form. Whether the owning server offers the
// caller that title and copy, with previews, is its own to check (404
// otherwise); 404 not-supported when video is off comes first, before
// the body is read.
func ReadPreviewsRequest(body []byte) (PreviewsRequest, error) {
	var r struct {
		Title     *string `json:"title"`
		VersionID *string `json:"versionId"`
	}
	if err := DecodeJSON(body, &r); err != nil {
		return PreviewsRequest{}, err
	}
	switch k, ok := ParseVideoKey(deref(r.Title)); {
	case r.Title == nil:
		return PreviewsRequest{}, errors.New("title missing or not a string")
	case !ok:
		return PreviewsRequest{}, errors.New("title isn't a canonical video key")
	case !k.Playable():
		return PreviewsRequest{}, fmt.Errorf("title: a %s has no copies", k.Kind)
	case r.VersionID == nil:
		return PreviewsRequest{}, errors.New("versionId missing or not a string")
	case !ID(*r.VersionID):
		return PreviewsRequest{}, errors.New("versionId isn't 1 to 64 characters of A-Z a-z 0-9 _ -")
	}
	return PreviewsRequest{Title: *r.Title, VersionID: *r.VersionID}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Previews is a trickplay object as kept (video section 5.5): its
// numbers, in bounds (Trickplay.Sheets is len(URLs)), the ticket its
// sheets are under, and the sheets' URLs, sheet n's at index n, each
// exactly SheetURL(root+Ticket+"/", n). The viewer's server fetches them
// itself and never hands them to its people.
type Previews struct {
	Trickplay
	Ticket string
	URLs   []string
}

// The numbers of a trickplay object (video section 5.5).
var trickplayNumbers = []string{"tileWidth", "tileHeight", "columns", "rows", "intervalMs", "tileCount"}

// ReadTrickplay reads a trickplay object, of play's answer or of
// previews', against the owning server's stream root (video section
// 5.5). An error refuses the whole answer: trickplay isn't an object, a
// number is of another JSON type or isn't an integer, sheets isn't an
// array of strings, or a sheet's URL isn't exactly
// <root><ticket>/trickplay/<n> for its index n, all under one ticket. A
// refusal wins over a drop. nil, nil drops the previews, and the viewer's
// server goes on without them: a number missing or out of bounds
// (Trickplay.InBounds), or another number of sheets than the tiles fill.
// Unknown members are ignored.
func ReadTrickplay(raw json.RawMessage, root string) (*Previews, error) {
	tree, err := strictTree(raw, true)
	if err != nil {
		return nil, err
	}
	return readTrickplay(tree, root)
}

func readTrickplay(v any, root string) (*Previews, error) {
	o, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("trickplay isn't an object")
	}
	n := map[string]int{}
	for _, f := range trickplayNumbers {
		raw, present := o[f]
		if !present {
			continue
		}
		i, ok := videoInt(raw, math.MinInt64, math.MaxInt64)
		if !ok {
			return nil, fmt.Errorf("%s isn't an integer", f)
		}
		n[f] = int(i)
	}
	p := &Previews{Trickplay: Trickplay{
		TileWidth: n["tileWidth"], TileHeight: n["tileHeight"], Columns: n["columns"], Rows: n["rows"],
		IntervalMs: n["intervalMs"], TileCount: n["tileCount"],
	}}
	if raw, present := o["sheets"]; present {
		list, ok := raw.([]any)
		if !ok {
			return nil, errors.New("sheets isn't an array")
		}
		for i, e := range list {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("sheets[%d] isn't a string", i)
			}
			rest, _ := strings.CutPrefix(s, root)
			ticket, _, _ := strings.Cut(rest, "/")
			if !strings.HasPrefix(s, root) || !ID(ticket) || s != SheetURL(root+ticket+"/", i) {
				return nil, fmt.Errorf("sheets[%d] isn't <stream root><ticket>/trickplay/%d", i, i)
			}
			if i > 0 && ticket != p.Ticket {
				return nil, fmt.Errorf("sheets[%d] names another ticket", i)
			}
			p.Ticket = ticket
			p.URLs = append(p.URLs, s)
		}
	}
	p.Sheets = len(p.URLs)
	if !p.InBounds() {
		return nil, nil
	}
	return p, nil
}

// ReadPreviewsAnswer reads a previews answer (video section 5.6) against
// the owning server's stream root: an error refuses it (not an object,
// null at its top, or ReadTrickplay's refusals); nil, nil when it holds
// no previews, or ReadTrickplay drops them.
func ReadPreviewsAnswer(body []byte, root string) (*Previews, error) {
	tree, err := strictTree(body, true)
	if err != nil {
		return nil, err
	}
	o, ok := tree.(map[string]any)
	if !ok {
		return nil, errors.New("not an object")
	}
	for _, v := range o {
		if v == nil {
			return nil, errors.New("null at the top of an answer")
		}
	}
	tp, present := o["trickplay"]
	if !present {
		return nil, nil
	}
	return readTrickplay(tp, root)
}
