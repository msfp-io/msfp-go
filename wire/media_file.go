package wire

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// The file and segments, read by byte ranges (MSFP Media fetching 2.1,
// 3.2 and 4.2): the Range a request may carry, the answers a viewer's
// server relays, and the cap of a scan ticket.

// ByteRange is a well-formed Range header: bytes First to Last, both
// included (Last -1: to the end), or, when Suffix is above 0, the last
// Suffix bytes.
type ByteRange struct {
	First, Last int64
	Suffix      int64
}

var rangeForm = regexp.MustCompile(`^bytes=(?:(\d{1,15})-(\d{1,15})?|-(\d{1,15}))$`)

// ParseRange reads a Range header in one of MSFP Media fetching 2.1's three
// forms, bytes=<first>-<last> (first ≤ last), bytes=<first>- and
// bytes=-<count> (count ≥ 1): numbers of 1 to 15 digits, leading zeros
// accepted; false for any other, several ranges and bytes=- included.
// A malformed Range is answered 416, without reading the file.
func ParseRange(h string) (ByteRange, bool) {
	m := rangeForm.FindStringSubmatch(h)
	if m == nil {
		return ByteRange{}, false
	}
	n := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
	switch {
	case m[3] != "":
		r := ByteRange{Suffix: n(m[3])}
		return r, r.Suffix >= 1
	case m[2] != "":
		r := ByteRange{First: n(m[1]), Last: n(m[2])}
		return r, r.First <= r.Last
	}
	return ByteRange{First: n(m[1]), Last: -1}, true
}

// Span is what r asks of size bytes: start and end, both included; false
// when it can't be satisfied (a 416): its first at or past the end, or
// any range of an empty file. A last past the end means the end, a count
// above the size the whole.
func (r ByteRange) Span(size int64) (start, end int64, ok bool) {
	if r.Suffix > 0 {
		return max(0, size-r.Suffix), size - 1, size > 0
	}
	if r.First >= size {
		return 0, 0, false
	}
	end = size - 1
	if r.Last >= 0 && r.Last < end {
		end = r.Last
	}
	return r.First, end, true
}

// ContentRange is a 206's Content-Range: bytes <start>-<end>/<size>.
func ContentRange(start, end, size int64) string {
	return fmt.Sprintf("bytes %d-%d/%d", start, end, size)
}

// UnsatisfiedRange is a 416's Content-Range for the file: bytes */<size>.
func UnsatisfiedRange(size int64) string { return fmt.Sprintf("bytes */%d", size) }

// MediaAnswer is what a viewer's server checks of an answer for the file
// or a segment: its status and headers, "" for one absent.
type MediaAnswer struct {
	Status        int
	ETag          string
	ContentRange  string
	ContentLength string
}

var contentRangeForm = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)

func fileRefused(why string) error { return errors.New("msfp: the file's answer refused: " + why) }

// CheckFileAnswer is what a viewer's server checks of an answer for the
// file (MSFP Media fetching 2.1, 2.3 and 3.2), to the Range it sent ("" for
// none, never a malformed one), of a copy of size bytes and the given
// etag: a 200 only without Range, with Content-Length the size; a 206
// only to a Range, answering it (checkPartial), its total the size; an
// ETag of the etag, strong, on both. Anything else is a failed fetch, a
// 416 included: the viewer's server may pass a 416 to a range its viewer
// asked for on as a 416 of its own. A different ETag or size means the
// copy changed.
func CheckFileAnswer(rangeHeader string, size int64, etag string, a MediaAnswer) error {
	var r ByteRange
	if rangeHeader != "" {
		var ok bool
		if r, ok = ParseRange(rangeHeader); !ok {
			return fileRefused("a malformed Range, never sent")
		}
	}
	switch {
	case a.Status == 200 && rangeHeader != "":
		return fileRefused("a 200 to a Range")
	case a.Status == 206 && rangeHeader == "":
		return fileRefused("a 206 without a Range")
	case a.Status != 200 && a.Status != 206:
		return fileRefused(fmt.Sprintf("status %d", a.Status))
	}
	if a.ETag != `"`+etag+`"` {
		return fileRefused("an ETag other than the copy's: the copy changed")
	}
	if a.Status == 200 {
		if a.ContentLength != strconv.FormatInt(size, 10) {
			return fileRefused("a Content-Length other than the copy's size")
		}
		return nil
	}
	total, err := checkPartial(r, a)
	if err != nil {
		return err
	}
	if total != size {
		return fileRefused("a total other than the copy's size: the copy changed")
	}
	return nil
}

