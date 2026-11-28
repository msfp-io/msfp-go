package msfp

import (
	"encoding/json"
	"net/netip"
	"os"
	"testing"
)

// The cases of strict.json's destinations (msfp-spec's vectors/v0; MSFP
// core section 3.3): the IP addresses a server connects to for a peer by default, or
// refuses, through refusedDestination with loopback not allowed.
func TestDestinationVectors(t *testing.T) {
	b, err := os.ReadFile("wire/testdata/msfp/v0/strict.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Destinations []struct {
			Text   string
			Valid  bool
			Reason string
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Destinations) == 0 {
		t.Fatal("strict.json: no destinations")
	}
	for _, c := range v.Destinations {
		ip, err := netip.ParseAddr(c.Text)
		valid := err == nil && !refusedDestination(ip, false)
		if valid != c.Valid {
			t.Errorf("%s (%s): valid %v, want %v", c.Text, c.Reason, valid, c.Valid)
		}
	}
}

// Loopback allowed (a test or a server's own machine): the loopback
// cases pass, every other refused case stays refused.
func TestDestinationLoopbackAllowed(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1"} {
		if refusedDestination(netip.MustParseAddr(s), true) {
			t.Errorf("%s refused with loopback allowed", s)
		}
	}
	for _, s := range []string{"169.254.169.254", "0.0.0.0", "224.0.0.1"} {
		if !refusedDestination(netip.MustParseAddr(s), true) {
			t.Errorf("%s not refused with loopback allowed", s)
		}
	}
}
