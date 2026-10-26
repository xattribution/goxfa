package xfa

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"regexp"
	"strings"
	"testing"
)

// The checksum as PDFium computes it. Each value was computed by a second,
// independent implementation of the same rules, which reproduces the
// checksums Acrobat wrote on a real DD 2875 and on 40 of 43 published
// forms; the three others look published stale (docs/SIGNING.md).
func TestFormChecksum(t *testing.T) {
	for _, c := range []struct{ name, template, datasets, want string }{
		{"Acrobat's style", "<template xmlns=\"http://www.xfa.org/schema/xfa-template/3.3/\"\n><?formServer defaultPDFRenderFormat acrobat10.0dynamic?><subform name=\"form1\"\n><field name=\"a\"\n/></subform\n></template\n>",
			"<xfa:datasets xmlns:xfa=\"http://www.xfa.org/schema/xfa-data/1.0/\"\n><xfa:data\n><form1\n><a\n>TEST1</a\n></form1\n></xfa:data\n></xfa:datasets\n>", "y+0OEiaJkMuy0d+PJ7amlZmpRnE="},
		{"compact", `<template xmlns="http://www.xfa.org/schema/xfa-template/3.3/"><subform name="form1"><field name="a"/></subform></template>`,
			`<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/"><xfa:data><form1><a>TEST1</a></form1></xfa:data></xfa:datasets>`, "OaIbGL8tBtYzP9hvjIBqoG0EWNM="},
		{"spaces, comments, CDATA, entities", "<template a='1'  b=\"x &amp; y\">\n  <!-- note -->\n  <field name=\"a\">  <value><text><![CDATA[x<y]]></text></value>\n  </field>\n</template>",
			"<d>  text  <e/>\n</d>", "rX/7kAAwWmhADx6v0fzolf9jhPE="},
	} {
		if got := formChecksum([]byte(c.template), []byte(c.datasets)); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// Whitespace alone before a start tag doesn't count; before an end tag it does.
	if formChecksum([]byte("<a>\n  <b/></a>")) != formChecksum([]byte("<a><b/></a>")) {
		t.Error("whitespace before a start tag changed the checksum")
	}
	if formChecksum([]byte("<a><b/>\n</a>")) == formChecksum([]byte("<a><b/></a>")) {
		t.Error("whitespace before an end tag didn't change the checksum")
	}
}

// pagedTemplate is a form with a page number on its master page, as the
// DD 2875 has; extra goes in the body.
func pagedTemplate(extra string) string {
	return `<template xmlns="http://www.xfa.org/schema/xfa-template/3.3/">
<subform name="form1" layout="tb" locale="en_US">
<pageSet><pageArea name="Page1" id="Page1"><contentArea x="0.5in" y="0.5in" w="7.5in" h="9.5in"/><medium stock="letter" short="8.5in" long="11in"/>
<field name="CurrentPage" x="6in" y="10.2in" w="1in" h="0.3in"><ui><numericEdit/></ui><event activity="ready" ref="$layout"><script contentType="application/x-javascript">this.rawValue = xfa.layout.page(this);</script></event><bind match="none"/></field>
<field name="PageCount" x="7in" y="10.2in" w="1in" h="0.3in"><ui><numericEdit/></ui><event activity="ready" ref="$layout"><script contentType="application/x-javascript">this.rawValue = xfa.layout.pageCount();</script></event><bind match="none"/></field>
</pageArea></pageSet>
<subform name="Part1" w="7.5in" h="2in"><field name="Name" w="4in" h="0.4in"><ui><textEdit/></ui></field>
<field name="Sig" y="1in" w="4in" h="0.6in"><ui><signature/></ui></field>` + extra + `</subform>
</subform></template>`
}

// statefulForm is a dynamic form with a form packet whose checksum is
// current, as a form comes from Designer.
func statefulForm(tmpl string) []byte { return statefulFormChecksum(tmpl, "") }

// statefulFormChecksum is statefulForm with the checksum ck in its form
// packet, or the current one for "".
func statefulFormChecksum(tmpl, ck string) []byte {
	ds := `<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/"><xfa:data><form1><Part1><Name/></Part1></form1></xfa:data></xfa:datasets>`
	if ck == "" {
		ck = formChecksum([]byte(tmpl), []byte(ds))
	}
	form := `<form checksum="` + ck + `" xmlns="http://www.xfa.org/schema/xfa-form/2.8/"/>`
	return testPDF(
		[2]string{"preamble", `<xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/">`},
		[2]string{"template", tmpl},
		[2]string{"datasets", ds},
		[2]string{"form", form},
		[2]string{"postamble", `</xdp:xdp>`},
	)
}

// packetOf is a packet as the last revision holds it.
func packetOf(t *testing.T, pdf []byte, name string) []byte {
	t.Helper()
	f, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range f.pdf.packets {
		if p.name == name {
			return p.raw
		}
	}
	t.Fatalf("no %s packet", name)
	return nil
}

var reChecksum = regexp.MustCompile(`checksum="([^"]*)"`)

// current says the form packet's checksum goes with the template and
// datasets, and gives the form packet.
func current(t *testing.T, pdf []byte) (bool, string) {
	t.Helper()
	form := packetOf(t, pdf, "form")
	m := reChecksum.FindSubmatch(form)
	return m != nil && string(m[1]) == formChecksum(packetOf(t, pdf, "template"), packetOf(t, pdf, "datasets")), string(form)
}

// Save and Sign write the form packet with the page numbers and a checksum
// for the datasets they write, in the same revision.
func TestSaveAndSignKeepState(t *testing.T) {
	pdf := statefulForm(pagedTemplate(""))
	f, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	if !f.WritesState() || !f.PageNumbered() {
		t.Fatalf("WritesState %v, PageNumbered %v", f.WritesState(), f.PageNumbered())
	}
	if err := f.Set("form1[0].Part1[0].Name[0]", "TEST, SAMPLE A"); err != nil {
		t.Fatal(err)
	}
	saved, err := f.Save()
	if err != nil {
		t.Fatal(err)
	}
	ok, form := current(t, saved)
	if !ok || !strings.Contains(form, `<pageArea name="Page1"><field name="CurrentPage"><value><float>1.00000000</float></value></field><field name="PageCount"><value><float>1.00000000</float></value></field></pageArea>`) {
		t.Fatalf("after Save, the form packet is %s", form)
	}
	if !bytes.HasPrefix(saved, pdf) {
		t.Error("Save changed the original bytes")
	}

	// Signing a filled form: the values, the form packet and the signature in one revision.
	g, _ := Open(pdf)
	g.Set("form1[0].Part1[0].Name[0]", "TEST, SAMPLE B")
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signed, err := g.Sign(SignOptions{Field: "form1[0].Part1[0].Sig[0]", Signer: key, Chain: testSigner(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	checkByteRange(t, signed)
	if ok, form := current(t, signed); !ok || !strings.Contains(form, "CurrentPage") {
		t.Fatalf("after Sign, the form packet is %s", form)
	}
	if bytes.Count(signed[len(pdf):], []byte("%%EOF")) != 1 {
		t.Error("Sign wrote more than one revision")
	}

	// A form already kept current isn't written again.
	h, _ := Open(signed)
	if fp, _ := h.formPacketFor(h.datasetsNow()); fp != nil {
		t.Error("a current form packet was written again")
	}
}

// A form with a stale checksum (a server filled a value without Acrobat)
// is put right when it's signed, even with no value set.
func TestSignRepairsStaleChecksum(t *testing.T) {
	pdf := statefulFormChecksum(pagedTemplate(""), "MIB25FnBZasEliCtWB4r+OAr2EY=")
	f, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	signed, err := f.Sign(SignOptions{Field: "form1[0].Part1[0].Sig[0]", Signer: key, Chain: testSigner(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	if ok, form := current(t, signed); !ok {
		t.Fatalf("the stale checksum stayed: %s", form)
	}
}

// A form that calculates fields or runs initialize scripts keeps its form
// packet as it was: Acrobat runs those scripts when the checksum doesn't
// match, and goxfa runs none. So does a form without a form packet.
func TestStateLeftForScripts(t *testing.T) {
	calc := `<field name="Total" y="1.6in" w="2in" h="0.3in"><ui><numericEdit/></ui><calculate><script contentType="application/x-javascript">this.rawValue = 2 + 2;</script></calculate></field>`
	init := `<field name="Today" y="1.6in" w="2in" h="0.3in"><ui><textEdit/></ui><event activity="initialize"><script contentType="application/x-javascript">this.rawValue = "x";</script></event></field>`
	for name, extra := range map[string]string{"calculate": calc, "initialize": init} {
		pdf := statefulForm(pagedTemplate(extra))
		f, err := Open(pdf)
		if err != nil {
			t.Fatal(err)
		}
		if f.WritesState() || !f.PageNumbered() {
			t.Errorf("%s: WritesState %v, PageNumbered %v", name, f.WritesState(), f.PageNumbered())
		}
		before := packetOf(t, pdf, "form")
		f.Set("form1[0].Part1[0].Name[0]", "TEST")
		saved, err := f.Save()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(packetOf(t, saved, "form"), before) {
			t.Errorf("%s: the form packet changed", name)
		}
	}
	// Event scripts that run on a user's action, and the page numbers' own, don't count.
	safe := `<field name="Date" y="1.6in" w="2in" h="0.3in"><ui><textEdit/></ui><event activity="enter"><script contentType="application/x-javascript">this.rawValue = "x";</script></event></field>`
	if f, _ := Open(statefulForm(pagedTemplate(safe))); !f.WritesState() {
		t.Error("an enter script stopped the state being written")
	}
	g, _ := Open(testForm(`<subform name="Part1" w="7.5in" h="2in"><field name="Name" w="4in" h="0.4in"><ui><textEdit/></ui></field></subform>`, `<Part1><Name/></Part1>`))
	if g.WritesState() {
		t.Error("a form without a form packet writes state")
	}
}
