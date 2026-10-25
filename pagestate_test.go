package xfa

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"strings"
	"testing"
)

// A form whose master page has "Page n of m" fields, as the DoD and Army
// forms do, two pages long, with a form packet like Acrobat's.
func pagedForm() []byte {
	tmpl := `<template xmlns="http://www.xfa.org/schema/xfa-template/3.3/">
<subform name="form1" layout="tb" locale="en_US">
<pageSet><pageArea name="Instructions" id="P1"><contentArea x="0.5in" y="0.5in" w="7.5in" h="9.5in"/><medium stock="letter" short="8.5in" long="11in"/>
<subform name="Footer" x="0.5in" y="10.2in" w="7.5in" h="0.3in">
<field name="CurrentPage" presence="hidden" w="1in" h="0.3in" access="readOnly"><ui><numericEdit/></ui>
<event activity="ready" ref="$layout"><script contentType="application/x-javascript">this.rawValue = xfa.layout.page(this);</script></event><bind match="none"/></field>
</subform>
<field name="PageCount" presence="hidden" x="2in" y="10.2in" w="1in" h="0.3in" access="readOnly"><ui><numericEdit/></ui>
<event activity="ready" ref="$layout"><script contentType="application/x-javascript">this.rawValue = xfa.layout.pageCount();</script></event><bind match="none"/></field>
</pageArea></pageSet>
<subform name="Part1" w="7.5in" h="6in"><field name="Name" w="4in" h="0.4in"><ui><textEdit/></ui></field></subform>
<subform name="Part2" w="7.5in" h="6in"><field name="Sig" w="3in" h="0.6in"><ui><signature/></ui></field></subform>
</subform></template>`
	ds := `<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/"><xfa:data><form1><Part1><Name/></Part1></form1></xfa:data></xfa:datasets>`
	cfg := `<config xmlns="http://www.xfa.org/schema/xci/3.1/"><present><pdf><version>1.7</version><interactive>1</interactive></pdf></present><acrobat><acrobat7><dynamicRender>required</dynamicRender></acrobat7></acrobat></config>`
	form := `<form checksum="AAAA" xmlns="http://www.xfa.org/schema/xfa-form/2.8/"><subform name="form1"><subform name="Part1"><field name="Name"/></subform></subform></form>`
	return testPDF(
		[2]string{"preamble", `<xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/">`},
		[2]string{"config", cfg},
		[2]string{"template", tmpl},
		[2]string{"datasets", ds},
		[2]string{"form", form},
		[2]string{"postamble", `</xdp:xdp>`},
	)
}

func formPacket(t *testing.T, pdf []byte) string {
	t.Helper()
	f, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range f.pdf.packets {
		if p.name == "form" {
			return string(p.raw)
		}
	}
	t.Fatal("no form packet")
	return ""
}

// Signing writes the page number fields into the form packet in the signed
// revision, a pageArea per page as Acrobat does, so Acrobat doesn't count
// them as filled in after the signature. What the form packet kept stays.
func TestSignWritesPageNumbers(t *testing.T) {
	f, err := Open(pagedForm())
	if err != nil {
		t.Fatal(err)
	}
	if doc, err := f.Layout(); err != nil || len(doc.Pages) != 2 {
		t.Fatalf("the form should take two pages: %v", err)
	}
	f.Set("form1[0].Part1[0].Name[0]", "DOE, JANE A")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signed, err := f.Sign(SignOptions{Field: "form1[0].Part2[0].Sig[0]", Signer: key, Chain: testSigner(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	checkByteRange(t, signed)
	got := formPacket(t, signed)
	want := `<pageSet><pageArea name="Instructions"><subform name="Footer"><field name="CurrentPage"><value><float>1.00000000</float></value></field></subform><field name="PageCount"><value><float>2.00000000</float></value></field></pageArea>` +
		`<pageArea name="Instructions"><subform name="Footer"><field name="CurrentPage"><value><float>2.00000000</float></value></field></subform><field name="PageCount"><value><float>2.00000000</float></value></field></pageArea></pageSet>`
	if !strings.Contains(got, want) || !strings.Contains(got, `<subform name="Part1"><field name="Name"/></subform>`) {
		t.Fatalf("form packet:\n%s", got)
	}
	// Saving again changes nothing more there.
	if fp, _ := f.pageState(); fp != nil {
		t.Error("the signed form's page numbers should already be right")
	}
}

// Saving writes them too.
func TestSaveWritesPageNumbers(t *testing.T) {
	f, err := Open(pagedForm())
	if err != nil {
		t.Fatal(err)
	}
	f.Set("form1[0].Part1[0].Name[0]", "DOE, JANE A")
	saved, err := f.Save()
	if err != nil {
		t.Fatal(err)
	}
	if got := formPacket(t, saved); !strings.Contains(got, `<field name="PageCount"><value><float>2.00000000</float></value></field>`) {
		t.Fatalf("form packet:\n%s", got)
	}
}

// A form without page number fields keeps its form packet as it was.
func TestNoPageNumbersNoChange(t *testing.T) {
	f, _ := openSample(t)
	if fp, b := f.pageState(); fp != nil {
		t.Errorf("changed a form packet with no page numbers:\n%s", b)
	}
}
