# goxfa

Open, show, fill and save XFA forms in pure Go.

XFA forms are the PDF forms made in Adobe LiveCycle Designer and AEM Forms Designer. A *dynamic* XFA form has no ordinary PDF pages. Its pages exist only as a design (the template) and data (the datasets), and only Adobe Acrobat and Reader lay them out. Browsers and most other PDF readers show a "Please wait..." page instead. Many government and enterprise forms are built this way.

goxfa reads the design and the data, lays the pages out the way Acrobat does, and gives you three things:

- **A live form** that runs in any browser. Real inputs sit on the fields, and every change is laid out again, so repeating sections grow and the pages flow.
- **A flattened PDF** of the form as it stands, which any viewer can show and print.
- **The original PDF with the new values**, saved as an incremental update the way Acrobat saves. Acrobat and Reader open it with the form's own design, scripts and signature fields intact.

No cgo and no Adobe software. It builds for Windows, macOS, Linux, Android and WebAssembly.

```go
f, err := xfa.Open(pdfBytes)                       // encrypted forms open with the empty password, as Reader does
err = f.Set("form1[0].Page1[0].Name[0]", "DOE, JANE A")
doc, err := f.Layout()                             // pages of positioned boxes, text and fields
flat, err := xfa.Flatten(doc)                      // an ordinary PDF
page := xfa.ViewerHTML(doc, xfa.ViewerOptions{})   // a self-contained page with the live form
saved, err := f.Save()                             // the original bytes plus the new values
```

To serve a live form from your own program, mount the handler wherever you like:

```go
mux.Handle("/forms/2875/", http.StripPrefix("/forms/2875", f.Handler(xfa.HandlerOptions{
	Title: "DD 2875",
	Save:  func(pdf []byte) error { return os.WriteFile(out, pdf, 0o644) },
})))
```

The handler does no authentication. The program that mounts it decides who can reach it. If you draw forms in your own page instead, `xfa.ViewerJS()` and `xfa.ViewerCSS()` give you the viewer by itself.

## Signing

goxfa signs signature fields with any `crypto.Signer`, including a smart card through the Windows certificate store (package `winsign`, no cgo). The signature goes in one incremental update with any new values. It can carry an RFC 3161 timestamp and embedded OCSP answers, and fields in the signature's manifest are locked afterwards.

```go
certs, err := winsign.SigningCertificates()   // a CAC's Digital Signature certificate, for one
signer, err := certs[0].Signer()             // Windows asks for the PIN when it signs
signed, err := f.Sign(xfa.SignOptions{Field: som, Signer: signer, Chain: certs[0].Chain})
```

[docs/SIGNING.md](docs/SIGNING.md) explains how it works, how it's checked and what's left.

## The command

```
go install github.com/xattribution/goxfa/cmd/xfa@latest

xfa info form.pdf                       the form's kind and fields
xfa fields form.pdf                     the fields as JSON
xfa layout form.pdf > doc.json          the laid-out pages as JSON
xfa flatten form.pdf out.pdf            an ordinary PDF of the form as it stands
xfa html form.pdf > form.html           a standalone page with the live form
xfa fill form.pdf values.json out.pdf   set values (SOM path → value) and save
xfa serve form.pdf [127.0.0.1:47761]    fill the form in a browser; Save writes form-filled.pdf
xfa sigs form.pdf                       the signature fields and who signed them
xfa certs                               the signing certificates in the Windows store
xfa sign [-cert N] form.pdf field out.pdf   sign a field (-key and -chain take PEM files instead)
```

## What it covers

| Area | Covered |
|---|---|
| Packets | Template, datasets, config and form, as separate streams or one XDP. RC4 and AES encryption (standard security handler, empty user password). |
| Data binding | `once`, `global`, `dataRef`, `none`. Repeating subforms from `occur` and the data. Exclusion groups. Check box on, off and neutral values. Choice lists with separate shown and saved values. |
| Layout | Positioned, `tb`, `lr-tb`, `rl-tb`, tables and rows (column widths, `colSpan`). Growable widths and heights with their limits. Anchors, margins, captions with placement and reserve, borders (per-edge, dashes, fills), lines, rectangles, arcs, images and rotation. |
| Text | Plain and rich text (XHTML spans, bold, italic, underline, sizes, colors, line heights, indents, horizontal scale). Comb fields. Date display pictures. Floating fields embedded in text. |
| Pages | Page sets, page areas with their master-page content, content areas, `occur` limits, page and content-area breaks, overflow, and splitting flowed content across pages. Page numbers and counts. |
| Output | Layout as JSON. A flattened PDF that uses the standard 14 fonts, with check marks drawn as paths. A live HTML form. Values written back as an incremental update. |

## What it doesn't do (yet)

- **Scripts don't run.** Neither JavaScript nor FormCalc runs, so calculated fields keep their stored value and script-driven show and hide doesn't happen. Page-number fields are filled the way their scripts would fill them.
- **Fonts are approximated.** Text is measured with the standard 14 font metrics, with width corrections for common faces (Myriad, Calibri, Verdana, Tahoma, narrow faces). Line breaks match Acrobat on the forms tested, but a line can break one word early or late. The flattened PDF draws with the same metrics it measured with, so text never overflows its box.
- **Static XFA forms** already carry real PDF pages that every viewer shows. goxfa reads and fills them, but its layout of them is only approximate.
- **Signatures haven't been checked in Acrobat yet.** They validate in poppler and OpenSSL. See below.

## How it's checked

`go test ./...` runs against small forms built in the tests. Set `GOXFA_FORMS` to a folder of real forms and `TestRealForms` opens, lays out, flattens, fills, saves and reopens each one. Forms are never kept in the repository.

Layout has been compared page by page with pdf.js's XFA renderer on a dozen real-world government forms with up to 431 fields, most of them encrypted. Saved forms reopen with their values in goxfa and in pdf.js, and pass qpdf's structure check.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for how it fits together.

## License

Apache 2.0. See [LICENSE](LICENSE).
