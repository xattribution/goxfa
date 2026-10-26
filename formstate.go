package xfa

import (
	"bytes"
	"fmt"
	"strings"
)

// The form packet: what Acrobat keeps of a form's state beside its data,
// and the checksum that says the state still goes with that data.
//
// Acrobat writes the form packet when it saves a dynamic form: instance
// managers, fields' override flags, and the "Page n of m" fields of master
// pages, which are bound to nothing and filled by layout:ready scripts,
// under the root subform's pageSet (a pageArea element per page). Its
// checksum covers the template and the datasets (formChecksum). When the
// checksum matches, Acrobat uses the saved state; when it doesn't, Acrobat
// merges the data again and runs the scripts. A signed form then shows the
// page numbers as "Form Fields Filled In" after the signature, and the bar
// says there are unsigned changes, though nothing in the file changed.
//
// So when Save and Sign write the datasets, they also write the form packet
// with the page numbers for the current layout (pageState) and the checksum
// for the datasets they write. Both are needed: Acrobat flagged a signed
// form with only one of them (docs/SIGNING.md).
//
// They do this only for forms whose state goxfa can stand behind
// (stateSafe): a form with calculate or initialize scripts gets its
// checksum left as it was, so Acrobat runs those scripts itself, since
// goxfa runs none.

// WritesState says whether Save and Sign keep this form's state current
// for Acrobat: its form packet's checksum and page numbers. False for a
// form that calculates fields or runs initialize scripts, for a form with
// no form packet, and for one kept as a single XDP stream. A form that
// fills in its own page numbers (PageNumbered) and doesn't keep its state
// is listed in Acrobat as filled in after a goxfa signature.
func (f *Form) WritesState() bool {
	if f == nil || f.pdf == nil || f.pdf.whole {
		return false
	}
	fp, tp := f.statePackets()
	if fp == nil || tp == nil {
		return false
	}
	t, err := ParseXML(tp.raw)
	if err != nil {
		return false
	}
	return stateSafe(t)
}

// statePackets are the form and template packets, or nil.
func (f *Form) statePackets() (fp, tp *packet) {
	for i := range f.pdf.packets {
		switch f.pdf.packets[i].name {
		case "form":
			fp = &f.pdf.packets[i]
		case "template":
			tp = &f.pdf.packets[i]
		}
	}
	return fp, tp
}