// CheckSegmentAnswer is what a viewer's server checks of an answer for a
// segment (MSFP Media fetching 2.1), to the Range it sent ("" for none): a 200
// is the whole segment, accepted with or without Range; a 206, only to a
// Range, answers it (checkPartial), its total a number or "*" (unknown;
// never with a suffix range, whose start can't be checked).
func CheckSegmentAnswer(rangeHeader string, a MediaAnswer) error {
	switch {
	case a.Status == 200:
		return nil
	case a.Status != 206:
		return fileRefused(fmt.Sprintf("status %d", a.Status))
	case rangeHeader == "":
		return fileRefused("a 206 without a Range")
	}
	r, ok := ParseRange(rangeHeader)
	if !ok {
		return fileRefused("a malformed Range, never sent")
	}
	_, err := checkPartial(r, a)
	return err
}

// checkPartial checks a 206's Content-Range against the range r asked,
// and its Content-Length when present: bytes <start>-<end>/<total>,
// start ≤ end < total; start the range's first, or total − count (or
// 0); end at most its last; Content-Length end − start + 1. It gives the
// total, -1 for "*".
func checkPartial(r ByteRange, a MediaAnswer) (int64, error) {
	m := contentRangeForm.FindStringSubmatch(a.ContentRange)
	if m == nil {
		return 0, fileRefused("a Content-Range not of the form bytes <start>-<end>/<total>")
	}
	start, e1 := strconv.ParseInt(m[1], 10, 64)
	end, e2 := strconv.ParseInt(m[2], 10, 64)
	total, e3 := int64(-1), error(nil)
	if m[3] != "*" {
		total, e3 = strconv.ParseInt(m[3], 10, 64)
	}
	if e1 != nil || e2 != nil || e3 != nil {
		return 0, fileRefused("a Content-Range number too large")
	}
	if start > end || total >= 0 && end >= total {
		return 0, fileRefused("a Content-Range without start ≤ end < total")
	}
	want := r.First
	if r.Suffix > 0 {
		if total < 0 {
			return 0, fileRefused("a suffix range answered without its total")
		}
		want = max(0, total-r.Suffix)
	}
	if start != want {
		return 0, fileRefused("a Content-Range start other than the range's")
	}
	if r.Suffix == 0 && r.Last >= 0 && end > r.Last {
		return 0, fileRefused("a Content-Range end past the range's last")
	}
	if a.ContentLength != "" && a.ContentLength != strconv.FormatInt(end-start+1, 10) {
		return 0, fileRefused("a Content-Length other than end − start + 1")
	}
	return total, nil
}

// ScanSpan is what the owning server sends for a fetch under a scan
// ticket that has sent sent bytes so far, of a copy of size bytes, the
// ticket serving capBytes in all, a byte sent twice counted twice (its
// scanBytes: Limits.ScanCap; media section 4.2): a 206 of start to end,
// the range cut short at the cap when it would pass it. False is a 416,
// with UnsatisfiedRange(size): a fetch without Range or with a malformed
// one, one that can't be satisfied, or one once the cap is spent.
func ScanSpan(sent int64, rangeHeader string, size, capBytes int64) (start, end int64, ok bool) {
	r, ok := ParseRange(rangeHeader)
	if !ok || sent >= capBytes {
		return 0, 0, false
	}
	if start, end, ok = r.Span(size); !ok {
		return 0, 0, false
	}
	return start, min(end, start+(capBytes-sent)-1), true
}
