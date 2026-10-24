//go:build windows

package winsign

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	crypt32    = windows.NewLazySystemDLL("crypt32.dll")
	ncrypt     = windows.NewLazySystemDLL("ncrypt.dll")
	getProp    = crypt32.NewProc("CertGetCertificateContextProperty")
	signHash   = ncrypt.NewProc("NCryptSignHash")
	freeObject = ncrypt.NewProc("NCryptFreeObject")
)

const (
	certKeyProvInfoPropID      = 2
	chainCacheOnlyURLRetrieval = 0x00000004
	ncryptPadPKCS1             = 0x00000002
	nteBufferTooSmall          = 0x80090028
	scardWCancelledByUser      = 0x8010006E
	nteUserCancelled           = 0x80090036
)

// handle keeps the certificate's context; it's freed with the Cert.
type handle struct{ ctx *windows.CertContext }

// Certificates lists the certificates in the user's store ("MY") that
// have a private key: software keys and keys on inserted smart cards.
func Certificates() ([]*Cert, error) {
	store, err := windows.CertOpenSystemStore(0, windows.StringToUTF16Ptr("MY"))
	if err != nil {
		return nil, fmt.Errorf("winsign: opening the certificate store: %w", err)
	}
	defer windows.CertCloseStore(store, 0)
	var out []*Cert
	var prev *windows.CertContext
	for {
		ctx, err := windows.CertEnumCertificatesInStore(store, prev)
		if err != nil || ctx == nil {
			break
		}
		prev = ctx
		if !hasKey(ctx) {
			continue
		}
		der := append([]byte(nil), unsafe.Slice(ctx.EncodedCert, ctx.Length)...)
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			continue
		}
		dup := windows.CertDuplicateCertificateContext(ctx)
		c := &Cert{Certificate: cert, Chain: chainOf(dup, cert), h: handle{ctx: dup}}
		runtime.SetFinalizer(c, func(c *Cert) { windows.CertFreeCertificateContext(c.h.ctx) })
		out = append(out, c)
	}
	return out, nil
}

func hasKey(ctx *windows.CertContext) bool {
	var n uint32
	r, _, _ := getProp.Call(uintptr(unsafe.Pointer(ctx)), certKeyProvInfoPropID, 0, uintptr(unsafe.Pointer(&n)))
	return r != 0 && n > 0
}

// chainOf builds the chain from installed certificates only.
func chainOf(ctx *windows.CertContext, leaf *x509.Certificate) []*x509.Certificate {
	out := []*x509.Certificate{leaf}
	para := windows.CertChainPara{Size: uint32(unsafe.Sizeof(windows.CertChainPara{}))}
	var chain *windows.CertChainContext
	if err := windows.CertGetCertificateChain(0, ctx, nil, 0, &para, chainCacheOnlyURLRetrieval, 0, &chain); err != nil || chain == nil {
		return out
	}
	defer windows.CertFreeCertificateChain(chain)
	if chain.ChainCount == 0 {
		return out
	}
	simple := unsafe.Slice(chain.Chains, chain.ChainCount)[0]
	elems := unsafe.Slice(simple.Elements, simple.NumElements)
	for _, e := range elems[1:] {
		cc := e.CertContext
		if c, err := x509.ParseCertificate(append([]byte(nil), unsafe.Slice(cc.EncodedCert, cc.Length)...)); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// Signer opens the certificate's key through CNG. For a smart card,
// Windows asks for the PIN when the first signature is made.
func (c *Cert) Signer() (Signer, error) {
	var key windows.Handle
	var spec uint32
	var free bool
	err := windows.CryptAcquireCertificatePrivateKey(c.h.ctx, windows.CRYPT_ACQUIRE_ONLY_NCRYPT_KEY_FLAG, nil, &key, &spec, &free)
	if err != nil {
		return nil, fmt.Errorf("winsign: opening the key (is the card inserted?): %w", err)
	}
	if spec != windows.CERT_NCRYPT_KEY_SPEC {
		return nil, errors.New("winsign: the key isn't a CNG key")
	}
	switch c.Certificate.PublicKey.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
	default:
		if free {
			freeObject.Call(uintptr(key))
		}
		return nil, fmt.Errorf("winsign: %T keys aren't supported", c.Certificate.PublicKey)
	}
	return &cngSigner{key: key, free: free, pub: c.Certificate.PublicKey}, nil
}

type cngSigner struct {
	mu   sync.Mutex
	key  windows.Handle
	free bool
	pub  crypto.PublicKey
}

func (s *cngSigner) Public() crypto.PublicKey { return s.pub }

func (s *cngSigner) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != 0 && s.free {
		freeObject.Call(uintptr(s.key))
	}
	s.key = 0
	return nil
}

type pkcs1Padding struct{ algID *uint16 }

// Sign signs a digest: PKCS #1 v1.5 for RSA, and ECDSA as ASN.1, as Go's
// own keys do.
func (s *cngSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key == 0 {
		return nil, errors.New("winsign: the signer is closed")
	}
	if len(digest) == 0 || len(digest) != opts.HashFunc().Size() {
		return nil, errors.New("winsign: the digest doesn't match the hash")
	}
	var pad unsafe.Pointer
	var flags uint32
	var info pkcs1Padding
	if _, ok := s.pub.(*rsa.PublicKey); ok {
		var name string
		switch opts.HashFunc() {
		case crypto.SHA256:
			name = "SHA256"
		case crypto.SHA384:
			name = "SHA384"
		case crypto.SHA512:
			name = "SHA512"
		default:
			return nil, fmt.Errorf("winsign: hash %v isn't supported", opts.HashFunc())
		}
		if _, pss := opts.(*rsa.PSSOptions); pss {
			return nil, errors.New("winsign: RSA-PSS isn't supported")
		}
		info.algID = windows.StringToUTF16Ptr(name)
		pad, flags = unsafe.Pointer(&info), ncryptPadPKCS1
	}
	var n uint32
	if r, _, _ := signHash.Call(uintptr(s.key), uintptr(pad), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), 0, 0, uintptr(unsafe.Pointer(&n)), uintptr(flags)); r != 0 {
		return nil, cngError(r)
	}
	sig := make([]byte, n)
	if r, _, _ := signHash.Call(uintptr(s.key), uintptr(pad), uintptr(unsafe.Pointer(&digest[0])), uintptr(len(digest)), uintptr(unsafe.Pointer(&sig[0])), uintptr(n), uintptr(unsafe.Pointer(&n)), uintptr(flags)); r != 0 {
		return nil, cngError(r)
	}
	runtime.KeepAlive(info)
	sig = sig[:n]
	if _, ok := s.pub.(*ecdsa.PublicKey); ok {
		// CNG gives r and s side by side; Go and CMS want them in ASN.1
		half := len(sig) / 2
		return asn1.Marshal(struct{ R, S *big.Int }{new(big.Int).SetBytes(sig[:half]), new(big.Int).SetBytes(sig[half:])})
	}
	return sig, nil
}

func cngError(r uintptr) error {
	switch uint32(r) {
	case scardWCancelledByUser, nteUserCancelled:
		return errors.New("winsign: signing was canceled")
	}
	return fmt.Errorf("winsign: signing failed (0x%08X)", uint32(r))
}
