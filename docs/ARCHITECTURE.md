# How goxfa fits together

```
PDF bytes ──Open──▶ packets ──parse──▶ template + datasets ──merge──▶ form (instances)
                                                                        │
                        Set / SetValues ◀──────── values ◀──────────────┤
                                                                        │
                                                         Layout ──▶ Doc (pages of items)
                                                                        │
                                       ┌──────────────┬────────────────┼──────────────┐
                                    Flatten       ViewerHTML         JSON          Save
                                  (plain PDF)    (live form)      (your UI)   (incremental update)
```

## Files

| File | What it does |
|---|---|
| pdfio.go | Reads the PDF with pdfcpu, finds the XFA packets in the AcroForm `/XFA` entry (an array of named streams, or one XDP stream), and decrypts them (RC4 and AES, standard security handler). Writes changed packets back as an incremental update: new stream objects encrypted with the file's own key, then an xref stream or a classic xref table to match the original, and a trailer pointing at the previous one. |
| xmltree.go | A small XML tree that writes back what it read: namespaces, prefixes, comments and processing instructions survive a round trip. Templates are not changed. Only the datasets packet is written back. |
| form.go | `Open`, `Save`, and the packets the form is built from. Reads `dynamicRender` from the config packet. |
| template.go | Template helpers: prototypes (`use` and `usehref`) resolved into the template, then presence, occur, margins, fonts, paragraphs and the widget kind, each with XFA's defaults. |
| merge.go | The merge: one instance per container, repeated as `occur` and the data say, with each field bound to its data node (`once`, `global`, `dataRef`, `none`). Instances get their SOM paths (`form1[0].Page1[0].Name[0]`, with `#subform` for unnamed subforms). Data nodes for unbound repeats are made only when a value is written. |
| values.go | Field values: reading them, setting them (with check boxes, choice lists, radio groups and dates converted to what the form stores), and describing fields for programs (`Fields`). |
| picture.go | Date picture clauses, both ways: `date{DD MMM YY}` for display, `date{YYYYMMDD}` for storage. |
| font.go | Fonts: typeface names mapped to the standard 14 fonts, with a width scale for faces that run narrower or wider than Helvetica. Measuring uses pdfcpu's metrics. |
| text.go | Text: plain and rich (XHTML) paragraphs into spans, then lines broken to a width, with alignment, indents, line heights and spacing. Each laid-out line keeps its measured width. |
| layout.go | Layout: boxes for subforms, fields and draws, sized from the template and their content. Containers are positioned or flowed (`tb`, `lr-tb`, `rl-tb`, tables). Then pagination: page areas, content areas, breaks, splitting flowed boxes across pages, master-page content, and page numbers. |
| flatten.go | A small PDF writer: every item drawn with the standard 14 fonts at the measured widths, check marks drawn as paths, and images embedded. |
| viewer.go, viewer/ | The live form: an SVG of each page with HTML inputs placed on the fields. Text is stretched to the width Go measured, so lines break where the layout put them, whatever fonts the browser has. |
| handler.go | An `http.Handler` that serves the viewer, takes changes, lays the form out again, saves and signs. |
| pdfobj.go | Writes PDF objects into an update, encrypting their strings with the object's key. |
| sign.go | Signing: the AcroForm field for the SOM path, the signature dictionary with its holes, the byte range, and reading back who signed what. Manifest locks. |
| internal/cms | CMS SignedData built by hand in DER: detached signatures for PDFs, with the signed and unsigned attributes PDF readers look for. |
| tsa.go | RFC 3161 timestamps and OCSP answers, fetched only when a program asks. |
| winsign/ | The Windows certificate store and CNG signing, through crypt32.dll and ncrypt.dll. |

## The Doc

`Layout` returns plain data, so any program can draw it:

- `Doc{Pages, Fonts, Dynamic}`. Fonts are listed once and referred to by index.
- `Page{W, H, Items}`, in points, with the origin at the top left.
- `Item.T` is `rect`, `line`, `ellipse`, `text`, `image`, `field` or `group`. A group is rotated content: its items are relative to its origin, rotated counterclockwise by `Rot` degrees.
- Text is lines of runs. Each run has a font index, its text, its x position and its measured width. Each line has a baseline.
- A field item carries a `FieldBox` with its SOM path, kind, value, display text, options, font, alignment and limits. Its caption and borders are separate items before it.

## Choices worth knowing

- **Measure once, draw the same.** The layout measures with the standard 14 font metrics, and the flattened PDF draws with those fonts. The viewer stretches each run to the measured width. So a line never overflows its box in either output, even where the real typeface (Arial, Myriad) would be a little wider or narrower.
- **A little slack in line breaking.** Designer sizes boxes to Acrobat's measure of their text, which can be a fraction of a point under the standard 14 metrics. Lines get 0.2% of their width as slack, so a label that fits in Acrobat fits here too.
- **Saving never rewrites.** The original bytes stay exactly as they were. Usage-rights signatures and document signatures keep covering what they signed. Acrobat sees one more revision, the same thing it writes itself.
- **Scripts don't run.** Running Acrobat's JavaScript and FormCalc object models faithfully is a project of its own. The scripts forms rely on for layout are page numbers, and those are recognized and filled. Show-and-hide scripts are the biggest gap: a section a script would reveal stays as the template and data leave it.
- **Bounded.** A template or data asking for more than 100,000 objects or 2,000 pages is refused. Panics inside `Open` and `Layout` come back as errors.
