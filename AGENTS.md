# goxfa

Pure-Go XFA forms: read the packets out of a PDF, merge template and data, lay the pages out the way Acrobat does, show them as a live HTML form or a flattened PDF, and save values back as an incremental update. No cgo, ever.

## Every change

1. `gofmt -l .` prints nothing; `go vet ./...` and `GOOS=windows go vet ./...` pass.
2. `go test ./...` passes. With real forms at hand, `GOXFA_FORMS=<folder> go test -run RealForms -v .` passes too.
3. A layout change is checked by eye: flatten the affected forms, render the pages (`pdftoppm -r 72`), and compare them with a reference (Acrobat, pdf.js with `enableXfa`, or the form's own static pages). Look at every page, not just the first.
4. Every target in the CI cross-build still builds.

## Rules

- PDFs are untrusted input. Check sizes and offsets, recover panics at the API edge (`Open`, `Layout`), and never let a template or data value reach the page unescaped.
- Saving appends: the original bytes stay untouched, so signatures and usage rights keep covering what they signed.
- No form PDFs in the repository (`.gitignore`). Real forms can carry personal data and file paths in their packets.
- Write docs and comments in clear, plain English (American spelling).

## Where to look

- How it fits together: docs/ARCHITECTURE.md.
- Signing, and what it will take: docs/SIGNING.md.
