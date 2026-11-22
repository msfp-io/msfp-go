package wire

import (
	"encoding/json"
	"slices"
	"strconv"
)

// The queue of a play (MSFP music section 5.2): the tracks a play names
// after its title, which the owning server prepares under the same
// playback, each with its own copy and delivery when it names them, and
// what the viewer's server keeps of the answer's entries.

// MaxQueue is how many tracks a play's queue names at most, and how many
// entries of an answer's queue are kept.
const MaxQueue = 5

// QueueAsk is an entry of a play's queue (music section 5.2): a track's
// key, the copy it plays (VersionID; without it, its first) and its own
// delivery, with its own mediaType or transcode targets as a request's;
// without Delivery, the request's delivery, media type and targets.
type QueueAsk struct {
	Title     string            `json:"title"`
	VersionID string            `json:"versionId,omitempty"`
	Delivery  string            `json:"delivery,omitempty"`
	MediaType string            `json:"mediaType,omitempty"`
	Transcode *TranscodeTargets `json:"transcode,omitempty"`
}

// MusicRemuxTypes are the media types a track's remux takes (playback
// section 2.7, for a copy with no video).
var MusicRemuxTypes = []string{"audio/" + SegmentsTS, "audio/" + SegmentsMP4}

// CheckQueue is the owning server's check of a music play's title and
// queue (the request's raw member; empty when absent): the queue's
// entries, in order, or a *PlayRefusal. 400 when title isn't a track key
// or queue isn't an array of objects, holds more than MaxQueue entries,
// or an entry whose title isn't a track key in canonical form, is title
// itself or a repeat, whose versionId isn't of an ID's form, whose
// delivery isn't a token, whose mediaType comes without its own delivery
// remux or isn't a media type (or is missing with its own remux), or
// whose transcode comes without its own delivery transcode or isn't a
// transcode object (TranscodeTargets' rules); then, once no entry is
// malformed, 409 delivery-refused for an entry's delivery not of the
// list or a remux to another media type than MusicRemuxTypes. A queued
// track the owning server doesn't offer the caller, or can't play, isn't
// refused: it is left out of the answer, its place kept empty.
func CheckQueue(title string, queue json.RawMessage) ([]QueueAsk, error) {
	if !IsMusicKey(title, "track") {
		return nil, badPlay("title isn't a track key")
	}
	if len(queue) == 0 {
		return nil, nil
	}
	tree, err := strictTree(queue, true)
	list, ok := tree.([]any)
	if err != nil || !ok {
		return nil, badPlay("queue isn't an array")
	}
	if len(list) > MaxQueue {
		return nil, badPlay("a queue of more than 5 entries")
	}
	var asked []QueueAsk
	var refused error
	for i, e := range list {
		o, ok := e.(map[string]any)
		if !ok {
			return nil, badPlay("queue entry " + strconv.Itoa(i) + " isn't an object")
		}
		s, _ := o["title"].(string)
		if !IsMusicKey(s, "track") || s == title || slices.ContainsFunc(asked, func(a QueueAsk) bool { return a.Title == s }) {
			return nil, badPlay("queue entry " + strconv.Itoa(i) + ": title isn't another track's key")
		}
		a := QueueAsk{Title: s}
		if v, present := o["versionId"]; present {
			if a.VersionID, _ = v.(string); !ID(a.VersionID) {
				return nil, badPlay("queue entry " + strconv.Itoa(i) + ": versionId isn't a version ID")
			}
		}
		var r PlayRequest
		if err := members(o).readDelivery(&r); err != nil {
			return nil, err
		}
		if r.emptyTrack {
			return nil, badPlay("queue entry " + strconv.Itoa(i) + ": a malformed burnSubtitle")
		}
		a.Delivery, a.MediaType, a.Transcode = r.Delivery, r.MediaType, r.Transcode
		switch {
		case refused != nil:
		case a.Delivery != "" && !KnownDelivery(a.Delivery):
			refused = refusedDelivery("queue entry " + strconv.Itoa(i) + ": a delivery the owning server doesn't make")
		case a.Delivery == DeliveryRemux && !slices.Contains(MusicRemuxTypes, a.MediaType):
			refused = refusedDelivery("queue entry " + strconv.Itoa(i) + ": a remux to another media type than audio/mp2t or audio/mp4")
		}
		asked = append(asked, a)
	}
	if refused != nil {
		return nil, refused
	}
	return asked, nil
}

