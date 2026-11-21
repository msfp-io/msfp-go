package msfp_test

import (
	"context"
	"errors"
	"testing"

	msfp "github.com/msfp-io/msfp-go"
)

// A live code's text again: the same code, redeemable; none for a code
// withdrawn.
func TestCodeText(t *testing.T) {
	ctx := context.Background()
	a := newServer(t, msfp.Config{})
	code, view, err := a.eng.MakeCodeWith(ctx, msfp.CodeOptions{Note: "x"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.eng.CodeText(ctx, view.ID)
	if err != nil || again != code {
		t.Fatalf("again: %q %v, want %q", again, err, code)
	}
	if err := a.eng.DeleteCode(ctx, view.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.eng.CodeText(ctx, view.ID); !errors.Is(err, msfp.ErrNotFound) {
		t.Fatalf("withdrawn: %v", err)
	}
}
