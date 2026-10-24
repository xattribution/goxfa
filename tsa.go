package xfa

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/xattribution/goxfa/internal/cms"
	"golang.org/x/crypto/ocsp"
)

// Timestamps and revocation answers come from servers on the network.
// Nothing here runs unless a program asks for it, with the client it
// chooses.

// TimestampAuthority returns a SignOptions.Timestamp function that asks
// the RFC 3161 timestamp authority at url to timestamp the signature.
// client may be nil for http.DefaultClient.
func TimestampAuthority(url string, client *http.Client) func(signature []byte) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return func(signature []byte) ([]byte, error) {
		h := crypto.SHA256.New()
		h.Write(signature)
		nonce, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 63))
		req, err := asn1.Marshal(timeStampReq{
			Version: 1,
			MessageImprint: messageImprint{
				HashAlgorithm: algorithmIdentifier{Algorithm: asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}},
				HashedMessage: h.Sum(nil),
			},
			Nonce:   nonce,
			CertReq: true,
		})
		if err != nil {
			return nil, err
		}
		hr, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(req))
		if err != nil {
			return nil, err
		}
		hr.Header.Set("Content-Type", "application/timestamp-query")
		resp, err := client.Do(hr)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("the timestamp authority answered %s", resp.Status)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, err
		}
		return timestampToken(body)
	}
}

type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type messageImprint struct {
	HashAlgorithm algorithmIdentifier
	HashedMessage []byte
}

type timeStampReq struct {
	Version        int
	MessageImprint messageImprint
	Nonce          *big.Int `asn1:"optional"`
	CertReq        bool     `asn1:"optional,default:false"`
}

// timestampToken takes the token out of a TimeStampResp, refusing an
// answer that isn't granted.
func timestampToken(resp []byte) ([]byte, error) {
	var r struct {
		Status struct {
			Status int
			Rest   asn1.RawValue `asn1:"optional"`
			More   asn1.RawValue `asn1:"optional"`
		}
		Token asn1.RawValue `asn1:"optional"`
	}
	if _, err := asn1.Unmarshal(resp, &r); err != nil {
		return nil, fmt.Errorf("the timestamp authority's answer can't be read: %w", err)
	}
	if r.Status.Status > 1 { // 0 granted, 1 granted with modifications
		return nil, fmt.Errorf("the timestamp authority refused (status %d)", r.Status.Status)
	}
	if len(r.Token.FullBytes) == 0 {
		return nil, errors.New("the timestamp authority sent no token")
	}
	return r.Token.FullBytes, nil
}

// FetchOCSP asks each certificate's OCSP responder in chain (signing
// certificate first) whether it's revoked, and returns the answers for
// SignOptions.OCSP. A revoked certificate is an error. Certificates
// without a responder, and the root, are skipped. client may be nil for
// http.DefaultClient.
func FetchOCSP(chain []*x509.Certificate, client *http.Client) ([][]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	var out [][]byte
	for i := 0; i+1 < len(chain); i++ {
		c, issuer := chain[i], chain[i+1]
		if len(c.OCSPServer) == 0 {
			continue
		}
		req, err := ocsp.CreateRequest(c, issuer, &ocsp.RequestOptions{Hash: crypto.SHA256})
		if err != nil {
			return nil, err
		}
		resp, err := client.Post(c.OCSPServer[0], "application/ocsp-request", bytes.NewReader(req))
		if err != nil {
			return nil, fmt.Errorf("asking %s about %s: %w", c.OCSPServer[0], c.Subject.CommonName, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		r, err := ocsp.ParseResponseForCert(body, c, issuer)
		if err != nil {
			return nil, fmt.Errorf("the OCSP answer for %s can't be read: %w", c.Subject.CommonName, err)
		}
		if r.Status == ocsp.Revoked {
			return nil, fmt.Errorf("%s was revoked on %s", c.Subject.CommonName, r.RevokedAt.Format("02 Jan 06"))
		}
		out = append(out, body)
	}
	return out, nil
}

// CompleteChain adds the issuers a chain is missing by following each
// last certificate's issuer URL (its AIA caIssuers entry) until a
// self-signed root, a certificate with no URL, or five fetches. Readers
// that trust the root can then validate offline. client may be nil.
func CompleteChain(chain []*x509.Certificate, client *http.Client) ([]*x509.Certificate, error) {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	out := append([]*x509.Certificate(nil), chain...)
	for i := 0; i < 5 && len(out) > 0; i++ {
		last := out[len(out)-1]
		if bytes.Equal(last.RawIssuer, last.RawSubject) && last.CheckSignatureFrom(last) == nil {
			break // a root
		}
		if len(last.IssuingCertificateURL) == 0 {
			break
		}
		resp, err := client.Get(last.IssuingCertificateURL[0])
		if err != nil {
			return out, fmt.Errorf("fetching the issuer of %s: %w", last.Subject.CommonName, err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return out, err
		}
		if resp.StatusCode != http.StatusOK {
			return out, fmt.Errorf("fetching the issuer of %s: %s", last.Subject.CommonName, resp.Status)
		}
		issuer := issuerIn(body, last)
		if issuer == nil {
			return out, fmt.Errorf("%s doesn't hold the issuer of %s", last.IssuingCertificateURL[0], last.Subject.CommonName)
		}
		out = append(out, issuer)
	}
	return out, nil
}

// issuerIn finds the certificate that issued c in a DER or PEM certificate
// or a .p7c bundle.
func issuerIn(b []byte, c *x509.Certificate) *x509.Certificate {
	var certs []*x509.Certificate
	if blk, _ := pem.Decode(b); blk != nil {
		b = blk.Bytes
	}
	if cs, err := x509.ParseCertificates(b); err == nil {
		certs = cs
	} else {
		certs = cms.Certificates(b)
	}
	for _, k := range certs {
		if c.CheckSignatureFrom(k) == nil {
			return k
		}
	}
	return nil
}
