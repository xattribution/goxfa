package xfa

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testSigner is a software key with a root-issued signing certificate.
func testSigner(t *testing.T, key crypto.Signer) []*x509.Certificate {
	t.Helper()
	rk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	rt := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rd, _ := x509.CreateCertificate(rand.Reader, rt, rt, rk.Public(), rk)
	root, _ := x509.ParseCertificate(rd)
	lt := &x509.Certificate{SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "DOE.JANE.A.1234567890"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	ld, err := x509.CreateCertificate(rand.Reader, lt, root, key.Public(), rk)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(ld)
	return []*x509.Certificate{leaf, root}
}

var reByteRange = regexp.MustCompile(`/ByteRange \[0 (\d+) (\d+) (\d+) *\]`)

// checkByteRange checks the last signature covers the file but its hole.
func checkByteRange(t *testing.T, pdf []byte) {
	t.Helper()
	m := reByteRange.FindAllSubmatch(pdf, -1)
	if len(m) == 0 {
		t.Fatal("no /ByteRange")
	}
	last := m[len(m)-1]
	a, _ := strconv.Atoi(string(last[1]))
	b, _ := strconv.Atoi(string(last[2]))
	c, _ := strconv.Atoi(string(last[3]))
	if pdf[a] != '<' || pdf[b-1] != '>' || b+c != len(pdf) {
		t.Errorf("byte range [0 %d %d %d] doesn't cover a %d-byte file around /Contents", a, b, c, len(pdf))
	}
}

// pdfsig runs poppler's checker, when it's installed.
func pdfsig(t *testing.T, pdf []byte) string {
	t.Helper()
	if _, err := exec.LookPath("pdfsig"); err != nil {
		return ""
	}
	p := filepath.Join(t.TempDir(), "signed.pdf")
	os.WriteFile(p, pdf, 0o600)
	out, _ := exec.Command("pdfsig", p).CombinedOutput()
	return string(out)
}

func TestSign(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   crypto.Signer
		cades bool
	}{
		{"rsa", must(rsa.GenerateKey(rand.Reader, 2048)), false},
		{"ecdsa-p384-cades", must(ecdsa.GenerateKey(elliptic.P384(), rand.Reader)), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, orig := openSample(t)
			chain := testSigner(t, tc.key)
			if err := f.Set("form1[0].Page1[0].Name[0]", "DOE, JANE A"); err != nil {
				t.Fatal(err)
			}
			signed, err := f.Sign(SignOptions{Field: "form1[0].Page1[0].Sig[0]", Signer: tc.key, Chain: chain, Reason: "I agree", CAdES: tc.cades})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(signed, orig) {
				t.Fatal("signing must append to the original")
			}
			checkByteRange(t, signed)
			// the widget carries a picture sized to the XFA field's box, for
			// viewers that draw XFA forms themselves
			if !bytes.Contains(signed[len(orig):], []byte("/BBox [0 0 216")) {
				t.Error("the signature's appearance isn't sized to the field's box")
			}
			if out := pdfsig(t, signed); out != "" && (!strings.Contains(out, "Signature is Valid") || !strings.Contains(out, "Total document signed")) {
				t.Errorf("pdfsig:\n%s", out)
			}
			// the form now stands for the signed PDF
			if v, _ := f.Value("form1[0].Page1[0].Name[0]"); v != "DOE, JANE A" {
				t.Errorf("value signed with the form: %q", v)
			}
			var sf SignatureField
			for _, s := range f.SignatureFields() {
				if s.SOM == "form1[0].Page1[0].Sig[0]" {
					sf = s
				}
			}
			if !sf.Signed || sf.Signer != "DOE.JANE.A.1234567890" || sf.Name != "DOE, JANE A" || len(sf.Locks) != 1 {
				t.Errorf("signature field after signing: %+v", sf)
			}
			if err := f.Set("form1[0].Page1[0].Name[0]", "ROE"); !errors.Is(err, ErrLocked) {
				t.Errorf("the manifest's field should be locked, got %v", err)
			}
			if _, err := f.Sign(SignOptions{Field: "form1[0].Page1[0].Sig[0]", Signer: tc.key, Chain: chain}); !errors.Is(err, ErrSigned) {
				t.Errorf("signing twice: %v", err)
			}
			// fields the signature doesn't lock can still change, after it
			if err := f.Set("form1[0].Page1[0].Branch[0]", "A"); err != nil {
				t.Fatal(err)
			}
			later, err := f.Save()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(later, signed) {
				t.Error("saving after signing must append after the signature")
			}
			if out := pdfsig(t, later); out != "" && !strings.Contains(out, "Signature is Valid") {
				t.Errorf("pdfsig after a later save:\n%s", out)
			}
		})
	}
}

func TestSignRefuses(t *testing.T) {
	f, _ := openSample(t)
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	chain := testSigner(t, key)
	if _, err := f.Sign(SignOptions{Field: "form1[0].Page1[0].Name[0]", Signer: key, Chain: chain}); err == nil {
		t.Error("a text field can't be signed")
	}
	enc := *chain[0]
	enc.KeyUsage = x509.KeyUsageKeyEncipherment
	if _, err := f.Sign(SignOptions{Field: "form1[0].Page1[0].Sig[0]", Signer: key, Chain: []*x509.Certificate{&enc}}); err == nil {
		t.Error("an encryption certificate can't sign")
	}
	other := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	if _, err := f.Sign(SignOptions{Field: "form1[0].Page1[0].Sig[0]", Signer: other, Chain: chain}); err == nil {
		t.Error("a key that isn't the certificate's can't sign")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func TestHandlerSigns(t *testing.T) {
	f, _ := openSample(t)
	key := must(ecdsa.GenerateKey(elliptic.P256(), rand.Reader))
	chain := testSigner(t, key)
	var saved []byte
	h := f.Handler(HandlerOptions{
		Save: func(b []byte) error { saved = b; return nil },
		Sign: func(string) (SignOptions, error) { return SignOptions{Signer: key, Chain: chain}, nil },
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	r, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(page), `"CanSign":true`) {
		t.Error("the page should offer signing")
	}
	r, err = http.Post(srv.URL+"/sign", "application/json", strings.NewReader(`{"SOM":"form1[0].Page1[0].Sig[0]"}`))
	if err != nil || r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("sign: %v %s", err, b)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "Digitally signed by DOE, JANE A") {
		t.Errorf("the new layout should show the signature: %.300s", b)
	}
	if out := pdfsig(t, saved); out != "" && !strings.Contains(out, "Signature is Valid") {
		t.Errorf("pdfsig:\n%s", out)
	}
}
