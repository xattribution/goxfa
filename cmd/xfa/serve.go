package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	xfa "github.com/xattribution/goxfa"
	"github.com/xattribution/goxfa/winsign"
)

// serve fills a form in the browser: the page shows the live form, every
// change comes back here to be laid out again, and Save writes the filled
// PDF beside the original as name-filled.pdf. It listens on loopback only.
func serve(f *xfa.Form, path, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("serve listens on this computer only: use 127.0.0.1 or localhost, not %s", host)
	}
	out := strings.TrimSuffix(path, filepath.Ext(path)) + "-filled.pdf"
	ho := xfa.HandlerOptions{
		Title: filepath.Base(path),
		Save:  func(b []byte) error { return os.WriteFile(out, b, 0o644) },
	}
	if runtime.GOOS == "windows" {
		// signature fields sign with the store's signing certificate
		ho.Sign = func(string) (xfa.SignOptions, error) {
			certs, err := winsign.SigningCertificates()
			if err != nil {
				return xfa.SignOptions{}, err
			}
			if len(certs) != 1 {
				return xfa.SignOptions{}, fmt.Errorf("%d signing certificates found; sign with 'xfa sign -cert N' instead", len(certs))
			}
			s, err := certs[0].Signer()
			if err != nil {
				return xfa.SignOptions{}, err
			}
			return xfa.SignOptions{Signer: s, Chain: certs[0].Chain}, nil
		}
	}
	h := f.Handler(ho)
	fmt.Fprintf(os.Stderr, "open http://%s/ (Save writes %s)\n", addr, out)
	return http.ListenAndServe(addr, h)
}
