package jsonfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
)

// One process at a time: a second Open of the directory fails while the
// first holds it, and works once it's closed.
func TestOneAtATime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no lock there")
	}
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, errLocked) || !strings.Contains(err.Error(), dir) {
		t.Fatalf("a second Open: %v", err)
	}
	st.Close()
	st, err = Open(dir)
	if err != nil {
		t.Fatalf("after Close: %v", err)
	}
	st.Close()
}

// A file that isn't JSON is an error that names it, never read as empty:
// a server must not start afresh, without its keys and pairings, over a
// damaged state.
func TestDamagedFilesRefused(t *testing.T) {
	for _, name := range []string{"state.json", "own.json"} {
		dir := t.TempDir()
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"peers":`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s damaged: %v", name, err)
		}
		// The lock isn't kept by the failed Open.
		os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o600)
		st, err = Open(dir)
		if err != nil {
			t.Errorf("%s mended: %v", name, err)
			continue
		}
		st.Close()
	}
	// An empty state.json (a disk full at the wrong moment) too.
	dir0 := t.TempDir()
	st0, _ := Open(dir0)
	st0.Close()
	os.WriteFile(filepath.Join(dir0, "state.json"), nil, 0o600)
	if _, err := Open(dir0); err == nil || !strings.Contains(err.Error(), "state.json") {
		t.Errorf("an empty state.json: %v", err)
	}
	// A peer's catalog too.
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := Open(dir)
	st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k")})
		return tx.PutPeerTitles("p1", 1, nil)
	})
	st.Close()
	os.WriteFile(filepath.Join(dir, "peers", "p1.json"), []byte(`not json`), 0o600)
	if _, err := Open(dir); err == nil || !strings.Contains(err.Error(), "p1.json") {
		t.Errorf("a damaged catalog: %v", err)
	}
}

// A peer ID names a file only in the engine's form: nothing climbs out of
// peers/, nor names another file.
func TestPeerFileNames(t *testing.T) {
	st := &Store{dir: "/srv/msfp"}
	for _, id := range []string{"", "../state", "a/b", "A1", "p.1", "p\x00", strings.Repeat("a", 65), "p1 "} {
		if p, err := st.peerFile(id); err == nil {
			t.Errorf("%q: %s", id, p)
		}
	}
	if p, err := st.peerFile("abc123"); err != nil || p != filepath.Join("/srv/msfp", "peers", "abc123.json") {
		t.Errorf("abc123: %s %v", p, err)
	}
}

// Files are 0600, the directories 0700, whatever the umask.
func TestModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no modes there")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "store")
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	err = st.Update(ctx, func(tx msfp.Tx) error {
		tx.PutPeer(msfp.PeerRecord{ID: "p1", Key: []byte("k")})
		return tx.PutPeerTitles("p1", 1, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Sealer().Seal([]byte("seed"), "test"); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{
		dir: 0o700, filepath.Join(dir, "peers"): 0o700,
		filepath.Join(dir, "state.json"): 0o600, filepath.Join(dir, "peers", "p1.json"): 0o600,
		filepath.Join(dir, "lock"): 0o600, filepath.Join(dir, "sealing.key"): 0o600,
	} {
		fi, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got := fi.Mode().Perm(); got&^want != 0 {
			t.Errorf("%s: mode %o, want at most %o", path, got, want)
		}
	}
}