// Request is the play an entry of r's queue starts (music section 5.2):
// the entry's title and copy, its own delivery with its own media type
// or targets, else the request's; purpose, the caps and audio renditions
// are the request's. It starts at 0, on its default audio, and counts
// once, under the request. A 400 when r's purpose doesn't take the
// entry's own delivery (PurposeAllows).
func (a QueueAsk) Request(r PlayRequest) (PlayRequest, error) {
	e := PlayRequest{Title: a.Title, Purpose: r.Purpose, AudioRenditions: r.AudioRenditions,
		MaxBitrateKbps: r.MaxBitrateKbps, VersionID: a.VersionID,
		Delivery: r.Delivery, MediaType: r.MediaType, Transcode: r.Transcode}
	if a.Delivery != "" {
		e.Delivery, e.MediaType, e.Transcode = a.Delivery, a.MediaType, a.Transcode
	}
	if e.Transcode != nil {
		t := *e.Transcode
		e.Transcode = &t
	}
	if !PurposeAllows(e.Purpose, e.Delivery, e.Transcode) {
		return e, badPlay("purpose " + e.Purpose + " doesn't take the entry's delivery")
	}
	return e, nil
}

// QueueEntry is an entry of a play answer's queue, as kept: a queued
// track prepared, the copy playing, how, and its protocol. It carries no
// URL: QueueURL builds it from the playback's ticket and the entry's
// place in the request's queue (At).
type QueueEntry struct {
	Title     string `json:"title"`
	VersionID string `json:"versionId"`
	Method    string `json:"method"`
	Protocol  string `json:"protocol"`
	At        int    `json:"-"`
}

// QueueURL is the stream URL of the queued track at place n (0 to 4) of
// the request's queue, under the playback's ticket (media section 1):
// <stream root><ticket>/queue/<n>/master.m3u8 for hls, with the files it
// names under <stream root><ticket>/queue/<n>/, or .../queue/<n>/file
// for file.
func QueueURL(root, ticket string, n int, protocol string) string {
	return QueueStreamBase(TicketBase(root, ticket), n) + streamName(protocol)
}

func streamName(protocol string) string {
	if protocol == ProtocolFile {
		return NameFile
	}
	return NameMaster
}

// ReadQueue is what the viewer's server keeps of a play answer's queue
// (raw; empty when absent, which keeps none, as does a queue that isn't
// an array), given the request's queue (asked) and delivery ("" when
// absent). An entry is dropped alone when it isn't an object; when its
// title isn't one of asked, after the place of the entry kept before it
// (out of order, a repeat, the request's title); when its versionId
// hasn't ID's form, or isn't the one its entry named; when its method
// isn't direct, remux or transcode, or not the one its delivery (its
// own, else the request's) asks; when its protocol isn't the
// delivery's (file for file, hls otherwise). At most MaxQueue entries
// are kept.
func ReadQueue(raw json.RawMessage, asked []QueueAsk, delivery string) []QueueEntry {
	kept := []QueueEntry{}
	tree, err := strictTree(raw, true)
	list, ok := tree.([]any)
	if len(raw) == 0 || err != nil || !ok {
		return kept
	}
	next := 0 // the first place of asked an entry may take
	for _, e := range list {
		if len(kept) == MaxQueue {
			break
		}
		o, ok := e.(map[string]any)
		if !ok {
			continue
		}
		title, _ := o["title"].(string)
		at := -1
		for i := next; i < len(asked) && at < 0; i++ {
			if asked[i].Title == title {
				at = i
			}
		}
		if at < 0 {
			continue
		}
		d := delivery
		if asked[at].Delivery != "" {
			d = asked[at].Delivery
		}
		q := QueueEntry{Title: title, At: at}
		q.VersionID, _ = o["versionId"].(string)
		q.Method, _ = o["method"].(string)
		q.Protocol, _ = o["protocol"].(string)
		wantProtocol := ProtocolHLS
		if d == DeliveryFile {
			wantProtocol = ProtocolFile
		}
		if !ID(q.VersionID) || asked[at].VersionID != "" && q.VersionID != asked[at].VersionID ||
			!slices.Contains([]string{MethodDirect, MethodRemux, MethodTranscode}, q.Method) ||
			methodOf[d] != "" && q.Method != methodOf[d] || q.Protocol != wantProtocol {
			continue
		}
		kept = append(kept, q)
		next = at + 1
	}
	return kept
}
