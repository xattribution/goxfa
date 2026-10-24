// Command xfa works with XFA forms from the command line:
//
//	xfa info form.pdf                 the form's kind and fields
//	xfa fields form.pdf               the fields as JSON
//	xfa layout form.pdf > doc.json    the laid-out pages as JSON
//	xfa flatten form.pdf out.pdf      an ordinary PDF of the form as it stands
//	xfa html form.pdf > form.html     a standalone page with the live form
//	xfa fill form.pdf values.json out.pdf   set values (by SOM path) and save
//	xfa serve form.pdf [addr]         fill the form in a browser, and save it
//	xfa sigs form.pdf                 the signature fields and who signed them
//	xfa sign [flags] form.pdf field out.pdf   sign a signature field
//	xfa certs                         the signing certificates in the Windows store
package main

import (
	"encoding/json"
	"fmt"
	"os"

	xfa "github.com/xattribution/goxfa"
)

func main() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "certs":
			check(listCerts())
			return
		case "sign":
			check(signCmd(os.Args[2:]))
			return
		}
	}
	if len(os.Args) < 3 {
		usage()
	}
	cmd, path := os.Args[1], os.Args[2]
	data, err := os.ReadFile(path)
	check(err)
	f, err := xfa.Open(data)
	check(err)
	switch cmd {
	case "info":
		kind := "static"
		if f.Dynamic() {
			kind = "dynamic"
		}
		fs := f.Fields()
		fmt.Printf("%s XFA form, %d fields\n", kind, len(fs))
		for _, x := range fs {
			fmt.Printf("  %-9s %-60s %q\n", x.Kind, x.SOM, x.Value)
		}
	case "fields":
		out(f.Fields())
	case "layout":
		doc, err := f.Layout()
		check(err)
		out(doc)
	case "flatten":
		need(4)
		doc, err := f.Layout()
		check(err)
		pdf, err := xfa.Flatten(doc)
		check(err)
		check(os.WriteFile(os.Args[3], pdf, 0o644))
		fmt.Fprintf(os.Stderr, "%d pages\n", len(doc.Pages))
	case "html":
		doc, err := f.Layout()
		check(err)
		os.Stdout.Write(xfa.ViewerHTML(doc, xfa.ViewerOptions{Title: path}))
	case "fill":
		need(5)
		b, err := os.ReadFile(os.Args[3])
		check(err)
		var vals map[string]string
		check(json.Unmarshal(b, &vals))
		check(f.SetValues(vals))
		saved, err := f.Save()
		check(err)
		check(os.WriteFile(os.Args[4], saved, 0o644))
	case "sigs":
		for _, s := range f.SignatureFields() {
			state := "not signed"
			if s.Signed {
				state = "signed by " + s.Signer + " on " + s.Time.Format("02 Jan 06 15:04")
			}
			fmt.Printf("%-70s %s\n", s.SOM, state)
			if len(s.Locks) > 0 {
				fmt.Printf("  locks %d fields once signed\n", len(s.Locks))
			}
		}
	case "serve":
		addr := "127.0.0.1:47761"
		if len(os.Args) > 3 {
			addr = os.Args[3]
		}
		check(serve(f, path, addr))
	default:
		usage()
	}
}

func need(n int) {
	if len(os.Args) < n {
		usage()
	}
}

func out(v any) {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", " ")
	check(e.Encode(v))
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: xfa info|fields|layout|flatten|html|fill|serve|sigs form.pdf [args]\n       xfa sign [flags] form.pdf field out.pdf\n       xfa certs")
	os.Exit(2)
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
