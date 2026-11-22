package jsonfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/msfp-io/msfp-go"
)

// What a commit writes is there after reopening; a failed commit's
// changes aren't; a catalog whose peer is gone (a crash between two
// writes) is removed; no temporary file is left.
func TestReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	title := &msfp.VideoTitle{TitleCommon: msfp.TitleCommon{Key: "movie/tmdb/1", Kind: "movie"}}
	err = st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k"), Name: "one"})
		return tx.PutPeerTitles("p1", 1, []msfp.Title{title})
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p2", Key: []byte("k2")})
		return os.ErrInvalid
	})
	st.Close()
	if err := os.WriteFile(filepath.Join(dir, "peers", "p9.json"), []byte(`{"titles":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Read(ctx, func(tx msfp.Tx) error {
		peers, _ := tx.Peers()
		if len(peers) != 1 || peers[0].Name != "one" {
			t.Errorf("peers: %+v", peers)
		}
		if got, _ := tx.PeerTitle("p1", "movie/tmdb/1"); got == nil {
			t.Error("its title is lost")
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(dir, "peers", "p9.json")); !os.IsNotExist(err) {
		t.Error("a lone catalog kept")
	}
	tmp, _ := filepath.Glob(filepath.Join(dir, "*", ".tmp-*"))
	tmp2, _ := filepath.Glob(filepath.Join(dir, ".tmp-*"))
	if len(tmp)+len(tmp2) != 0 {
		t.Errorf("temporary files left: %v %v", tmp, tmp2)
	}
}

// Downloads are kept across reopening; a state.json written before they
// were (no "downloads") reads as none kept; a peer's go with it.
func TestDownloadsKept(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{"peers":{},"codes":{},"forgotten":{},"libraryIds":{},"leftovers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	played := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	err = st.Update(ctx, func(tx msfp.Tx) error {
		d := tx.(msfp.DownloadsTx)
		if got, _ := d.Downloads(); len(got) != 0 {
			t.Errorf("downloads of an older state.json: %+v", got)
		}
		tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k")})
		tx.PutPeer(msfp.PeerRecord{ID: "p2", Key: []byte("k2")})
		d.PutDownload(msfp.DownloadRecord{ID: "play_a", Peer: "p1", Title: "movie/tmdb/1", Handle: "h", PlayedAt: played})
		d.PutDownload(msfp.DownloadRecord{ID: "play_a", Viewer: true, Peer: "p1", TheirID: "x", Title: "movie/tmdb/2", PlayedAt: played})
		return d.PutDownload(msfp.DownloadRecord{ID: "play_b", Peer: "p2", Title: "movie/tmdb/3", PlayedAt: played})
	})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.Update(ctx, func(tx msfp.Tx) error {
		d := tx.(msfp.DownloadsTx)
		got, _ := d.Downloads()
		if len(got) != 3 || !got[0].PlayedAt.Equal(played) {
			t.Errorf("downloads after reopening: %+v", got)
		}
		tx.DeletePeer("p2")
		d.DeleteDownload(true, "play_a")
		return nil
	})
	st.Read(ctx, func(tx msfp.Tx) error {
		got, _ := tx.(msfp.DownloadsTx).Downloads()
		if len(got) != 1 || got[0].ID != "play_a" || got[0].Viewer {
			t.Errorf("downloads after a peer's deletion and one's: %+v", got)
		}
		return nil
	})
}
