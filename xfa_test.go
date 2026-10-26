package xfa

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const sampleBody = `
<subform name="Page1" w="7.5in" h="3in">
  <field name="Name" x="0" y="0" w="4in" h="0.4in">
    <ui><textEdit/></ui><font typeface="Arial" size="10pt"/>
    <caption placement="top" reserve="0.15in"><value><text>NAME (Last, First, MI)</text></value><font typeface="Arial" size="7pt"/></caption>
    <border><edge/></border>
  </field>
  <field name="Agree" x="4.2in" y="0" w="1.5in" h="0.3in">
    <ui><checkButton size="10pt"/></ui>
    <items><integer>1</integer><integer>0</integer></items>
    <caption placement="right"><value><text>I agree</text></value></caption>
  </field>
  <field name="Branch" x="0" y="0.5in" w="2in" h="0.3in">
    <ui><choiceList/></ui>
    <items><text>Army</text><text>Navy</text><text>Air Force</text></items>
    <items save="1" presence="hidden"><text>A</text><text>N</text><text>F</text></items>
  </field>
  <exclGroup name="Status" x="2.2in" y="0.5in" w="3in" h="0.3in" layout="lr-tb">
    <field name="Active" w="1in" h="0.3in"><ui><checkButton shape="round"/></ui><items><text>active</text></items><caption placement="right"><value><text>Active</text></value></caption></field>
    <field name="Reserve" w="1in" h="0.3in"><ui><checkButton shape="round"/></ui><items><text>reserve</text></items><caption placement="right"><value><text>Reserve</text></value></caption></field>
  </exclGroup>
  <field name="Signed" x="0" y="1in" w="2in" h="0.3in">
    <ui><dateTimeEdit/></ui>
    <format><picture>date{DD MMM YY}</picture></format>
    <bind match="once"><picture>date{YYYYMMDD}</picture></bind>
  </field>
  <field name="Sig" x="3.5in" y="1.5in" w="3in" h="0.5in">
    <ui><signature><manifest action="include"><ref>xfa[0].template[0].form1[0].Page1[0].Name[0]</ref></manifest></signature></ui>
    <caption placement="top" reserve="0.15in"><value><text>SIGNATURE</text></value></caption>
  </field>
  <draw name="Note" x="0" y="1.5in" w="3in">
    <value><exData contentType="text/html"><body xmlns="http://www.w3.org/1999/xhtml"><p>Plain <span style="font-weight:bold">bold</span> text that runs long enough to wrap onto a second line here.</p></body></exData></value>
    <font typeface="Arial" size="9pt"/>
  </draw>
</subform>
<subform name="Item" layout="tb" w="7.5in">
  <occur min="1" max="-1"/>
  <field name="Line" w="7.5in" h="1in"><ui><textEdit multiLine="1"/></ui><border><edge/></border></field>
</subform>`

const sampleData = `<Page1><Name>DOE, JANE A</Name><Agree>0</Agree><Branch>N</Branch><Status>reserve</Status><Signed>20261016</Signed></Page1>` +
	`<Item><Line>one</Line></Item><Item><Line>two</Line></Item>`

func openSample(t *testing.T) (*Form, []byte) {
	t.Helper()
	pdf := testForm(sampleBody, sampleData)
	f, err := Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	return f, pdf
}

func TestOpenReadsValues(t *testing.T) {
	f, _ := openSample(t)
	if !f.Dynamic() {
		t.Error("dynamicRender required: want Dynamic")
	}
	want := map[string]string{
		"form1[0].Page1[0].Name[0]":   "DOE, JANE A",
		"form1[0].Page1[0].Agree[0]":  "0",
		"form1[0].Page1[0].Branch[0]": "N",
		"form1[0].Page1[0].Status[0]": "reserve",
		"form1[0].Page1[0].Signed[0]": "20261016",
		"form1[0].Item[0].Line[0]":    "one",
		"form1[0].Item[1].Line[0]":    "two",
	}
	for som, v := range want {
		got, ok := f.Value(som)
		if !ok || got != v {
			t.Errorf("%s = %q, %v; want %q", som, got, ok, v)
		}
	}
	kinds := map[string]string{}
	for _, fd := range f.Fields() {
		kinds[fd.SOM] = fd.Kind
		if fd.SOM == "form1[0].Page1[0].Signed[0]" && fd.Display != "16 Oct 26" {
			t.Errorf("date display = %q, want 16 Oct 26", fd.Display)
		}
		if fd.SOM == "form1[0].Page1[0].Branch[0]" && fd.Display != "Navy" {
			t.Errorf("choice display = %q, want Navy", fd.Display)
		}
	}
	for som, k := range map[string]string{
		"form1[0].Page1[0].Name[0]":   "text",
		"form1[0].Page1[0].Agree[0]":  "check",
		"form1[0].Page1[0].Branch[0]": "choice",
		"form1[0].Page1[0].Status[0]": "radio", // a group: its members are its options
		"form1[0].Item[1].Line[0]":    "multiline",
		"form1[0].Page1[0].Signed[0]": "date",
	} {
		if kinds[som] != k {
			t.Errorf("%s kind = %q, want %q", som, kinds[som], k)
		}
	}
}

