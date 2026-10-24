package cms

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// testChain makes a root and a signing certificate for key.
func testChain(t *testing.T, key crypto.Signer) (root, leaf *x509.Certificate) {
	t.Helper()
	rk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	now := time.Now()
	rt := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rd, err := x509.CreateCertificate(rand.Reader, rt, rt, rk.Public(), rk)
	if err != nil {
		t.Fatal(err)
	}
	root, _ = x509.ParseCertificate(rd)
	lt := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "DOE.JANE.A"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageContentCommitment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageEmailProtection}}
	ld, err := x509.CreateCertificate(rand.Reader, lt, root, key.Public(), rk)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ = x509.ParseCertificate(ld)
	return root, leaf
}

func TestDetachedVerifiesWithOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not found")
	}
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	content := []byte("the signed byte ranges of a PDF")
	for _, tc := range []struct {
		name string
		key  crypto.Signer
		hash crypto.Hash
	}{{"rsa", rk, crypto.SHA256}, {"ecdsa", ek, crypto.SHA384}} {
		t.Run(tc.name, func(t *testing.T) {
			root, leaf := testChain(t, tc.key)
			var d []byte
			if tc.hash == crypto.SHA256 {
				s := sha256.Sum256(content)
				d = s[:]
			} else {
				s := sha512.Sum384(content)
				d = s[:]
			}
			der, err := Sign(Options{Digest: d, Hash: tc.hash, Signer: tc.key, Chain: []*x509.Certificate{leaf, root},
				SigningTime: time.Now(), SigningCertificate: true, OCSP: [][]byte{{0x30, 0x00}}})
			if err != nil {
				t.Fatal(err)
			}
			if c := SignerCertificate(der); c == nil || c.Subject.CommonName != "DOE.JANE.A" {
				t.Errorf("signer certificate: %v", c)
			}
			dir := t.TempDir()
			write := func(name string, b []byte) string {
				p := filepath.Join(dir, name)
				os.WriteFile(p, b, 0o600)
				return p
			}
			sig := write("sig.der", der)
			data := write("data", content)
			ca := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw}))
			out, err := exec.Command("openssl", "cms", "-verify", "-binary", "-inform", "DER", "-in", sig, "-content", data, "-CAfile", ca, "-purpose", "any", "-out", os.DevNull).CombinedOutput()
			if err != nil {
				t.Fatalf("openssl: %v\n%s", err, out)
			}
			// a changed byte fails
			bad := write("bad", append([]byte("X"), content[1:]...))
			if err := exec.Command("openssl", "cms", "-verify", "-binary", "-inform", "DER", "-in", sig, "-content", bad, "-CAfile", ca, "-purpose", "any", "-out", os.DevNull).Run(); err == nil {
				t.Error("openssl verified changed content")
			}
		})
	}
}

func TestRefusesAnotherKey(t *testing.T) {
	k1, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	k2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	_, leaf := testChain(t, k1)
	d := sha256.Sum256(nil)
	if _, err := Sign(Options{Digest: d[:], Signer: k2, Chain: []*x509.Certificate{leaf}}); err == nil {
		t.Error("signing with a key that isn't the certificate's should fail")
	}
}
