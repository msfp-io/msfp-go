package msfp

import "testing"

// A peer linked by hand and through groups, or through several groups, is
// given the most generous of its relations' limits (MSFP playback section
// 5): the higher count of each; for a bitrate or a download speed, none
// (0) over any cap; downloads a day go with the downloads they allow (a
// pool that allows none doesn't lend its daily cap); audio if any allows
// it. A peer by hand only, or in one group only, keeps that one's.
func TestGenerousLimits(t *testing.T) {
	hand := Limits{Streams: 2, Transcodes: 1, BitrateKbps: 8000, Downloads: 0, DownloadsPerDay: 9, ScansPerDay: 1, ScanBytes: 100}
	family := Limits{Streams: 1, Transcodes: 3, BitrateKbps: 0, Downloads: 2, DownloadsPerDay: 5, DownloadKbps: 4000, Audio: true, AudioStreams: 2}
	friends := Limits{Streams: 4, Transcodes: 0, BitrateKbps: 20000, Downloads: 1, DownloadsPerDay: 10, DownloadKbps: 0, ScanBytes: 500, AudioTranscodes: 1}

	byHand := &PeerRecord{Limits: hand}
	if got := generous(byHand); got != hand {
		t.Errorf("by hand only: %+v", got)
	}
	oneGroup := &PeerRecord{NotByHand: true, Pools: []GroupPool{{Group: "g1", Limits: family}}}
	if got := generous(oneGroup); got != family {
		t.Errorf("one group only: %+v", got)
	}
	if got := generous(&PeerRecord{NotByHand: true}); got != (Limits{}) {
		t.Errorf("no relation: %+v", got)
	}

	all := &PeerRecord{Limits: hand, Pools: []GroupPool{{Group: "g1", Limits: family}, {Group: "g2", Limits: friends}}}
	want := Limits{
		Streams: 4, Transcodes: 3,
		BitrateKbps:     0, // the family's has no cap
		Downloads:       2,
		DownloadsPerDay: 10, // the hand's 9 came with no downloads at all
		DownloadKbps:    0,  // the friends' has no cap
		ScansPerDay:     1, ScanBytes: 500,
		Audio: true, AudioStreams: 2, AudioTranscodes: 1,
	}
	if got := generous(all); got != want {
		t.Errorf("all three:\n got %+v\nwant %+v", got, want)
	}
	// The order of the relations changes nothing.
	reversed := &PeerRecord{NotByHand: true, Pools: []GroupPool{{Group: "g2", Limits: friends}, {Group: "g1", Limits: family}}}
	forward := &PeerRecord{NotByHand: true, Pools: []GroupPool{{Group: "g1", Limits: family}, {Group: "g2", Limits: friends}}}
	if generous(reversed) != generous(forward) {
		t.Errorf("order matters: %+v vs %+v", generous(reversed), generous(forward))
	}
	// A daily cap of a pool without downloads never limits one with them.
	a := Limits{Downloads: 0, DownloadsPerDay: 1}
	b := Limits{Downloads: 3, DownloadsPerDay: 0}
	if got := generous(&PeerRecord{NotByHand: true, Pools: []GroupPool{{Group: "a", Limits: a}, {Group: "b", Limits: b}}}); got.Downloads != 3 || got.DownloadsPerDay != 0 {
		t.Errorf("downloads with no daily cap: %+v", got)
	}
}