func TestSetAndSave(t *testing.T) {
	f, orig := openSample(t)
	vals := map[string]string{
		"form1[0].Page1[0].Name[0]":   "ROE, RICHARD <&> \"Q\"",
		"form1[0].Page1[0].Agree[0]":  "1",
		"form1[0].Page1[0].Branch[0]": "F",
		"form1[0].Item[1].Line[0]":    "line one\nline two",
	}
	if err := f.SetValues(vals); err != nil {
		t.Fatal(err)
	}
	// a radio member sets its group
	if err := f.Set("form1[0].Page1[0].Status[0].Active[0]", "active"); err != nil {
		t.Fatal(err)
	}
	vals["form1[0].Page1[0].Status[0]"] = "active"
	// dates are typed as shown and stored as bound
	if err := f.Set("form1[0].Page1[0].Signed[0]", "17 Oct 26"); err != nil {
		t.Fatal(err)
	}
	vals["form1[0].Page1[0].Signed[0]"] = "20261017"
	if err := f.Set("form1[0].Nope[0]", "x"); err == nil {
		t.Error("setting a missing field: want an error")
	}
	saved, err := f.Save()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(saved, orig) {
		t.Fatal("save must append to the original bytes, not rewrite them")
	}
	g, err := Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	for som, v := range vals {
		if got, _ := g.Value(som); got != v {
			t.Errorf("after save %s = %q, want %q", som, got, v)
		}
	}
	if _, err := api.ReadContext(context.Background(), bytes.NewReader(saved), relaxed()); err != nil {
		t.Errorf("saved PDF doesn't read: %v", err)
	}
}

func relaxed() *model.Configuration {
	c := model.NewDefaultConfiguration()
	c.ValidationMode = model.ValidationRelaxed
	c.Offline = true
	return c
}

func TestLayout(t *testing.T) {
	f, _ := openSample(t)
	doc, err := f.Layout()
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 1 || doc.Pages[0].W != 612 || doc.Pages[0].H != 792 {
		t.Fatalf("pages = %+v", doc.Pages)
	}
	fields := map[string]Item{}
	var texts []string
	var walk func([]Item)
	walk = func(items []Item) {
		for _, it := range items {
			if it.Field != nil {
				fields[it.Field.SOM] = it
			}
			for _, l := range it.Lines {
				for _, r := range l.Runs {
					texts = append(texts, r.Text)
				}
			}
			walk(it.Items)
		}
	}
	walk(doc.Pages[0].Items)
	name := fields["form1[0].Page1[0].Name[0]"]
	// the content area starts at 0.5in; the caption reserves 0.15in on top
	if name.X != 36 || name.Y != 36+10.8 || name.H != 0.4*72-10.8 {
		t.Errorf("Name widget at %v,%v h %v", name.X, name.Y, name.H)
	}
	// the repeated subforms flow below Page1 (3in tall), one inch each
	l0, l1 := fields["form1[0].Item[0].Line[0]"], fields["form1[0].Item[1].Line[0]"]
	if l0.Y != 36+216 || l1.Y != l0.Y+72 {
		t.Errorf("repeated lines at %v and %v", l0.Y, l1.Y)
	}
	all := strings.Join(texts, "|")
	for _, w := range []string{"NAME (Last, First, MI)", "I agree", "Reserve", "bold", "16 Oct 26"} {
		if !strings.Contains(all, w) {
			t.Errorf("laid-out text lacks %q: %s", w, all)
		}
	}
	// the rich text wraps within 3in
	for _, it := range doc.Pages[0].Items {
		if it.T == "text" && len(it.Lines) > 0 && strings.HasPrefix(it.Lines[0].Runs[0].Text, "Plain") {
			if len(it.Lines) < 2 {
				t.Error("long rich text didn't wrap")
			}
			for _, l := range it.Lines {
				if l.X+l.W > 3*72+0.5 {
					t.Errorf("line wider than its box: %v", l.W)
				}
			}
		}
	}
}

