// Package winsign signs with the keys in the Windows certificate store,
// smart cards included, through CNG. It needs no cgo: the
// calls go straight to crypt32.dll and ncrypt.dll.
//
//	certs, err := winsign.Certificates()      // the user's signing certificates
//	signer, err := certs[0].Signer()          // Windows asks for the card's PIN when it signs
//	defer signer.Close()
//	signed, err := form.Sign(xfa.SignOptions{Field: som, Signer: signer, Chain: certs[0].Chain})
//
// On other systems Certificates returns ErrUnsupported.
package winsign

import (
	"crypto"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"slices"
	"time"
)

// ErrUnsupported is returned off Windows.
var ErrUnsupported = errors.New("winsign: the Windows certificate store is only on Windows")

// Cert is a certificate in the user's store that has a private key.
type Cert struct {
	Certificate *x509.Certificate
	// Chain is the certificate first, then its issuers, as far as Windows
	// can build the chain from what's installed (nothing is fetched).
	Chain []*x509.Certificate
	h     handle
}

// Signer is a crypto.Signer for a certificate's key. Close it when done.
type Signer interface {
	crypto.Signer
	Close() error
}

// Extended key usages that mean a certificate signs documents: e-mail
// protection (a smart card's signature certificate), Microsoft and IETF document
// signing, and Adobe's authentic documents.
var signingEKUs = []asn1.ObjectIdentifier{
	{1, 3, 6, 1, 4, 1, 311, 10, 3, 12},
	{1, 3, 6, 1, 5, 5, 7, 3, 36},
	{1, 2, 840, 113583, 1, 1, 5},
}

// CanSign says whether a certificate is for signing documents: valid now,
// with a digital signature or non-repudiation key usage, and, when it
// lists extended key usages, one for signing documents or e-mail. That
// leaves out a card's authentication and card-authentication
// certificates, its encryption certificate, and TLS certificates.
func CanSign(c *x509.Certificate) bool {
	now := time.Now()
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return false
	}
	if c.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageContentCommitment) == 0 {
		return false
	}
	if len(c.ExtKeyUsage) == 0 && len(c.UnknownExtKeyUsage) == 0 {
		return true
	}
	for _, u := range c.ExtKeyUsage {
		if u == x509.ExtKeyUsageEmailProtection || u == x509.ExtKeyUsageAny {
			return true
		}
	}
	for _, u := range c.UnknownExtKeyUsage {
		for _, s := range signingEKUs {
			if u.Equal(s) {
				return true
			}
		}
	}
	return false
}

// SigningCertificates are the certificates CanSign accepts: those with
// non-repudiation first (a card's signature certificate), then the ones
// that expire last.
func SigningCertificates() ([]*Cert, error) {
	all, err := Certificates()
	if err != nil {
		return nil, err
	}
	var out []*Cert
	for _, c := range all {
		if CanSign(c.Certificate) {
			out = append(out, c)
		}
	}
	nr := func(c *Cert) int {
		if c.Certificate.KeyUsage&x509.KeyUsageContentCommitment != 0 {
			return 0
		}
		return 1
	}
	slices.SortStableFunc(out, func(a, b *Cert) int {
		if d := nr(a) - nr(b); d != 0 {
			return d
		}
		return b.Certificate.NotAfter.Compare(a.Certificate.NotAfter)
	})
	return out, nil
}
