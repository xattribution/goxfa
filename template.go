package xfa

import (
	"strconv"
	"strings"
)

// Template helpers: prototypes, and reading the properties layout needs
// with XFA's defaults.

// containers are the template elements that take part in layout.
var containers = map[string]bool{
	"subform": true, "subformSet": true, "field": true, "draw": true,
	"exclGroup": true, "area": true,
}

// properties are child elements that occur once and describe their parent,
// so a prototype can supply them when the element has none of its own.
var properties = map[string]bool{
	"assist": true, "bind": true, "border": true, "break": true, "breakAfter": true,
	"breakBefore": true, "calculate": true, "caption": true, "desc": true, "extras": true,
	"font": true, "format": true, "items": true, "keep": true, "margin": true, "occur": true,
	"overflow": true, "para": true, "traversal": true, "ui": true, "validate": true,
	"value": true, "edge": true, "corner": true, "fill": true, "color": true,
	"textEdit": true, "checkButton": true, "choiceList": true, "dateTimeEdit": true,
	"numericEdit": true, "passwordEdit": true, "signature": true, "imageEdit": true,
	"button": true, "barcode": true, "picture": true, "text": true, "exData": true,
	"comb": true, "line": true, "rectangle": true, "arc": true, "image": true,
	"medium": true, "pageSet": true, "toolTip": true, "speak": true,
}

// resolveProtos applies use="#id" and usehref="#id" references in place:
// an element takes the attributes it doesn't set and the property
// elements it doesn't have from the element it references, recursively.
// References that leave the document (usehref to another file) are left
// alone.
func resolveProtos(tmpl *Node) {
	byID := map[string]*Node{}
	var index func(n *Node)
	index = func(n *Node) {
		for _, k := range n.Kids {
			if k.Kind != ElemNode {
				continue
			}
			if id := k.Attr("id"); id != "" {
				if _, dup := byID[id]; !dup {
					byID[id] = k
				}
			}
			index(k)
		}
	}
	index(tmpl)
	done := map[*Node]bool{}
	var apply func(n *Node, depth int)
	apply = func(n *Node, depth int) {
		if done[n] || depth > 32 {
			return
		}
		done[n] = true
		ref := n.Attr("use")
		if ref == "" {
			if h := n.Attr("usehref"); strings.HasPrefix(h, "#") {
				ref = h
			}
		}
		if strings.HasPrefix(ref, "#") {
			if p := byID[ref[1:]]; p != nil && p != n {
				apply(p, depth+1)
				inherit(n, p)
			}
		}
		for _, k := range n.Kids {
			if k.Kind == ElemNode {
				apply(k, depth+1)
			}
		}
	}
	apply(tmpl, 0)
}

// inherit copies what n lacks from its prototype p.
func inherit(n, p *Node) {
	for _, a := range p.Attrs {
		switch a.Name.Local {
		case "id", "use", "usehref", "name":
			continue
		}
		if !n.HasAttr(a.Name.Local) {
			n.Attrs = append(n.Attrs, a)
		}
	}
	for _, pk := range p.Elems() {
		if !properties[pk.Local] {
			continue
		}
		if own := n.Child(pk.Local); own != nil {
			if pk.Local != "value" && pk.Local != "items" {
				inherit(own, pk) // merge nested properties (font inside caption, edge inside border)
			}
			continue
		}
		n.Append(clone(pk))
	}
}

func clone(n *Node) *Node {
	c := &Node{Kind: n.Kind, Space: n.Space, Local: n.Local, Text: n.Text}
	c.Attrs = append(c.Attrs, n.Attrs...)
	for _, k := range n.Kids {
		c.Append(clone(k))
	}
	return c
}

// presence is how an element shows: "visible", "hidden", "invisible" or
// "inactive".
func presence(n *Node) string {
	switch p := n.Attr("presence"); p {
	case "hidden", "invisible", "inactive":
		return p
	}
	return "visible"
}

// printable is false for elements marked relevant="-print" (screen only),
// and true for "+print" or none.
func printable(n *Node) bool {
	r := n.Attr("relevant")
	for _, t := range strings.Fields(r) {
		if t == "-print" {
			return false
		}
	}
	return true
}

