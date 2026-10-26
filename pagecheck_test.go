package xfa

import (
	"testing"
)

// Page numbers as Acrobat saves them, for TestRealForms: Acrobat keeps the
// "Page n of m" fields of master pages (bound to nothing, filled by
// layout:ready scripts) in the form packet, a pageArea element per page
// under the root subform's pageSet. Where a real form's form packet holds
// them for as many pages as goxfa lays out, goxfa's master pages and page
// numbers must be the same.
//
// goxfa writes them too, with a current checksum, when it saves or signs
// a form it may keep the state of (formstate.go).

func TestPageNumbered(t *testing.T) {
	paged := testForm(`<subform name="Part1" w="7.5in" h="2in"><field name="Name" w="4in" h="0.4in"><ui><textEdit/></ui></field></subform>`, `<Part1><Name/></Part1>`)
	f, err := Open(paged)
	if err != nil {
		t.Fatal(err)
	}
	if f.PageNumbered() {
		t.Error("a form without page numbers")
	}
	tmpl := `<subform name="Part1" w="7.5in" h="2in"><field name="Name" w="4in" h="0.4in"><ui><textEdit/></ui></field>
<field name="PageNo" w="1in" h="0.3in"><ui><numericEdit/></ui><event activity="ready" ref="$layout"><script contentType="application/x-javascript">this.rawValue = xfa.layout.page(this);</script></event><bind match="none"/></field></subform>`
	g, err := Open(testForm(tmpl, `<Part1><Name/></Part1>`))
	if err != nil {
		t.Fatal(err)
	}
	if !g.PageNumbered() {
		t.Error("a page number field bound to nothing")
	}
}
