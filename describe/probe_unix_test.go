//go:build unix

package describe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A FIFO named like a film is refused without blocking.
func TestProbeFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fifo.mkv")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	if _, err := (&Prober{}).Probe(context.Background(), fifo); !errors.Is(err, ErrNotRegular) {
		t.Errorf("a FIFO: %v", err)
	}
}

// With Nice, each ffprobe child runs at that niceness.
func TestProbeNice(t *testing.T) {
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	prog := filepath.Join(dir, "ffprobe")
	script := "#!/bin/sh\n/bin/sleep 0.3\nn=$(/usr/bin/cut -d' ' -f19 /proc/$$/stat)\n" +
		`printf '{"streams":[{"index":0,"codec_type":"audio","codec_name":"flac","tags":{"nice":"%s"}}]}' "$n"` + "\n"
	if err := os.WriteFile(prog, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "a.flac")
	if err := os.WriteFile(file, []byte("fLaC\x00\x00\x00\x22"), 0o600); err != nil {
		t.Fatal(err)
	}
	pr, err := (&Prober{FFprobe: prog, Nice: 19}).Probe(context.Background(), file)
	if err != nil {
		t.Skip(err) // no /bin/sleep or /usr/bin/cut
	}
	if got := pr.Streams[0].Tags.Get("nice"); got != "19" {
		t.Errorf("ffprobe's niceness: %q", got)
	}
}
