package wire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

// The certificates of vectors/v0/keys.json (core section 3.2): each
// Ed25519 seed gives the SubjectPublicKeyInfo spki, and a certificate
// for that key has the pin keys.json says.
func TestVectorCertificatePins(t *testing.T) {
	var v struct {
		Certificates map[string]struct{ Seed, Spki, Pin string }
	}
	readVectors(t, "msfp/v0/keys.json", &v)
	if len(v.Certificates) == 0 {
		t.Fatal("keys.json: no certificates")
	}
	for name, c := range v.Certificates {
		priv := ed25519.NewKeyFromSeed(unhex(t, c.Seed))
		spki, err := x509.MarshalPKIXPublicKey(priv.Public())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(spki, unhex(t, c.Spki)) {
			t.Errorf("%s: spki %x, keys.json says %s", name, spki, c.Spki)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1),
			Subject:      pkix.Name{CommonName: name + ".example.org"},
			NotBefore:    time.Unix(0, 0),
			NotAfter:     time.Unix(1<<32, 0),
		}
		der, err := x509.CreateCertificate(nil, tmpl, tmpl, priv.Public(), priv)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		if got := Pin(cert); got != c.Pin {
			t.Errorf("%s: pin %q, keys.json says %q", name, got, c.Pin)
		}
		if !ValidPin(c.Pin) {
			t.Errorf("%s: pin %q not valid", name, c.Pin)
		}
	}
}
