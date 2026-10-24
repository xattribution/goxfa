// Package cms builds CMS (PKCS #7) SignedData for PDF signatures: detached
// signatures over a PDF's byte ranges, as /adbe.pkcs7.detached and
// /ETSI.CAdES.detached expect, and encapsulated ones for tests.
package cms

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"time"
)

var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidSigningCertV2 = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 47}
	oidTimeStampTok  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 2, 14}
	oidAdbeRevInfo   = asn1.ObjectIdentifier{1, 2, 840, 113583, 1, 1, 8}
	oidRSA           = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidECDSASHA256   = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
	oidECDSASHA384   = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 3}
	oidECDSASHA512   = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 4}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}
	oidSHA512        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}

	// OIDTSTInfo is the content type of an RFC 3161 timestamp token.
	OIDTSTInfo = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
)

// Options describe one signature.
type Options struct {
	// Digest is the hash of the signed content (for a PDF, its byte
	// ranges), made with Hash. Ignored when Content is set.
	Digest []byte
	Hash   crypto.Hash // SHA256, SHA384 or SHA512
	// Content, when set, is encapsulated in the signature with the
	// content type ContentType (data when nil).
	Content     []byte
	ContentType asn1.ObjectIdentifier

	Signer crypto.Signer
	Chain  []*x509.Certificate // the signer's certificate first

	// SigningTime, when not zero, is added as a signed attribute.
	SigningTime time.Time
	// SigningCertificate adds the ESS signing-certificate-v2 attribute,
	// which CAdES requires.
	SigningCertificate bool
	// OCSP and CRLs, DER encoded, go in Adobe's revocation archive
	// attribute, so the signature validates later and offline.
	OCSP, CRLs [][]byte
	// Timestamp, when set, gets the signature value and gives back an
	// RFC 3161 timestamp token for it, added as an unsigned attribute.
	Timestamp func(signature []byte) ([]byte, error)
}

// Sign builds the ContentInfo holding the SignedData, DER encoded.
func Sign(o Options) ([]byte, error) {
	if o.Signer == nil || len(o.Chain) == 0 {
		return nil, errors.New("cms: a signer and its certificate are needed")
	}
	cert := o.Chain[0]
	if !samePublicKey(cert.PublicKey, o.Signer.Public()) {
		return nil, errors.New("cms: the certificate isn't the signer's")
	}
	if o.Hash == 0 {
		o.Hash = crypto.SHA256
	}
	hashOID, err := hashOIDFor(o.Hash)
	if err != nil {
		return nil, err
	}
	digest := o.Digest
	ctype := oidData
	if o.Content != nil {
		h := o.Hash.New()
		h.Write(o.Content)
		digest = h.Sum(nil)
		if o.ContentType != nil {
			ctype = o.ContentType
		}
	}
	if len(digest) != o.Hash.Size() {
		return nil, fmt.Errorf("cms: the digest is %d bytes, not %d", len(digest), o.Hash.Size())
	}
	sigAlg, err := signatureAlg(o.Signer.Public(), o.Hash)
	if err != nil {
		return nil, err
	}

	attrs := [][]byte{
		attr(oidContentType, oid(ctype)),
		attr(oidMessageDigest, octets(digest)),
	}
	if !o.SigningTime.IsZero() {
		attrs = append(attrs, attr(oidSigningTime, utcTime(o.SigningTime)))
	}
	if o.SigningCertificate {
		attrs = append(attrs, attr(oidSigningCertV2, signingCertV2(cert, o.Hash, hashOID)))
	}
	if len(o.OCSP) > 0 || len(o.CRLs) > 0 {
		var parts [][]byte
		if len(o.CRLs) > 0 {
			parts = append(parts, explicit(0, seq(o.CRLs...)))
		}
		if len(o.OCSP) > 0 {
			parts = append(parts, explicit(1, seq(o.OCSP...)))
		}
		attrs = append(attrs, attr(oidAdbeRevInfo, seq(parts...)))
	}
	signedAttrs := setOf(attrs...)
	h := o.Hash.New()
	h.Write(signedAttrs)
	sig, err := o.Signer.Sign(rand.Reader, h.Sum(nil), o.Hash)
	if err != nil {
		return nil, fmt.Errorf("cms: signing: %w", err)
	}

	si := [][]byte{
		small(1),
		seq(cert.RawIssuer, integer(cert.SerialNumber)),
		alg(hashOID, false),
		implicit(0, signedAttrs),
		sigAlg,
		octets(sig),
	}
	if o.Timestamp != nil {
		tok, err := o.Timestamp(sig)
		if err != nil {
			return nil, fmt.Errorf("cms: timestamp: %w", err)
		}
		si = append(si, implicit(1, setOf(attr(oidTimeStampTok, tok))))
	}

	var certs [][]byte
	for _, c := range o.Chain {
		certs = append(certs, c.Raw)
	}
	eci := [][]byte{oid(ctype)}
	if o.Content != nil {
		eci = append(eci, explicit(0, octets(o.Content)))
	}
	version := 1
	if o.Content != nil && !ctype.Equal(oidData) {
		version = 3
	}
	sd := seq(
		small(version),
		setOf(alg(hashOID, false)),
		seq(eci...),
		implicit(0, setOf(certs...)),
		setOf(seq(si...)),
	)
	return seq(oid(oidSignedData), explicit(0, sd)), nil
}

