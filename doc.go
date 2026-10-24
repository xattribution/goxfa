// Package xfa opens XFA forms (Adobe LiveCycle and AEM Designer forms) in
// PDFs, lays them out the way Acrobat does, and fills them, in pure Go.
//
// Dynamic XFA forms have no ordinary PDF pages: only Acrobat and Reader
// can show them, and browsers and most other readers show a "Please wait"
// page instead. This package reads the form's design (the XFA template)
// and data (the datasets packet), merges them, and lays the pages out
// itself, so the form can be shown and filled anywhere:
//
//	f, err := xfa.Open(pdfBytes)
//	doc, err := f.Layout()            // pages of positioned boxes, text and fields
//	html := xfa.ViewerHTML(doc, opts) // a live form to type into
//	flat, err := xfa.Flatten(doc)     // an ordinary PDF any viewer shows
//	h := f.Handler(xfa.HandlerOptions{...}) // the live form, served
//	err = f.Set("form1[0].Page1[0].Name[0]", "DOE, JANE A")
//	saved, err := f.Save()            // the original PDF with the new values appended
//
// Save writes an incremental update with a new datasets packet, as Acrobat
// does, so Acrobat and Reader open the filled form with its own design,
// scripts and signature fields intact.
//
// What it covers: data binding (once, global, dataRef, none; repeating
// subforms), positioned and flowed layout (tb, lr-tb, rl-tb, tables and
// rows), growable fields and draws, captions, borders, fills, lines,
// rectangles, arcs, images, rich text, comb fields, check boxes, radio
// groups, choice lists, date display pictures, page areas with their
// master-page content, content areas, page and content-area breaks, and
// splitting flowed content across pages. Scripts (JavaScript and FormCalc)
// don't run; page-number fields are filled the way their scripts would.
package xfa
