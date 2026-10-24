package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	xfa "github.com/xattribution/goxfa"
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
	h := f.Handler(xfa.HandlerOptions{
		Title: filepath.Base(path),
		Save:  func(b []byte) error { return os.WriteFile(out, b, 0o644) },
	})
	fmt.Fprintf(os.Stderr, "open http://%s/ (Save writes %s)\n", addr, out)
	return http.ListenAndServe(addr, h)
}
