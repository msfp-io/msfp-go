package msfp_test

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// lastReading is B's view of how its last reading of A went.
func (s *sharing) lastReading(t *testing.T) msfp.LastReading {
	t.Helper()
	v, err := s.b.eng.Peer(context.Background(), s.onB.ID)
	if err != nil || v.Reading == nil {
		t.Fatalf("B's view of A: %+v, %v", v.Reading, err)
	}
	return *v.Reading
}

// librariesWithKinds rewrites A's libraries answers as the previous draft
// sent them: each library with a kind.
var libraryID = regexp.MustCompile(`("id":"[^"]*")`)

func librariesWithKinds(path string, body []byte) ([]byte, bool) {
	if !strings.HasSuffix(path, "/libraries") {
		return nil, false
	}
	return libraryID.ReplaceAll(body, []byte(`$1,"kind":"movies"`)), true
}

// A peer answering in the previous draft's shapes (libraries with kinds,
// titles with kinds and no copies) fails the reading as older-draft;
// what was kept stays. Either mark alone isn't enough.
func TestReadFailureOlderDraft(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})
	if r := s.lastReading(t); r.Failure != "" || r.At.IsZero() {
		t.Fatalf("a reading that read: %+v", r)
	}
	lib := s.libraryPub
	old := `{"titles":[{"key":"series/tmdb/1399","kind":"series","library":"` + lib + `"},` +
		`{"key":"movie/tmdb/949","kind":"movie","library":"` + lib + `","audio":["en"],"video":{"height":1080}},` +
		`{"key":"movie/tmdb/603","kind":"movie","library":"` + lib + `"}],"seq":4}`
	page := old
	libraries := true
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		if strings.HasSuffix(path, "/titles") {
			return []byte(page), true
		}
		if libraries {
			return librariesWithKinds(path, body)
		}
		return nil, false
	})
	_, err := s.b.eng.ReadNow(ctx, s.onB.ID)
	if !errors.Is(err, msfp.ErrUnreachable) {
		t.Fatalf("an older draft's answers: %v", err)
	}
	if r := s.lastReading(t); r.Failure != msfp.ReadOlderDraft || r.Refusal != "" {
		t.Errorf("its last reading: %+v", r)
	}
	if ev, ok := has[msfp.CatalogReadEnded](s.b.events); !ok || ev.Failure == "" {
		t.Errorf("the event: %+v", ev)
	}
	if keys := keysOf(s.peerTitles(t)); len(keys) != 1 || keys[0] != "movie/tmdb/1" {
		t.Errorf("B keeps %v", keys)
	}

	// The same titles, but the libraries of this draft: titles skipped
	// one by one, as a broken owner's; the reading reads.
	libraries = false
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if r := s.lastReading(t); r.Failure != "" {
		t.Errorf("without kinds in libraries: %+v", r)
	}
	sinceSent := func() bool {
		x := s.a.exchanges("titles")
		return !strings.Contains(x[len(x)-1].reqBody, `"since":0`)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil || !sinceSent() {
		t.Fatalf("a reading of changes: %v, since sent %v", err, sinceSent())
	}

	// Libraries with kinds, and a film with copies: this draft's.
	libraries = true
	page = `{"titles":[{"key":"movie/tmdb/2","kind":"movie","library":"` + lib + `","copies":[{"id":"v1","durationMs":1000,"mediaType":"video/matroska","size":5,"etag":"t1","audioTracks":[]}]},` +
		`{"key":"movie/tmdb/603","kind":"movie","library":"` + lib + `"}]}`
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	if r := s.lastReading(t); r.Failure != "" {
		t.Errorf("a film with copies: %+v", r)
	}
	if sinceSent() {
		t.Error("libraries with kinds, and a reading of changes: its titles might not tell")
	}
	// Titles without kinds: not the previous draft either.
	page = `{"titles":[{"key":"movie/tmdb/603","library":"` + lib + `"}]}`
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	// Only series and seasons on the page: nothing tells, the reading reads.
	page = `{"titles":[{"key":"series/tmdb/1399","kind":"series","library":"` + lib + `"}]}`
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
		t.Fatal(err)
	}
	// Back to the old shapes, read through TheirLibraries and the
	// scheduled readings' path: the same code.
	page = old
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil || s.lastReading(t).Failure != msfp.ReadOlderDraft {
		t.Errorf("again: %v %+v", err, s.lastReading(t))
	}
}

