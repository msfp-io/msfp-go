package msfp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/msfp-io/msfp-go"
)

// The next key (core section 12.5): its fingerprint shown, its seed on
// this server until the operator exports it and deletes it here; after
// that it can't be exported again, and its commitment (the fingerprint)
// stays the same.
func TestNextKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{Name: func() string { return "A" }})
	fp, onServer, err := a.eng.NextKey(ctx)
	if err != nil || fp == "" || !onServer {
		t.Fatalf("next key: %q %v %v", fp, onServer, err)
	}
	text, fp2, err := a.eng.ExportNextKey(ctx)
	if err != nil || fp2 != fp || !strings.HasPrefix(text, "msfp-next-key-1.") || len(text) < len("msfp-next-key-1.")+40 {
		t.Fatalf("export: %q %q %v", text, fp2, err)
	}
	if again, _, _ := a.eng.ExportNextKey(ctx); again != text {
		t.Error("a second export differs")
	}
	if err := a.eng.DeleteNextKey(ctx); err != nil {
		t.Fatal(err)
	}
	fp3, onServer, err := a.eng.NextKey(ctx)
	if err != nil || fp3 != fp || onServer {
		t.Errorf("after deleting it here: %q %v %v", fp3, onServer, err)
	}
	if _, _, err := a.eng.ExportNextKey(ctx); !errors.Is(err, msfp.ErrNoNextKey) {
		t.Errorf("export once deleted: %v", err)
	}
}
