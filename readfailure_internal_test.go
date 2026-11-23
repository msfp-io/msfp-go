package msfp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/msfp-io/msfp-go/wire"
)

// What the previous draft's pages look like, and what isn't enough to
// say so.
func TestOlderDraftPage(t *testing.T) {
	for _, c := range []struct {
		page         string
		old, current bool
	}{
		{`[{"key":"movie/tmdb/949","kind":"movie","library":"l"}]`, true, false},
		{`[{"key":"series/tmdb/1","kind":"series","library":"l"},{"key":"series/tmdb/1/s1/e2","kind":"episode","library":"l","audio":["en"]}]`, true, false},
		{`[{"key":"track/mb/0c000000-0000-4000-8000-000000000003","kind":"track","library":"l","name":"x","durationMs":1}]`, true, false},
		// Series and seasons only: nothing tells.
		{`[{"key":"series/tmdb/1","kind":"series","library":"l"}]`, false, false},
		{`[]`, false, false},
		// One title without kind, or a kind that isn't its key's.
		{`[{"key":"movie/tmdb/949","kind":"movie","library":"l"},{"key":"movie/tmdb/603","library":"l"}]`, false, false},
		{`[{"key":"movie/tmdb/949","kind":"series","library":"l"}]`, false, false},
		{`[{"key":"movie/tmdb/949","kind":7,"library":"l"}]`, false, false},
		// A key this draft doesn't read.
		{`[{"key":"movie/tmdb/0949","kind":"movie","library":"l"}]`, false, false},
		// Copies on a film: this draft's, whatever else.
		{`[{"key":"movie/tmdb/949","kind":"movie","library":"l"},{"key":"movie/tmdb/603","kind":"movie","library":"l","copies":[]}]`, false, true},
		{`[{"key":"movie/tmdb/603","library":"l","copies":[]}]`, false, true},
	} {
		var titles []json.RawMessage
		if err := json.Unmarshal([]byte(c.page), &titles); err != nil {
			t.Fatal(err)
		}
		if old, current := olderDraftPage(titles); old != c.old || current != c.current {
			t.Errorf("%s: old %v, current %v", c.page, old, current)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

// Each engine error a reading may end with, and its code.
func TestReadFailureCodes(t *testing.T) {
	e := &Engine{}
	paired := &PeerRecord{ID: "p"}
	noCore := &PeerRecord{ID: "q", Versions: &wire.Versions{Core: nil}}
	for _, c := range []struct {
		err     error
		p       *PeerRecord
		want    ReadFailure
		refusal string
		keep    bool
	}{
		{nil, paired, "", "", true},
		{ErrBackingOff, paired, "", "", false},
		{ErrUnreachable.with(fmt.Errorf("%w: %w", errNoConnection, context.Canceled)), paired, "", "", false},
		{ErrUnreachable.with(fmt.Errorf("%w: %w", errNoConnection, errors.New("connection refused"))), paired, ReadUnreachable, "", true},
		{ErrUnreachable.with(fmt.Errorf("%w: %w", errNoConnection, timeoutErr{})), paired, ReadTimeout, "", true},
		{context.DeadlineExceeded, paired, ReadTimeout, "", true},
		{ErrCertificateRefused.with(errors.New("https://x")), paired, ReadCertificateRefused, "", true},
		{ErrRefusedDestination, paired, ReadUnreachable, "", true},
		{ErrUnreachable.with(fmt.Errorf("%w: 404", errNotHeard)), paired, ReadRefusedUnsigned, "", true},
		{ErrUnreachable.with(errUnsigned), paired, ReadRefusedUnsigned, "", true},
		{&Error{Code: "NotFound", Err: signedRefusal{404, "not-found"}}, paired, ReadRefused, "not-found", true},
		{&Error{Code: "StreamLimitReached", Err: signedRefusal{409, "<script>"}}, paired, ReadRefused, "", true},
		{&Error{Code: "NotSupported", Err: signedRefusal{404, "not-supported"}}, noCore, ReadRefused, "not-supported", true},
		{ErrNotPaired, paired, ReadNotPaired, "", true},
		{ErrKeyLost, paired, ReadNotPaired, "", true},
		{ErrSuspended, paired, ReadSuspended, "", true},
		{ErrUncallable, paired, ReadUncallable, "", true},
		{ErrNotSupported, paired, ReadCatalogOff, "", true},
		{ErrNotSupported, noCore, ReadNoCommonVersion, "", true},
		{ErrUnreachable.with(errOlderDraft), paired, ReadOlderDraft, "", true},
		{ErrUnreachable.with(fmt.Errorf("%w: x", errNotUnderstood)), paired, ReadNotUnderstood, "", true},
		{errors.New("the store failed"), paired, ReadInternal, "", true},
	} {
		f, refusal, keep := e.readFailure(c.p, c.err)
		if f != c.want || refusal != c.refusal || keep != c.keep {
			t.Errorf("%v: %q %q %v", c.err, f, refusal, keep)
		}
	}
}