func TestPagination(t *testing.T) {
	var data strings.Builder
	for range 30 {
		data.WriteString("<Item><Line>x</Line></Item>")
	}
	f, err := Open(testForm(sampleBody, data.String()))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := f.Layout()
	if err != nil {
		t.Fatal(err)
	}
	// 10in of content area: Page1 (3in) and 7 lines on the first page,
	// then 10 a page
	if len(doc.Pages) != 4 {
		t.Errorf("30 one-inch lines: %d pages, want 4", len(doc.Pages))
	}
	for i, p := range doc.Pages {
		for _, it := range p.Items {
			if it.Y < 36-0.01 || it.Y+it.H > 36+720+0.01 {
				t.Errorf("page %d: item outside the content area: %+v", i+1, it)
			}
		}
	}
}

func TestFlatten(t *testing.T) {
	f, _ := openSample(t)
	doc, err := f.Layout()
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := Flatten(doc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := api.ReadContext(context.Background(), bytes.NewReader(pdf), relaxed())
	if err != nil {
		t.Fatalf("flattened PDF doesn't read: %v", err)
	}
	if err := api.ValidateContext(context.Background(), ctx); err != nil {
		t.Errorf("flattened PDF isn't valid: %v", err)
	}
	if ctx.PageCount != len(doc.Pages) {
		t.Errorf("%d pages, want %d", ctx.PageCount, len(doc.Pages))
	}
}

func TestViewerEscapes(t *testing.T) {
	f, _ := openSample(t)
	if err := f.Set("form1[0].Page1[0].Name[0]", "</script><script>alert(1)</script>"); err != nil {
		t.Fatal(err)
	}
	doc, err := f.Layout()
	if err != nil {
		t.Fatal(err)
	}
	page := string(ViewerHTML(doc, ViewerOptions{Title: "<b>t</b>"}))
	if strings.Contains(page, "alert(1)</script>") || strings.Contains(page, "<b>t</b>") {
		t.Error("values and the title must be escaped in the page")
	}
	if strings.Count(page, "<script>") != 2 {
		t.Errorf("want exactly the viewer's two scripts")
	}
}

func TestHandler(t *testing.T) {
	f, _ := openSample(t)
	var saved []byte
	h := f.Handler(HandlerOptions{Title: "Sample", Save: func(b []byte) error { saved = b; return nil }})
	srv := httptest.NewServer(http.StripPrefix("/form", h))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/form/")
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("GET: %v %v", err, r.Status)
	}
	r.Body.Close()
	r, err = http.Post(srv.URL+"/form/set", "application/json", strings.NewReader(`{"SOM":"form1[0].Page1[0].Name[0]","Value":"SMITH, J"}`))
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("set: %v %v", err, r.Status)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if !strings.Contains(string(b), "SMITH, J") {
		t.Error("set should answer with the new layout")
	}
	r, _ = http.Post(srv.URL+"/form/set", "application/json", strings.NewReader(`{"SOM":"form1[0].Missing[0]","Value":"x"}`))
	if r.StatusCode != 400 {
		t.Errorf("unknown field: %s, want 400", r.Status)
	}
	r.Body.Close()
	r, _ = http.Post(srv.URL+"/form/save", "", nil)
	r.Body.Close()
	g, err := Open(saved)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := g.Value("form1[0].Page1[0].Name[0]"); v != "SMITH, J" {
		t.Errorf("saved name = %q", v)
	}
}

