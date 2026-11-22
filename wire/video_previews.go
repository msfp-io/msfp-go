package wire

import (
	"errors"
	"fmt"
)

// Seek-bar previews (MSFP video sections 5.5 and 5.6): the previews
// call's request and its answer, a ticket and the copy's etag, as each
// side checks them. The layout is the catalog's (PreviewLayout).

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

// PreviewsAnswer is a previews answer as kept (video section 5.6): the
// ticket the copy's sheets are fetched under, and the copy's etag as the
// owning server holds it now. When the etag isn't the catalog's, the
// viewer's server reads the title again before fetching sheets.
type PreviewsAnswer struct {
	Ticket string `json:"ticket"`
	ETag   string `json:"etag"`
}

// ReadPreviewsAnswer reads a previews answer (video section 5.6): an
// error refuses it, and no sheet is fetched: not an object, null at its
// top, or a ticket or an etag missing or not of an ID's form. Other
// members (a trickplay object of earlier drafts) are ignored.
func ReadPreviewsAnswer(body []byte) (PreviewsAnswer, error) {
	tree, err := strictTree(body, true)
	if err != nil {
		return PreviewsAnswer{}, err
	}
	o, ok := tree.(map[string]any)
	if !ok {
		return PreviewsAnswer{}, errors.New("not an object")
	}
	for _, v := range o {
		if v == nil {
			return PreviewsAnswer{}, errors.New("null at the top of an answer")
		}
	}
	a := PreviewsAnswer{}
	a.Ticket, _ = o["ticket"].(string)
	a.ETag, _ = o["etag"].(string)
	if !ID(a.Ticket) {
		return PreviewsAnswer{}, errors.New("ticket missing or not 1 to 64 characters of A-Z a-z 0-9 _ -")
	}
	if !ID(a.ETag) {
		return PreviewsAnswer{}, errors.New("etag missing or not 1 to 64 characters of A-Z a-z 0-9 _ -")
	}
	return a, nil
}