// stateSafe says a template runs no script that Acrobat would skip when it
// uses saved state and that changes what the form shows: a calculate
// script (but the page numbers'), or an initialize event's script.
func stateSafe(t *Node) bool {
	safe := true
	var walk func(n *Node)
	walk = func(n *Node) {
		if !safe || n == nil {
			return
		}
		if n.Kind == ElemNode {
			switch n.Local {
			case "calculate":
				if s := n.Child("script"); s != nil && strings.TrimSpace(s.TextContent()) != "" && n.Parent != nil && pageNumberScript(n.Parent) == "" {
					safe = false
				}
			case "event":
				if n.Attr("activity") == "initialize" {
					if s := n.Child("script"); s != nil && strings.TrimSpace(s.TextContent()) != "" {
						safe = false
					}
				}
			}
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(t)
	return safe
}

// formPacketFor is the form packet to write beside datasets ds: the page
// numbers set (pageState) and the checksum for the template and ds. nil
// when the form's state isn't goxfa's to keep (WritesState) or nothing
// changes.
func (f *Form) formPacketFor(ds []byte) (*packet, []byte) {
	if !f.WritesState() {
		return nil, nil
	}
	fp, tp := f.statePackets()
	body := fp.raw
	if p, b := f.pageState(); p != nil {
		body = b
	}
	doc, err := ParseXML(body)
	if err != nil {
		return nil, nil
	}
	form := doc.FirstElem()
	if form == nil || form.Local != "form" {
		return nil, nil
	}
	form.SetAttr("checksum", formChecksum(tp.raw, ds))
	out := doc.Bytes()
	if bytes.Equal(out, fp.raw) {
		return nil, nil
	}
	return fp, out
}

// datasetsNow is the datasets packet as the next revision will hold it.
func (f *Form) datasetsNow() []byte {
	if f.dirty {
		return f.dsDoc.Bytes()
	}
	for _, p := range f.pdf.packets {
		if p.name == "datasets" {
			return p.raw
		}
	}
	return nil
}

// pageState is the form packet with the page number fields set for the
// current layout, or nil when there's nothing to change: no form packet,
// no page number fields, or the values are already there.
func (f *Form) pageState() (*packet, []byte) {
	if f.pdf == nil || f.pdf.whole || f.root == nil {
		return nil, nil
	}
	var fp *packet
	for i := range f.pdf.packets {
		if f.pdf.packets[i].name == "form" {
			fp = &f.pdf.packets[i]
		}
	}
	if fp == nil {
		return nil, nil
	}
	areas := f.pageAreasLaidOut()
	if len(areas) == 0 {
		return nil, nil
	}
	has := false
	for _, pa := range areas {
		if pa != nil && len(pageNumberFields(pa, nil)) > 0 {
			has = true
			break
		}
	}
	if !has {
		return nil, nil
	}
	doc, err := ParseXML(fp.raw)
	if err != nil {
		return nil, nil
	}
	form := doc.FirstElem()
	if form == nil || form.Local != "form" {
		return nil, nil
	}
	root := form.Child("subform")
	if root == nil {
		// A form packet that keeps nothing yet: the root subform, holding
		// only the pages.
		root = form.Append(NewElem("subform"))
		if f.root.Name != "" {
			root.SetAttr("name", f.root.Name)
		}
	}
	old := root.Child("pageSet")
	var had []*Node
	if old != nil {
		had = old.Children("pageArea")
	}
	// The pages Acrobat laid out the same way keep their elements as they
	// are, so a form packet that's already right isn't written again.
	same := old != nil && len(had) == len(areas)
	for i, pa := range areas {
		if same && (pa == nil || had[i].Attr("name") != pa.Name) {
			same = false
		}
	}
	set, changed := old, false
	if !same {
		set, changed = NewElem("pageSet"), true
	}
	for i, pa := range areas {
		if pa == nil {
			continue
		}
		// What Acrobat kept for this page (other fields' state) stays.
		var el *Node
		if i < len(had) && had[i].Attr("name") == pa.Name {
			el = had[i]
		} else {
			el = NewElem("pageArea")
			if pa.Name != "" {
				el.SetAttr("name", pa.Name)
			}
		}
		if !same {
			set.Append(el)
		}
		for _, pf := range pageNumberFields(pa, nil) {
			v := i + 1
			if pageNumberScript(pf.field.T) == "pages" {
				v = len(areas)
			}
			if setPagedValue(el, pf, v) {
				changed = true
			}
		}
	}
	if !changed {
		return nil, nil
	}
	if !same {
		if old != nil {
			for k, n := range root.Kids {
				if n == old {
					set.Parent = root
					root.Kids[k] = set
				}
			}
		} else {
			root.Append(set)
		}
	}
	return fp, doc.Bytes()
}

// pageAreasLaidOut is the master page each page of the current layout
// uses, in order (nil for a page made with none).
func (f *Form) pageAreasLaidOut() (areas []*Inst) {
	defer func() {
		if recover() != nil {
			areas = nil
		}
	}()
	L := &layouter{f: f}
	doc := &Doc{Dynamic: f.dynamic}
	L.paginate(doc)
	return doc.areas
}

// pagedField is a page number field and the containers between its page
// area and it.
type pagedField struct {
	path  []*Inst
	field *Inst
}

// pageNumberFields lists the page number fields under a page area.
func pageNumberFields(in *Inst, path []*Inst) []pagedField {
	var out []pagedField
	for _, k := range in.Kids {
		switch k.Kind {
		case "field":
			if pageNumberScript(k.T) != "" {
				out = append(out, pagedField{path: append([]*Inst(nil), path...), field: k})
			}
		case "subform", "area", "subformSet", "exclGroup":
			out = append(out, pageNumberFields(k, append(path, k))...)
		}
	}
	return out
}

// setPagedValue writes a page number field's value under a page area's
// element, making the containers on the way as Acrobat names them, and
// says whether anything changed.
func setPagedValue(el *Node, pf pagedField, v int) bool {
	cur, made := el, false
	for _, c := range pf.path {
		var m bool
		cur, m = childNamed(cur, c.Kind, c.Name)
		made = made || m
	}
	fe, m := childNamed(cur, "field", pf.field.Name)
	made = made || m
	kind, text := "float", fmt.Sprintf("%d.00000000", v)
	if vt := pf.field.T.Child("value"); vt != nil {
		if t := vt.FirstElem(); t != nil {
			switch t.Local {
			case "integer":
				kind, text = "integer", fmt.Sprint(v)
			case "decimal":
				kind, text = "decimal", fmt.Sprint(v)
			case "text":
				kind, text = "text", fmt.Sprint(v)
			}
		}
	}
	if kind == "float" {
		if ui, _ := uiKind(pf.field.T); ui == "textEdit" {
			kind, text = "text", fmt.Sprint(v)
		}
	}
	if vals := fe.Children("value"); len(vals) == 1 {
		if t := vals[0].FirstElem(); t != nil && t.Local == kind && strings.TrimSpace(t.TextContent()) == text {
			return made
		}
	}
	for _, k := range fe.Children("value") {
		fe.Remove(k)
	}
	val := fe.Append(NewElem("value"))
	val.Append(NewElem(kind)).SetText(text)
	return true
}

// childNamed is n's first child element of that kind and name, made at the
// end when there's none (made says so).
func childNamed(n *Node, kind, name string) (c *Node, made bool) {
	for _, k := range n.Children(kind) {
		if k.Attr("name") == name {
			return k, false
		}
	}
	c = NewElem(kind)
	if name != "" {
		c.SetAttr("name", name)
	}
	return n.Append(c), true
}