func TestXMLRoundTrip(t *testing.T) {
	src := `<?xml version="1.0" encoding="UTF-8"?>
<!-- c --><a xmlns="urn:x" xmlns:p="urn:p"><b p:k="1 &amp; 2">t &lt; u</b><c/><?pi x?><![CDATA[raw <x>]]></a>`
	n, err := ParseXML([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseXML(n.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(n.Bytes(), m.Bytes()) {
		t.Errorf("not stable:\n%s\n%s", n.Bytes(), m.Bytes())
	}
	b := n.FirstElem().Child("b")
	if b.Attr("k") != "1 & 2" || b.TextContent() != "t < u" {
		t.Errorf("b = %q %q", b.Attr("k"), b.TextContent())
	}
}

func TestNotXFA(t *testing.T) {
	_, err := Open(testPDF())
	if err != ErrNoXFA {
		t.Errorf("err = %v, want ErrNoXFA", err)
	}
	if _, err := Open([]byte("not a pdf")); err == nil {
		t.Error("garbage: want an error")
	}
}

// TestRealForms opens, lays out, flattens, fills and saves every PDF in
// $GOXFA_FORMS. Forms aren't kept in the repository.
func TestRealForms(t *testing.T) {
	dir := os.Getenv("GOXFA_FORMS")
	if dir == "" {
		t.Skip("set GOXFA_FORMS to a folder of XFA forms")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.pdf"))
	if len(paths) == 0 {
		t.Skip("no PDFs in GOXFA_FORMS")
	}
	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			f, err := Open(data)
			if err == ErrNoXFA {
				t.Skip("no XFA")
			}
			if err != nil {
				t.Fatal(err)
			}
			doc, err := f.Layout()
			if err != nil {
				t.Fatal(err)
			}
			if len(doc.Pages) == 0 {
				t.Fatal("no pages")
			}
			if _, err := Flatten(doc); err != nil {
				t.Fatal(err)
			}
			// Where Acrobat saved the page numbers for as many pages as
			// goxfa lays out (a form filled since may run longer), goxfa's
			// master pages and numbers are the same (pagestate.go).
			for _, pk := range f.pdf.packets {
				if pk.name == "form" && bytes.Contains(pk.raw, []byte("<pageSet")) && bytes.Contains(pk.raw, []byte("CurrentPage")) && bytes.Count(pk.raw, []byte("<pageArea")) == len(doc.Pages) {
					if fp, b := f.pageState(); fp != nil {
						t.Errorf("page numbers differ from Acrobat's:\n%s", b[bytes.Index(b, []byte("<pageSet")):])
					}
				}
			}
			var som string
			for _, fd := range f.Fields() {
				if (fd.Kind == "text" || fd.Kind == "multiline") && fd.Bound && !fd.ReadOnly && !fd.Hidden {
					som = fd.SOM
					break
				}
			}
			if som == "" {
				t.Skip("no text field to fill")
			}
			if err := f.Set(som, "DOE, JANE A"); err != nil {
				t.Fatal(err)
			}
			saved, err := f.Save()
			if err != nil {
				t.Fatal(err)
			}
			// the form packet goes with the new datasets (formstate.go)
			if f.WritesState() {
				if ok, form := current(t, saved); !ok {
					t.Errorf("after Save the form packet's checksum isn't current: %.300s", form)
				}
			}
			g, err := Open(saved)
			if err != nil {
				t.Fatal(err)
			}
			if v, _ := g.Value(som); v != "DOE, JANE A" {
				t.Errorf("%s after save = %q", som, v)
			}
			// sign the first signature field, with the value in the same revision
			sigs := g.SignatureFields()
			if len(sigs) == 0 {
				t.Logf("%d pages, %d fields, no signature fields", len(doc.Pages), len(f.Fields()))
				return
			}
			key := must(rsa.GenerateKey(rand.Reader, 2048))
			if err := g.Set(som, "ROE, RICHARD"); err != nil && !errors.Is(err, ErrLocked) {
				t.Fatal(err)
			}
			signed, err := g.Sign(SignOptions{Field: sigs[0].SOM, Signer: key, Chain: testSigner(t, key)})
			if err != nil {
				t.Fatalf("signing %s: %v", sigs[0].SOM, err)
			}
			checkByteRange(t, signed)
			out := pdfsig(t, signed)
			if out != "" && !strings.Contains(out, "Signature is Valid") {
				t.Errorf("pdfsig:\n%s", out)
			}
			if os.Getenv("GOXFA_OUT") != "" {
				os.WriteFile(filepath.Join(os.Getenv("GOXFA_OUT"), "signed-"+filepath.Base(p)), signed, 0o644)
			}
			t.Logf("%d pages, %d fields, signed %s", len(doc.Pages), len(f.Fields()), sigs[0].SOM)
		})
	}
}

// FuzzOpen feeds damaged forms through Open, Layout and Flatten: they may
// fail, but never panic or hang.
func FuzzOpen(f *testing.F) {
	f.Add(testForm(sampleBody, sampleData))
	f.Add(testForm(`<subform name="a" layout="table" columnWidths="1in -1"><subform layout="row"><draw w="1in"><value><text>x</text></value></draw></subform></subform>`, ""))
	f.Fuzz(func(t *testing.T, b []byte) {
		form, err := Open(b)
		if err != nil {
			return
		}
		form.Fields()
		if doc, err := form.Layout(); err == nil {
			Flatten(doc)
		}
	})
}
