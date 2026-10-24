package xfa

import (
	"bytes"
	"fmt"
)

// testPDF builds a small unencrypted PDF whose AcroForm carries the given
// XFA packets, in order, as separate streams. A blank page stands in for
// the "Please wait" page dynamic forms carry.
func testPDF(packets ...[2]string) []byte {
	var b bytes.Buffer
	var offs []int
	obj := func(body string) int {
		offs = append(offs, b.Len())
		n := len(offs)
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, body)
		return n
	}
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	// 1 catalog, 2 pages, 3 page, 4 acroform, then the packet streams
	obj("<< /Type /Catalog /Pages 2 0 R /AcroForm 4 0 R /NeedsRendering true >>")
	obj("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	obj("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>")
	arr := "["
	for i, p := range packets {
		arr += fmt.Sprintf(" (%s) %d 0 R", p[0], 5+i)
	}
	arr += " ]"
	obj("<< /Fields [] /XFA " + arr + " >>")
	for _, p := range packets {
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(p[1]), p[1]))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)
	return b.Bytes()
}

// testForm is a PDF holding a dynamic form with the given template body
// (the children of the root subform) and data (the children of the root
// data group).
func testForm(body, data string) []byte {
	tmpl := `<template xmlns="http://www.xfa.org/schema/xfa-template/3.3/">
<subform name="form1" layout="tb" locale="en_US">
<pageSet><pageArea name="Page1" id="Page1"><contentArea x="0.5in" y="0.5in" w="7.5in" h="10in"/><medium stock="letter" short="8.5in" long="11in"/></pageArea></pageSet>
` + body + `
</subform></template>`
	ds := `<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/"><xfa:data><form1>` + data + `</form1></xfa:data></xfa:datasets>`
	cfg := `<config xmlns="http://www.xfa.org/schema/xci/3.1/"><present><pdf><version>1.7</version><interactive>1</interactive></pdf></present><acrobat><acrobat7><dynamicRender>required</dynamicRender></acrobat7></acrobat></config>`
	return testPDF(
		[2]string{"preamble", `<xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/">`},
		[2]string{"config", cfg},
		[2]string{"template", tmpl},
		[2]string{"datasets", ds},
		[2]string{"postamble", `</xdp:xdp>`},
	)
}