func attr(t asn1.ObjectIdentifier, value []byte) []byte {
	return seq(oid(t), setOf(value))
}

// signingCertV2 is SigningCertificateV2 with one ESSCertIDv2.
func signingCertV2(cert *x509.Certificate, hash crypto.Hash, hashOID asn1.ObjectIdentifier) []byte {
	h := hash.New()
	h.Write(cert.Raw)
	id := [][]byte{}
	if hash != crypto.SHA256 { // SHA-256 is the default and is left out
		id = append(id, alg(hashOID, false))
	}
	issuer := seq(tlv(0xa4, cert.RawIssuer)) // GeneralNames { directoryName }
	id = append(id, octets(h.Sum(nil)), seq(issuer, integer(cert.SerialNumber)))
	return seq(seq(seq(id...)))
}

func hashOIDFor(h crypto.Hash) (asn1.ObjectIdentifier, error) {
	switch h {
	case crypto.SHA256:
		return oidSHA256, nil
	case crypto.SHA384:
		return oidSHA384, nil
	case crypto.SHA512:
		return oidSHA512, nil
	}
	return nil, fmt.Errorf("cms: hash %v isn't supported", h)
}

func signatureAlg(pub crypto.PublicKey, h crypto.Hash) ([]byte, error) {
	switch pub.(type) {
	case *rsa.PublicKey:
		return alg(oidRSA, true), nil
	case *ecdsa.PublicKey:
		switch h {
		case crypto.SHA256:
			return alg(oidECDSASHA256, false), nil
		case crypto.SHA384:
			return alg(oidECDSASHA384, false), nil
		case crypto.SHA512:
			return alg(oidECDSASHA512, false), nil
		}
	}
	return nil, fmt.Errorf("cms: %T keys aren't supported", pub)
}

func samePublicKey(a, b crypto.PublicKey) bool {
	type eq interface{ Equal(crypto.PublicKey) bool }
	if e, ok := a.(eq); ok {
		return e.Equal(b)
	}
	return false
}

// SignerCertificate is the signer's certificate in a CMS SignedData, or
// nil when it can't be found. The signature isn't verified.
func SignerCertificate(der []byte) *x509.Certificate {
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(der, &ci); err != nil || !ci.Type.Equal(oidSignedData) {
		return nil
	}
	var sd struct {
		Version     int
		Digests     asn1.RawValue
		Encap       asn1.RawValue
		Certs       asn1.RawValue `asn1:"optional,tag:0"`
		CRLs        asn1.RawValue `asn1:"optional,tag:1"`
		SignerInfos []struct {
			Version int
			SID     struct {
				Issuer asn1.RawValue
				Serial *big.Int
			}
			DigestAlg   asn1.RawValue
			SignedAttrs asn1.RawValue `asn1:"optional,tag:0"`
			SigAlg      asn1.RawValue
			Signature   []byte
			Unsigned    asn1.RawValue `asn1:"optional,tag:1"`
		} `asn1:"set"`
	}
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil
	}
	certs, err := x509.ParseCertificates(sd.Certs.Bytes)
	if err != nil || len(certs) == 0 {
		return nil
	}
	for _, s := range sd.SignerInfos {
		for _, c := range certs {
			if s.SID.Serial != nil && c.SerialNumber.Cmp(s.SID.Serial) == 0 && bytes.Equal(c.RawIssuer, s.SID.Issuer.FullBytes) {
				return c
			}
		}
	}
	return certs[0]
}

// Certificates are the certificates in a degenerate SignedData (a .p7c
// bundle), or nil.
func Certificates(der []byte) []*x509.Certificate {
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"explicit,tag:0"`
	}
	if _, err := asn1.Unmarshal(der, &ci); err != nil || !ci.Type.Equal(oidSignedData) {
		return nil
	}
	var sd struct {
		Version int
		Digests asn1.RawValue
		Encap   asn1.RawValue
		Certs   asn1.RawValue `asn1:"optional,tag:0"`
		Rest    asn1.RawValue `asn1:"optional"`
		More    asn1.RawValue `asn1:"optional"`
	}
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &sd); err != nil {
		return nil
	}
	certs, _ := x509.ParseCertificates(sd.Certs.Bytes)
	return certs
}
