package main

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"

	xfa "github.com/xattribution/goxfa"
	"github.com/xattribution/goxfa/winsign"
)

// signCmd signs one signature field. On Windows the key comes from the
// certificate store (a smart card included); elsewhere, or with -key,
// from PEM files.
func signCmd(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	certN := fs.Int("cert", 0, "which certificate from 'xfa certs' to sign with (1, 2, ...)")
	keyPath := fs.String("key", "", "a PEM private key to sign with instead of the Windows store")
	chainPath := fs.String("chain", "", "with -key: the PEM certificates, signing certificate first")
	reason := fs.String("reason", "", "the reason for signing")
	location := fs.String("location", "", "where it was signed")
	tsa := fs.String("tsa", "", "an RFC 3161 timestamp authority's URL")
	withOCSP := fs.Bool("ocsp", false, "ask the certificates' OCSP responders and embed the answers")
	cades := fs.Bool("cades", false, "make a CAdES (PAdES) signature")
	fetch := fs.Bool("fetch-issuers", true, "fetch issuer certificates the chain is missing from the certificate's issuer URL")
	fs.Parse(args)
	if fs.NArg() != 3 {
		return errors.New("usage: xfa sign [flags] form.pdf field out.pdf")
	}
	in, field, out := fs.Arg(0), fs.Arg(1), fs.Arg(2)

	var signer crypto.Signer
	var chain []*x509.Certificate
	switch {
	case *keyPath != "":
		k, c, err := pemSigner(*keyPath, *chainPath)
		if err != nil {
			return err
		}
		signer, chain = k, c
	case runtime.GOOS == "windows":
		certs, err := winsign.SigningCertificates()
		if err != nil {
			return err
		}
		if len(certs) == 0 {
			return errors.New("no signing certificate found; is the card inserted?")
		}
		n := *certN
		if n == 0 && len(certs) == 1 {
			n = 1
		}
		if n < 1 || n > len(certs) {
			printCerts(certs)
			return errors.New("choose one with -cert N")
		}
		s, err := certs[n-1].Signer()
		if err != nil {
			return err
		}
		defer s.Close()
		signer, chain = s, certs[n-1].Chain
	default:
		return errors.New("give -key and -chain: the certificate store is only on Windows")
	}

	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	f, err := xfa.Open(data)
	if err != nil {
		return err
	}
	if *fetch {
		if full, err := xfa.CompleteChain(chain, nil); err == nil {
			chain = full
		} else {
			fmt.Fprintf(os.Stderr, "the chain stays at %d certificates: %v\n", len(chain), err)
		}
	}
	o := xfa.SignOptions{Field: field, Signer: signer, Chain: chain, Reason: *reason, Location: *location, CAdES: *cades}
	if *tsa != "" {
		o.Timestamp = xfa.TimestampAuthority(*tsa, nil)
	}
	if *withOCSP {
		if o.OCSP, err = xfa.FetchOCSP(chain, nil); err != nil {
			return err
		}
	}
	signed, err := f.Sign(o)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, signed, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "signed %s as %s\n", field, chain[0].Subject.CommonName)
	return nil
}

func listCerts() error {
	certs, err := winsign.SigningCertificates()
	if err != nil {
		return err
	}
	if len(certs) == 0 {
		fmt.Println("no signing certificates; is the card inserted?")
		return nil
	}
	printCerts(certs)
	return nil
}

func printCerts(certs []*winsign.Cert) {
	for i, c := range certs {
		issuer := c.Certificate.Issuer.CommonName
		fmt.Printf("%d  %s  (issued by %s, valid to %s, %d in chain)\n", i+1, c.Certificate.Subject.CommonName, issuer, c.Certificate.NotAfter.Format("02 Jan 06"), len(c.Chain))
	}
}

func pemSigner(keyPath, chainPath string) (crypto.Signer, []*x509.Certificate, error) {
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	blk, _ := pem.Decode(kb)
	if blk == nil {
		return nil, nil, errors.New("the key file isn't PEM")
	}
	var key any
	if key, err = x509.ParsePKCS8PrivateKey(blk.Bytes); err != nil {
		if key, err = x509.ParsePKCS1PrivateKey(blk.Bytes); err != nil {
			if key, err = x509.ParseECPrivateKey(blk.Bytes); err != nil {
				return nil, nil, errors.New("the key isn't PKCS #8, PKCS #1 or EC")
			}
		}
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("the key can't sign")
	}
	if chainPath == "" {
		return nil, nil, errors.New("-key needs -chain")
	}
	cb, err := os.ReadFile(chainPath)
	if err != nil {
		return nil, nil, err
	}
	var chain []*x509.Certificate
	for {
		var b *pem.Block
		b, cb = pem.Decode(cb)
		if b == nil {
			break
		}
		if b.Type == "CERTIFICATE" {
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil, nil, err
			}
			chain = append(chain, c)
		}
	}
	if len(chain) == 0 {
		return nil, nil, errors.New("the chain file has no certificates")
	}
	return signer, chain, nil
}
