package wire

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
)

// The queue of a play (MSFP music section 5.2): the tracks a play names
// after its title, which the owning server prepares under the same
// playback, and what the viewer's server keeps of the answer's entries.

// MaxQueue is how many tracks a play's queue names at most, and how many
// entries of an answer's queue are kept.
const MaxQueue = 5

// HLSMimeType is the only mimeType an hls stream may name.
const HLSMimeType = "application/vnd.apple.mpegurl"

// ErrQueue is a queue the owning server refuses: 400 invalid-request.
var ErrQueue = errors.New("federation: a queue outside what music section 5.2 allows")

// CheckQueue is the owning server's check of a music play's title and
// queue (the request's raw member; empty when absent): the queue's
// track keys, in order, or ErrQueue, to refuse the request with 400,
// when title isn't a track key or queue isn't an array of strings, holds
// more than MaxQueue entries, or holds an entry that isn't a track key in
// canonical form, title itself or a repeat. A queued track the owning
// server doesn't offer the caller, or can't play, isn't refused: it is
// left out of the answer, its place kept empty.
func CheckQueue(title string, queue json.RawMessage) ([]string, error) {
	if !IsMusicKey(title, "track") {
		return nil, ErrQueue
	}
	if len(queue) == 0 {
		return nil, nil
	}
	var list []json.RawMessage
	if queue[0] != '[' || json.Unmarshal(queue, &list) != nil || len(list) > MaxQueue {
		return nil, ErrQueue
	}
	keys := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := musicString(e)
		if !ok || !IsMusicKey(s, "track") || s == title || slices.Contains(keys, s) {
			return nil, ErrQueue
		}
		keys = append(keys, s)
	}
	return keys, nil
}

// QueueEntry is an entry of a play answer's queue: a queued track
// prepared, the copy playing, how, and its stream. DurationMs is nil
// when absent.
type QueueEntry struct {
	Title      string      `json:"title"`
	VersionID  string      `json:"versionId"`
	Method     string      `json:"method"`
	Stream     QueueStream `json:"stream"`
	DurationMs *int64      `json:"durationMs,omitempty"`
}

// QueueStream is a queue entry's stream: its URL (QueueURL), its protocol
// (hls or file), and its mimeType when sent.
type QueueStream struct {
	URL      string `json:"url"`
	Protocol string `json:"protocol"`
	MimeType string `json:"mimeType,omitempty"`
}

// QueueURL is the stream URL of the queued track at place n (0 to 4) of
// the request's queue, under the playback's ticket (media section 1):
// <stream root><ticket>/queue/<n>/master.m3u8 for hls, with the files it
// names under <stream root><ticket>/queue/<n>/, or .../queue/<n>/file
// for file.
func QueueURL(root, ticket string, n int, protocol string) string {
	name := "master.m3u8"
	if protocol == "file" {
		name = "file"
	}
	return root + ticket + "/queue/" + strconv.Itoa(n) + "/" + name
}

// ReadQueue is what the viewer's server keeps of a play answer's queue
// (raw; empty when absent, which keeps none, as does a queue that isn't
// an array), given the request's queue (asked), its delivery ("" when
// absent), the owning server's stream root and the answer's own
// stream.url. An entry is dropped alone when it isn't an object; when
// its title isn't one of asked, after the place of the entry kept before
// it (out of order, a repeat, the request's title); when its versionId
// hasn't ID's form, its method isn't direct, remux or transcode (with a
// delivery, the one it asks: direct for file), or its stream isn't an
// object of string url and protocol (and mimeType, when sent); when its
// url isn't exactly QueueURL for its protocol and its place in asked,
// under the ticket of streamURL (with none, every entry is dropped); when
// an hls stream names another mimeType than HLSMimeType, a file stream is
// a remux or a transcode, or delivery file is served as hls. durationMs
// outside 0 to MaxMusicDurationMs is kept as 0, one that isn't an
// integer as absent. At most MaxQueue entries are kept.
func ReadQueue(raw json.RawMessage, asked []string, delivery, root, streamURL string) []QueueEntry {
	kept := []QueueEntry{}
	ticket := queueTicket(streamURL, root)
	var list []json.RawMessage
	if ticket == "" || len(raw) == 0 || raw[0] != '[' || json.Unmarshal(raw, &list) != nil {
		return kept
	}
	method := map[string]string{"file": "direct", "remux": "remux", "transcode": "transcode"}[delivery]
	next := 0 // the first place of asked an entry may take
	for _, e := range list {
		if len(kept) == MaxQueue {
			break
		}
		var o map[string]json.RawMessage
		if json.Unmarshal(e, &o) != nil || o == nil {
			continue
		}
		title, _ := musicString(o["title"])
		at := slices.Index(asked[min(next, len(asked)):], title)
		if at < 0 {
			continue
		}
		at += next
		entry, ok := readQueueEntry(o, method, delivery)
		if !ok || entry.Stream.URL != QueueURL(root, ticket, at, entry.Stream.Protocol) {
			continue
		}
		entry.Title = title
		kept = append(kept, entry)
		next = at + 1
	}
	return kept
}

// readQueueEntry reads an entry's versionId, method, stream and
// durationMs; false drops it. method is the one delivery asks, "" for
// any.
func readQueueEntry(o map[string]json.RawMessage, method, delivery string) (QueueEntry, bool) {
	var e QueueEntry
	e.VersionID, _ = musicString(o["versionId"])
	e.Method, _ = musicString(o["method"])
	if !ID(e.VersionID) || !slices.Contains([]string{"direct", "remux", "transcode"}, e.Method) ||
		method != "" && e.Method != method {
		return QueueEntry{}, false
	}
	var s map[string]json.RawMessage
	if json.Unmarshal(o["stream"], &s) != nil || s == nil {
		return QueueEntry{}, false
	}
	var ok1, ok2 bool
	e.Stream.URL, ok1 = musicString(s["url"])
	e.Stream.Protocol, ok2 = musicString(s["protocol"])
	mime, sent := s["mimeType"]
	if sent {
		var ok bool
		if e.Stream.MimeType, ok = musicString(mime); !ok {
			return QueueEntry{}, false
		}
	}
	if !ok1 || !ok2 {
		return QueueEntry{}, false
	}
	switch e.Stream.Protocol {
	case "hls":
		if sent && e.Stream.MimeType != HLSMimeType || delivery == "file" {
			return QueueEntry{}, false
		}
	case "file":
		if e.Method != "direct" {
			return QueueEntry{}, false
		}
	default:
		return QueueEntry{}, false
	}
	if d, present := o["durationMs"]; present && isInteger(string(d)) {
		n, ok := musicInt(d, 0, MaxMusicDurationMs)
		if !ok {
			n = 0
		}
		e.DurationMs = &n
	}
	return e, true
}

// queueTicket is the ticket of a play answer's own stream URL,
// <root><ticket>/file or <root><ticket>/master.m3u8; "" when it names
// none.
func queueTicket(streamURL, root string) string {
	for _, protocol := range []string{"file", "hls"} {
		if base, ok := StreamBase(streamURL, root, protocol); ok {
			return strings.TrimSuffix(strings.TrimPrefix(base, root), "/")
		}
	}
	return ""
}
