package winsign

import (
	"crypto/x509"
	"encoding/asn1"
	"testing"
	"time"
)

// The certificates on a typical smart card, by their key usages, plus a TLS certificate.
func TestCanSign(t *testing.T) {
	now := time.Now()
	valid := func(c x509.Certificate) *x509.Certificate {
		c.NotBefore, c.NotAfter = now.Add(-time.Hour), now.Add(time.Hour)
		return &c
	}
	docSign := asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 10, 3, 12}
	pkinit := asn1.ObjectIdentifier{1, 3, 6, 1, 5, 2, 3, 4}
	logon := asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 20, 2, 2}
	cardAuth := asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 6, 8}
	for _, tc := range []struct {
		name string
		c    *x509.Certificate
		want bool
	}{
		{"signature", valid(x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}, UnknownExtKeyUsage: []asn1.ObjectIdentifier{docSign}}), true},
		{"authentication", valid(x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, UnknownExtKeyUsage: []asn1.ObjectIdentifier{logon, pkinit}}), false},
		{"card authentication", valid(x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature, UnknownExtKeyUsage: []asn1.ObjectIdentifier{cardAuth}}), false},
		{"encryption", valid(x509.Certificate{KeyUsage: x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}), false},
		{"tls", valid(x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}), false},
		{"no usages", valid(x509.Certificate{}), false},
		{"expired", &x509.Certificate{KeyUsage: x509.KeyUsageDigitalSignature, NotBefore: now.Add(-2 * time.Hour), NotAfter: now.Add(-time.Hour)}, false},
	} {
		if got := CanSign(tc.c); got != tc.want {
			t.Errorf("%s: CanSign = %v, want %v", tc.name, got, tc.want)
		}
	}
}