// onScreen is false for relevant="-screen" or "+print" alone.
func onScreen(n *Node) bool {
	for _, t := range strings.Fields(n.Attr("relevant")) {
		if t == "-screen" || t == "+print" {
			return false
		}
	}
	return true
}

// occurOf is an element's occur settings: min, max (-1 unlimited) and
// initial. The defaults are 1, 1, 1; a missing initial is min.
func occurOf(n *Node) (lo, hi, initial int) {
	o := n.Child("occur")
	if o == nil {
		return 1, 1, 1
	}
	lo = max(0, atoiDef(o.Attr("min"), 1))
	hi = atoiDef(o.Attr("max"), 1)
	if !o.HasAttr("max") && o.HasAttr("min") && lo > 1 {
		hi = lo
	}
	if hi >= 0 && hi < lo {
		hi = lo
	}
	initial = atoiDef(o.Attr("initial"), lo)
	if hi >= 0 {
		initial = min(initial, hi)
	}
	initial = max(initial, lo)
	return lo, hi, initial
}

// margins are an element's insets: top, right, bottom, left.
type margins struct{ t, r, b, l float64 }

func marginOf(n *Node) margins {
	m := n.Child("margin")
	if m == nil {
		return margins{}
	}
	return margins{
		t: attrMeasure(m, "topInset", 0), r: attrMeasure(m, "rightInset", 0),
		b: attrMeasure(m, "bottomInset", 0), l: attrMeasure(m, "leftInset", 0),
	}
}

// fontOf reads a <font> element over a parent font.
func fontOf(n *Node, parent Font) Font {
	f := parent
	if n == nil {
		return f
	}
	if t := n.Attr("typeface"); t != "" {
		f.Typeface = t
	}
	if n.HasAttr("size") {
		if v := measure(n.Attr("size")); v > 0 {
			f.Size = v
		}
	}
	if n.HasAttr("weight") {
		f.Bold = n.Attr("weight") == "bold"
	}
	if n.HasAttr("posture") {
		f.Italic = n.Attr("posture") == "italic"
	}
	if n.HasAttr("underline") {
		f.Underline = n.Attr("underline") != "0"
	}
	if n.HasAttr("lineThrough") {
		f.Strike = n.Attr("lineThrough") != "0"
	}
	if fill := n.Child("fill"); fill != nil {
		f.Color = colorOf(fill, f.Color)
	}
	if v := n.Attr("fontHorizontalScale"); strings.HasSuffix(v, "%") {
		if pc, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64); err == nil && pc > 10 && pc < 1000 {
			f.HScale = pc / 100
			if pc == 100 {
				f.HScale = 0
			}
		}
	}
	return f
}

// defaultFont is XFA's: Courier 10pt. Designer always sets one.
var defaultFont = Font{Typeface: "Courier", Size: 10}

// para is paragraph formatting.
type para struct {
	hAlign, vAlign          string
	spaceAbove, spaceBelow  float64
	marginLeft, marginRight float64
	textIndent              float64
	lineHeight              float64 // 0: the font's
}

func paraOf(n *Node) para {
	p := para{hAlign: "left", vAlign: "top"}
	e := n.Child("para")
	if e == nil {
		return p
	}
	if v := e.Attr("hAlign"); v != "" {
		p.hAlign = v
	}
	if v := e.Attr("vAlign"); v != "" {
		p.vAlign = v
	}
	p.spaceAbove = attrMeasure(e, "spaceAbove", 0)
	p.spaceBelow = attrMeasure(e, "spaceBelow", 0)
	p.marginLeft = attrMeasure(e, "marginLeft", 0)
	p.marginRight = attrMeasure(e, "marginRight", 0)
	p.textIndent = attrMeasure(e, "textIndent", 0)
	p.lineHeight = attrMeasure(e, "lineHeight", 0)
	return p
}

// uiKind is the widget of a field: textEdit, checkButton, choiceList,
// dateTimeEdit, numericEdit, passwordEdit, signature, imageEdit, button,
// barcode. A field without a ui is a textEdit.
func uiKind(field *Node) (string, *Node) {
	ui := field.Child("ui")
	for _, k := range ui.Elems() {
		switch k.Local {
		case "picture", "extras":
			continue
		}
		return k.Local, k
	}
	return "textEdit", nil
}
