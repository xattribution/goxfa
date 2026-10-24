package xfa

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xattribution/goxfa/internal/cms"
	"golang.org/x/crypto/ocsp"
)

// fakeTSA answers timestamp requests with tokens it signs itself.
func fakeTSA(t *testing.T) *httptest.Server {
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	now := time.Now()
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "Test TSA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping}}
	cert := must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key))))
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/timestamp-query" {
			http.Error(w, "bad type", 400)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req timeStampReq
		if _, err := asn1.Unmarshal(body, &req); err != nil || !req.CertReq {
			http.Error(w, "bad request", 400)
			return
		}
		tst, _ := asn1.Marshal(struct {
			Version        int
			Policy         asn1.ObjectIdentifier
			MessageImprint messageImprint
			Serial         *big.Int
			GenTime        time.Time `asn1:"generalized"`
			Nonce          *big.Int  `asn1:"optional"`
		}{1, asn1.ObjectIdentifier{1, 2, 3}, req.MessageImprint, big.NewInt(1), time.Now().UTC().Truncate(time.Second), req.Nonce})
		tok, err := cms.Sign(cms.Options{Content: tst, ContentType: cms.OIDTSTInfo, Hash: crypto.SHA256, Signer: key, Chain: []*x509.Certificate{cert}, SigningCertificate: true})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		status, _ := asn1.Marshal(struct{ Status int }{0})
		resp, _ := asn1.Marshal(struct {
			Status asn1.RawValue
			Token  asn1.RawValue
		}{asn1.RawValue{FullBytes: status}, asn1.RawValue{FullBytes: tok}})
		w.Header().Set("Content-Type", "application/timestamp-reply")
		w.Write(resp)
	}))
}

func TestSignWithTimestampAndOCSP(t *testing.T) {
	tsa := fakeTSA(t)
	defer tsa.Close()

	// an OCSP responder for the signing certificate, signed by its issuer
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	rk := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	now := time.Now()
	rt := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	root := must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, rt, rt, rk.Public(), rk))))
	var leaf *x509.Certificate
	responder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req, err := ocsp.ParseRequest(body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		resp, _ := ocsp.CreateResponse(root, root, ocsp.Response{Status: ocsp.Good, SerialNumber: req.SerialNumber, ThisUpdate: now, NextUpdate: now.Add(time.Hour)}, rk)
		w.Write(resp)
	}))
	defer responder.Close()
	lt := &x509.Certificate{SerialNumber: big.NewInt(5), Subject: pkix.Name{CommonName: "SMITH.JOHN.Q.1234567890"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, OCSPServer: []string{responder.URL}}
	leaf = must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, lt, root, key.Public(), rk))))
	chain := []*x509.Certificate{leaf, root}

	answers, err := FetchOCSP(chain, nil)
	if err != nil || len(answers) != 1 {
		t.Fatalf("OCSP: %d answers, %v", len(answers), err)
	}
	f, _ := openSample(t)
	signed, err := f.Sign(SignOptions{Field: "form1[0].Page1[0].Sig[0]", Signer: key, Chain: chain, OCSP: answers, Timestamp: TimestampAuthority(tsa.URL, nil)})
	if err != nil {
		t.Fatal(err)
	}
	checkByteRange(t, signed)
	if out := pdfsig(t, signed); out != "" {
		if !strings.Contains(out, "Signature is Valid") {
			t.Errorf("pdfsig:\n%s", out)
		}
		t.Log(out)
	}
	for _, s := range f.SignatureFields() {
		if s.Signed && s.Signer != "SMITH.JOHN.Q.1234567890" {
			t.Errorf("signer %q", s.Signer)
		}
	}
}

func TestTimestampRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := asn1.Marshal(struct{ Status struct{ Status int } }{struct{ Status int }{2}})
		w.Write(b)
	}))
	defer srv.Close()
	if _, err := TimestampAuthority(srv.URL, nil)([]byte("sig")); err == nil {
		t.Error("a refused timestamp should be an error")
	}
}

func TestCompleteChain(t *testing.T) {
	rk := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	ik := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	lk := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	now := time.Now()
	var root, inter *x509.Certificate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/root.p7c": // a bundle, as some PKIs publish
			p7, _ := cms.Sign(cms.Options{Content: []byte{}, Hash: crypto.SHA256, Signer: rk, Chain: []*x509.Certificate{root}})
			w.Write(p7)
		case "/inter.cer":
			w.Write(inter.Raw)
		}
	}))
	defer srv.Close()
	rt := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	root = must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, rt, rt, rk.Public(), rk))))
	it := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Intermediate"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, IssuingCertificateURL: []string{srv.URL + "/root.p7c"}}
	inter = must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, it, root, ik.Public(), rk))))
	lt := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "Leaf"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, IssuingCertificateURL: []string{srv.URL + "/inter.cer"}}
	leaf := must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, lt, inter, lk.Public(), ik))))
	chain, err := CompleteChain([]*x509.Certificate{leaf}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 3 || chain[1].Subject.CommonName != "Intermediate" || chain[2].Subject.CommonName != "Root" {
		t.Errorf("chain: %d certificates", len(chain))
	}
}
