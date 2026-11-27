package wire

import "testing"

func TestTrackID(t *testing.T) {
	for id, want := range map[string]int{"a0": 0, "a1": 1, "a10": 10, "a999": 999} {
		if n, ok := TrackID(id, 'a'); !ok || n != want {
			t.Errorf("%s: %d %v", id, n, ok)
		}
	}
	for _, id := range []string{"", "a", "a1000", "a+1", "s1", "A1", "a-1", "a1 ", "a01", "a00", "a007"} {
		if _, ok := TrackID(id, 'a'); ok {
			t.Errorf("%q accepted", id)
		}
	}
}

// Every media URL is built from the stream root, the ticket and a
// fixed name (MSFP Media fetching 1, MSFP Media video 6.1).
func TestStreamURLs(t *testing.T) {
	root := StreamRoot("https://b.example.org")
	if root != "https://b.example.org/api/v1/stream/" {
		t.Fatal(root)
	}
	base := TicketBase(root, "T1")
	for got, want := range map[string]string{
		StreamURL(root, "T1", ProtocolHLS):    root + "T1/master.m3u8",
		StreamURL(root, "T1", ProtocolFile):   root + "T1/file",
		SubtitleURL(base, "s4", FormText):     root + "T1/subtitles/s4",
		SubtitleURL(base, "s4", FormASS):      root + "T1/ass/s4",
		SubtitleURL(base, "s6", FormPicture):  root + "T1/pictures/s6",
		FontURL(base, 2):                      root + "T1/fonts/2",
		SheetURL(base, 11):                    root + "T1/trickplay/11",
		QueueURL(root, "T1", 2, ProtocolFile): root + "T1/queue/2/file",
		ImageURL(root, "T1"):                  root + "T1/image",
	} {
		if got != want {
			t.Errorf("%s, want %s", got, want)
		}
	}
}

func TestPreviewLayoutBounds(t *testing.T) {
	ok := PreviewLayout{TileWidth: 320, TileHeight: 180, Columns: 10, Rows: 10, IntervalMs: 10_000, TileCount: 250}
	if !ok.InBounds() || ok.SheetCount() != 3 {
		t.Fatal("refused", ok.SheetCount())
	}
	for _, bad := range []func(*PreviewLayout){
		func(t *PreviewLayout) { t.TileWidth = 0 },
		func(t *PreviewLayout) { t.TileWidth = 410 }, // 4,100 pixels across
		func(t *PreviewLayout) { t.Rows = 101 },
		func(t *PreviewLayout) { t.IntervalMs = 99 },
		func(t *PreviewLayout) { t.IntervalMs = 3_600_001 },
		func(t *PreviewLayout) { t.TileCount = 0 },
		func(t *PreviewLayout) { t.Columns, t.Rows, t.TileCount = 1, 1, 1001 },
	} {
		tp := ok
		bad(&tp)
		if tp.InBounds() {
			t.Errorf("%+v accepted", tp)
		}
	}
}

func TestRelayedMediaType(t *testing.T) {
	for in, want := range map[string]string{
		"video/mp4": "video/mp4", "Video/MP2T; charset=x": "video/mp2t", "audio/mp4+x.y-z": "audio/mp4+x.y-z",
		"audio/mpegurl": "application/octet-stream", "application/vnd.apple.mpegurl": "application/octet-stream",
		"text/html": "application/octet-stream", "video/": "application/octet-stream", "video/a_b": "application/octet-stream",
		"video/a b": "application/octet-stream", "": "application/octet-stream", "video/mp4/x": "application/octet-stream",
	} {
		if got := RelayedMediaType(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestRelayedFileType(t *testing.T) {
	for in, want := range map[string]string{"video/matroska": "video/matroska", "application/mp4": "application/mp4",
		"": "application/octet-stream", "video/MP4": "application/octet-stream"} {
		if got := RelayedFileType(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
