package msfp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The sink a host answers its own clients with (NewHTTPSink): only the
// headers MSFP needs; the copy's etag and Accept-Ranges with a 200 or a
// 206, never with a refusal; a 200 when nothing was started; nothing
// written once its playback ended.
func TestHTTPSink(t *testing.T) {
	w := httptest.NewRecorder()
	s := NewHTTPSink(context.Background(), w)
	s.(ETagSink).SetETag("etag1234")
	s.Start(http.StatusPartialContent, "video/matroska", 4, "bytes 0-3/10")
	s.Start(http.StatusOK, "text/plain", 99, "") // a second Start changes nothing
	if _, err := s.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	h := w.Header()
	if w.Code != 206 || h.Get("Content-Type") != "video/matroska" || h.Get("Content-Length") != "4" ||
		h.Get("Content-Range") != "bytes 0-3/10" || h.Get("Accept-Ranges") != "bytes" || h.Get("ETag") != `"etag1234"` || w.Body.String() != "abcd" {
		t.Errorf("206: %d %v %q", w.Code, h, w.Body.String())
	}

	w = httptest.NewRecorder()
	s = NewHTTPSink(context.Background(), w)
	s.(ETagSink).SetETag("etag1234")
	s.Start(http.StatusRequestedRangeNotSatisfiable, "", 0, "bytes */10")
	if w.Code != 416 || w.Header().Get("ETag") != "" || w.Header().Get("Accept-Ranges") != "" || w.Header().Get("Content-Range") != "bytes */10" {
		t.Errorf("416: %d %v", w.Code, w.Header())
	}

	w = httptest.NewRecorder()
	s = NewHTTPSink(context.Background(), w)
	s.Write([]byte("x"))
	if w.Code != 200 || w.Header().Get("Content-Length") != "" || w.Body.String() != "x" {
		t.Errorf("written without a Start: %d %v", w.Code, w.Header())
	}

	ctx, cancel := context.WithCancel(context.Background())
	w = httptest.NewRecorder()
	s = NewHTTPSink(ctx, w)
	s.Start(http.StatusOK, "video/mp4", -1, "")
	cancel()
	if n, err := s.Write([]byte("more")); err == nil || n != 0 || w.Body.Len() != 0 {
		t.Errorf("after the end: %d %v %q", n, err, w.Body.String())
	}
}

// Only a strong ETag of MSFP's ID form is taken from an answer.
func TestStrongETag(t *testing.T) {
	for h, want := range map[string]string{
		`"etag1234"`:   "etag1234",
		`W/"etag1234"`: "",
		`etag1234`:     "",
		`"etag1234`:    "",
		`""`:           "",
		`"a b"`:        "",
		`"a/b"`:        "",
		``:             "",
	} {
		if got := strongETag(h); got != want {
			t.Errorf("%q: %q, want %q", h, got, want)
		}
	}
}
