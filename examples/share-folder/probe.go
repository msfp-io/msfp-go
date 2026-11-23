package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/msfp-io/msfp-go/describe"
	"github.com/msfp-io/msfp-go/wire"
)

// probes describe video files as MSFP video section 3.2 lists a copy,
// with the describe package: its media type from its first bytes, its
// length, its picture, its tracks and chapters from ffprobe when it is
// installed (two runs, the codec records read, attachments never),
// shaped into MSFP's vocabularies; its etag from its content. Without
// ffprobe, or for a file it can't read, a copy says only what every file
// says (its size, its media type, its etag) and its length is 0. What a
// file gave is kept by path, size and time, so a file is read once until
// it changes.
type probes struct {
	mu   sync.Mutex
	done map[string]probed
	p    describe.Prober
}

type probed struct {
	size    int64
	modTime time.Time
	copy    wire.Copy
	streams int // the file's streams, numbered from 0
}

// describe is what the file at path says of itself: a copy with its
// media type, etag, length, picture, audio and subtitle tracks and
// chapters; and how many streams it has. The rest is the caller's.
func (p *probes) describe(path string, st os.FileInfo) (wire.Copy, int) {
	p.mu.Lock()
	if d, ok := p.done[path]; ok && d.size == st.Size() && d.modTime.Equal(st.ModTime()) {
		p.mu.Unlock()
		return d.copy, d.streams
	}
	p.mu.Unlock()
	var c wire.Copy
	streams := 0
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if pr, err := p.p.Probe(ctx, path); err == nil {
		c = describe.Shape(pr, nil)
		streams = pr.Format.Streams
		for _, s := range pr.Streams {
			streams = max(streams, s.Index+1)
		}
	} else {
		c = wire.Copy{AudioTracks: []wire.CopyAudio{}, MediaType: mediaTypeOf(path)}
	}
	c.ETag = contentTag(path)
	p.mu.Lock()
	if p.done == nil {
		p.done = map[string]probed{}
	}
	p.done[path] = probed{size: st.Size(), modTime: st.ModTime(), copy: c, streams: streams}
	p.mu.Unlock()
	return c, streams
}

// mediaTypeOf is the media type of a file ffprobe didn't read: from its
// first bytes where they tell it, else from its extension, else
// video/mp4.
func mediaTypeOf(path string) string {
	head := make([]byte, describe.SniffSize)
	if f, err := os.Open(path); err == nil {
		n, _ := io.ReadFull(f, head)
		head = head[:n]
		f.Close()
	}
	if t := describe.Sniff(head); t != "" {
		return t
	}
	if t := describe.ContainerType(strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")); t != "" {
		return t
	}
	return "video/mp4"
}

// contentTag is the file's etag: from its content, so that it changes
// whenever its bytes change, and only then (MSFP video section 3.2):
// the first 16 bytes of its SHA-256, base64url. "" when it can't be
// read.
func contentTag(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:16])
}