// Each other way a reading fails has its code; none holds an address.
func TestReadFailures(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	s := newSharing(t, h, msfp.Config{}, msfp.Config{})

	// An answer not of the call's form.
	s.a.setRewrite(func(path string, _ http.Header, body []byte) ([]byte, bool) {
		return []byte(`{"titles":{}}`), strings.HasSuffix(path, "/titles")
	})
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil || s.lastReading(t).Failure != msfp.ReadNotUnderstood {
		t.Errorf("not understood: %v %+v", err, s.lastReading(t))
	}
	s.a.setRewrite(nil)

	// Too slow for the host's deadline.
	s.a.onRequest(func(*http.Request, []byte) { time.Sleep(300 * time.Millisecond) })
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err := s.b.eng.ReadNow(short, s.onB.ID)
	cancel()
	if err == nil || s.lastReading(t).Failure != msfp.ReadTimeout {
		t.Errorf("timeout: %v %+v", err, s.lastReading(t))
	}
	// Cancelled by the host: not the peer's doing, nothing kept.
	before := s.lastReading(t)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	s.b.eng.ReadNow(cancelled, s.onB.ID)
	if r := s.lastReading(t); r != before {
		t.Errorf("a cancelled reading kept %+v", r)
	}
	s.a.onRequest(nil)
	time.Sleep(400 * time.Millisecond) // the slow answers out of the way

	// Plain refusals, unsigned: a proxy, or a server that lost its records.
	s.a.setDown(true)
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); err == nil || s.lastReading(t).Failure != msfp.ReadRefusedUnsigned {
		t.Errorf("unsigned: %v %+v", err, s.lastReading(t))
	}
	s.a.setDown(false)

	// No answer at all. (After calls not answered, the engine holds off:
	// ErrBackingOff keeps the failure before it.)
	s.a.srv.Close()
	_, err = s.b.eng.ReadNow(ctx, s.onB.ID)
	if r := s.lastReading(t); r.Failure != msfp.ReadUnreachable {
		t.Errorf("unreachable: %v %+v", err, r)
	}
	if _, err := s.b.eng.ReadNow(ctx, s.onB.ID); errors.Is(err, msfp.ErrBackingOff) && s.lastReading(t).Failure != msfp.ReadUnreachable {
		t.Errorf("backing off replaced the failure: %+v", s.lastReading(t))
	}
	for _, f := range msfp.ReadFailures {
		if !regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`).MatchString(string(f)) {
			t.Errorf("not a closed code: %q", f)
		}
	}
}

// A signed refusal is refused, with its problem type.
func TestReadFailureRefused(t *testing.T) {
	ctx := context.Background()
	h := newFakeHost()
	h.film("films", "movie/tmdb/1", "en")
	s := newSharing(t, h, msfp.Config{Limits: msfp.CallLimits{PeerRate: 0.001, PeerBurst: 8}}, msfp.Config{})
	var err error
	for range 10 {
		if _, err = s.b.eng.ReadNow(ctx, s.onB.ID); err != nil {
			break
		}
	}
	if !errors.Is(err, msfp.ErrRateLimited) {
		t.Fatalf("past A's limits: %v", err)
	}
	if r := s.lastReading(t); r.Failure != msfp.ReadRefused || r.Refusal != "rate-limited" {
		t.Errorf("refused: %+v", r)
	}
}
