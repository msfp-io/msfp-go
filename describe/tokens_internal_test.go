package describe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
)

// A codec or format name as the registry's token: the producer-side
// aliases mapped (section 4.4); "none", "unknown" and what isn't a token,
// "".
func TestCodecToken(t *testing.T) {
	for in, want := range map[string]string{
		"dca": "dts", "srt": "subrip", "text": "subrip", "aac": "aac", "hdmv_pgs_subtitle": "hdmv_pgs_subtitle",
		"none": "", "unknown": "", "": "", "Has Space": "", "UPPER": "", "x/../y": "",
	} {
		if got := codecToken(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// An error opening a file never carries its path: a *PathError's or a
// *LinkError's cause alone, wrapped or not; other errors as they are.
func TestPathless(t *testing.T) {
	_, err := os.Open("/nowhere/secret-title.mkv")
	link := &os.LinkError{Op: "link", Old: "/a/secret", New: "/b/secret", Err: syscall.EXDEV}
	other := errors.New("plain")
	for _, c := range []struct {
		in   error
		want error
	}{
		{err, fs.ErrNotExist},
		{fmt.Errorf("wrapped: %w", err), fs.ErrNotExist},
		{link, syscall.EXDEV},
		{other, other},
	} {
		got := pathless(c.in)
		if !errors.Is(got, c.want) || strings.Contains(got.Error(), "secret") {
			t.Errorf("%v: %v", c.in, got)
		}
	}
}
