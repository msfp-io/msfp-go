//go:build unix

package describe

import (
	"context"
	"errors"
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
