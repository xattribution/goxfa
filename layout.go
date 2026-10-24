package xfa

import (
	"math"
	"sort"
	"strings"
)

// Layout: sizing every container, placing flowed and positioned content,
// and paginating into page areas' content areas.

// Doc is a laid-out form: pages of items in points, origin at each page's
// top left, y down.
type Doc struct {
	Pages   []Page `json:"pages"`
	Fonts   []Font `json:"fonts"`
	Dynamic bool   `json:"dynamic"`
}

// Page is one page.
type Page struct {
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Items []Item  `json:"items"`
}

// Item is something drawn on a page.
//
//	rect     a box: Fill, Edges (one for all sides, or top, right, bottom, left), Radius
//	line     from X,Y to X2,Y2 with Edges[0]
//	ellipse  in X,Y,W,H with Edges[0] and Fill
//	text     Lines, relative to X,Y
//	image    Src (a data: URL) in X,Y,W,H
//	field    Field: an input in X,Y,W,H, with Lines for its printed value
//	group    Items rotated by Rot degrees (counterclockwise) about X,Y
type Item struct {
	T       string     `json:"t"`
	X       float64    `json:"x"`
	Y       float64    `json:"y"`
	W       float64    `json:"w,omitempty"`
	H       float64    `json:"h,omitempty"`
	X2      float64    `json:"x2,omitempty"`
	Y2      float64    `json:"y2,omitempty"`
	Fill    string     `json:"fill,omitempty"`
	Edges   []Edge     `json:"edges,omitempty"`
	Radius  float64    `json:"r,omitempty"`
	Lines   []TextLine `json:"lines,omitempty"`
	Field   *FieldBox  `json:"field,omitempty"`
	Src     string     `json:"src,omitempty"`
	Rot     float64    `json:"rot,omitempty"`
	Items   []Item     `json:"items,omitempty"`
	NoPrint bool       `json:"noPrint,omitempty"`
	Clip    bool       `json:"clip,omitempty"`
}

// Edge is a stroke.
type Edge struct {
	W     float64 `json:"w"`
	Color string  `json:"color,omitempty"`
	Dash  string  `json:"dash,omitempty"` // dashed, dotted, dashDot, dashDotDot
}

// FieldBox is a fillable field on a page.
type FieldBox struct {
	SOM      string   `json:"som"`
	Kind     string   `json:"kind"`
	Value    string   `json:"value"`
	Display  string   `json:"display,omitempty"`
	Options  []string `json:"options,omitempty"`
	Saves    []string `json:"saves,omitempty"`
	On       string   `json:"on,omitempty"`
	Group    string   `json:"group,omitempty"` // a radio button's exclusion group
	Required bool     `json:"required,omitempty"`
	ReadOnly bool     `json:"readOnly,omitempty"`
	MaxChars int      `json:"maxChars,omitempty"`
	Comb     int      `json:"comb,omitempty"`
	Font     int      `json:"font"`
	HAlign   string   `json:"hAlign,omitempty"`
	VAlign   string   `json:"vAlign,omitempty"`
	Mark     string   `json:"mark,omitempty"`  // check boxes: check, circle, cross, diamond, square, star
	Shape    string   `json:"shape,omitempty"` // check boxes: square or round
	Tip      string   `json:"tip,omitempty"`
	Page     string   `json:"page,omitempty"` // "page" or "pages": filled with the page number or count
}

type brk struct {
	kind     string // pageArea, contentArea
	target   string // a page area or content area's id or name, "" for the next
	startNew bool
}

// lbox is a laid-out container.
type lbox struct {
	in     *Inst
	x, y   float64 // in the parent's content region
	w, h   float64
	kids   []*lbox
	items  []Item // relative to the box's top left
	flow   bool   // children are flowed and may be split across pages
	m      margins
	deco   *Node // a subform's border, drawn per fragment
	bb, ba *brk
	rot    float64
	nop    bool // not printed
}

// maxPages bounds a layout; no real form comes near it.
const maxPages = 2000

type layouter struct {
	f     *Form
	fonts fontTable
	byID  map[string]*Inst // instances by template id, for embedded fields
}

// embed is the text of a floating field embedded in rich text.
func (L *layouter) embed(ref string) string {
	if !strings.HasPrefix(ref, "#") {
		return ""
	}
	if L.byID == nil {
		L.byID = map[string]*Inst{}
		for _, in := range L.f.order {
			if id := in.T.Attr("id"); id != "" {
				if _, dup := L.byID[id]; !dup {
					L.byID[id] = in
				}
			}
		}
	}
	in := L.byID[ref[1:]]
	if in == nil {
		return ""
	}
	switch pageNumberScript(in.T) {
	case "page":
		return pagePlaceholder
	case "pages":
		return pagesPlaceholder
	}
	return L.f.display(in)
}

// Layout lays the form out with its current values.
func (f *Form) Layout() (doc *Doc, err error) {
	defer func() {
		if p := recover(); p != nil {
			doc, err = nil, errString("xfa: layout failed: "+toString(p))
		}
	}()
	L := &layouter{f: f}
	doc = &Doc{Dynamic: f.dynamic}
	L.paginate(doc)
	doc.Fonts = L.fonts.list
	return doc, nil
}

func toString(p any) string {
	if e, ok := p.(error); ok {
		return e.Error()
	}
	if s, ok := p.(string); ok {
		return s
	}
	return "unexpected error"
}

// ---- sizes ----

type extent struct {
	w, h       float64
	wFix, hFix bool
	minW, maxW float64
	minH, maxH float64
	x, y       float64
	anchor     string
}

func extentOf(t *Node) extent {
	e := extent{anchor: t.Attr("anchorType")}
	if t.HasAttr("w") {
		e.w, e.wFix = measure(t.Attr("w")), true
	}
	if t.HasAttr("h") {
		e.h, e.hFix = measure(t.Attr("h")), true
	}
	e.minW, e.maxW = attrMeasure(t, "minW", 0), attrMeasure(t, "maxW", 0)
	e.minH, e.maxH = attrMeasure(t, "minH", 0), attrMeasure(t, "maxH", 0)
	e.x, e.y = attrMeasure(t, "x", 0), attrMeasure(t, "y", 0)
	return e
}

// growW is a growable width clamped to min and max.
func (e extent) growW(v float64) float64 {
	v = max(v, e.minW)
	if e.maxW > 0 {
		v = min(v, e.maxW)
	}
	return v
}

func (e extent) growH(v float64) float64 {
	v = max(v, e.minH)
	if e.maxH > 0 {
		v = min(v, e.maxH)
	}
	return v
}

// anchorOffset is how far the box's top left sits from its anchor point.
func anchorOffset(anchor string, w, h float64) (float64, float64) {
	ax, ay := 0.0, 0.0
	switch {
	case strings.HasSuffix(anchor, "Center"):
		ax = 0.5
	case strings.HasSuffix(anchor, "Right"):
		ax = 1
	}
	switch {
	case strings.HasPrefix(anchor, "middle"):
		ay = 0.5
	case strings.HasPrefix(anchor, "bottom"):
		ay = 1
	}
	return -ax * w, -ay * h
}

// ---- dispatch ----

// box lays out one instance for an available width. force sets the
// width or height (table cells, rows); 0 leaves it free.
func (L *layouter) box(in *Inst, availW, forceW, forceH float64) *lbox {
	var b *lbox
	switch in.Kind {
	case "draw":
		b = L.drawBox(in, availW, forceW, forceH)
	case "field":
		b = L.fieldBox(in, availW, forceW, forceH)
	default:
		b = L.containerBox(in, availW, forceW, forceH)
	}
	if b == nil {
		return nil
	}
	if in.Presence == "invisible" {
		stripItems(b)
	}
	if !printable(in.T) {
		markNoPrint(b)
	}
	b.rot = math.Mod(attrFloat(in.T, "rotate"), 360)
	return b
}

func attrFloat(n *Node, a string) float64 {
	return float64(atoiDef(n.Attr(a), 0))
}

func stripItems(b *lbox) {
	b.items = nil
	b.deco = nil
	for _, k := range b.kids {
		stripItems(k)
	}
}

func markNoPrint(b *lbox) {
	b.nop = true
	for i := range b.items {
		b.items[i].NoPrint = true
	}
	for _, k := range b.kids {
		markNoPrint(k)
	}
}

// visibleKids are an instance's children that take part in layout, with
// subform sets opened up (their children lay out in the parent).
func visibleKids(in *Inst) []*Inst {
	var out []*Inst
	for _, k := range in.Kids {
		switch k.Kind {
		case "pageSet":
			continue
		}
		if k.Presence == "hidden" || k.Presence == "inactive" {
			continue
		}
		if k.Kind == "subformSet" {
			out = append(out, visibleKids(k)...)
			continue
		}
		out = append(out, k)
	}
	return out
}

// ---- containers ----

func (L *layouter) containerBox(in *Inst, availW, forceW, forceH float64) *lbox {
	t := in.T
	e := extentOf(t)
	m := marginOf(t)
	if in.Kind == "area" {
		m = margins{}
	}
	b := &lbox{in: in, m: m}
	b.bb, b.ba = breaksOf(t)
	layout := t.Attr("layout")
	if in.Kind == "area" || in.Kind == "pageArea" {
		layout = "position"
	}
	if layout == "" {
		layout = "position"
	}
	w := e.w
	switch {
	case forceW > 0:
		w = forceW
	case !e.wFix:
		w = 0 // decided by content
	}
	contentW := w - m.l - m.r
	if w == 0 {
		contentW = max(0, availW-m.l-m.r)
		if layout == "position" {
			contentW = 0
		}
	}
	kids := visibleKids(in)
	var ext float64 // content height
	var extW float64
	switch layout {
	case "tb":
		b.flow = true
		y := 0.0
		for _, k := range kids {
			kb := L.box(k, contentW, 0, 0)
			if kb == nil {
				continue
			}
			kb.x, kb.y = 0, y
			y += kb.h
			extW = max(extW, kb.w)
			b.kids = append(b.kids, kb)
		}
		ext = y
	case "lr-tb", "rl-tb":
		b.flow = true
		x, y, rowH := 0.0, 0.0, 0.0
		var row []*lbox
		limit := contentW
		if limit <= 0 {
			limit = math.MaxFloat64
		}
		endRow := func() {
			if layout == "rl-tb" {
				for _, kb := range row {
					kb.x = limit - kb.x - kb.w
				}
			}
			row = nil
			y += rowH
			x, rowH = 0, 0
		}
		for _, k := range kids {
			kb := L.box(k, contentW, 0, 0)
			if kb == nil {
				continue
			}
			if x > 0 && x+kb.w > limit+0.01 {
				endRow()
			}
			kb.x, kb.y = x, y
			x += kb.w
			rowH = max(rowH, kb.h)
			extW = max(extW, x)
			row = append(row, kb)
			b.kids = append(b.kids, kb)
		}
		if len(row) > 0 {
			endRow()
		}
		ext = y
		if layout == "rl-tb" && contentW <= 0 {
			for _, kb := range b.kids {
				kb.x = extW - kb.x - kb.w // laid out against no width: mirror within the widest row
			}
		}
	case "table":
		b.flow = true
		cols := columnWidths(t.Attr("columnWidths"), contentW)
		y := 0.0
		for _, k := range kids {
			var kb *lbox
			if k.Kind == "subform" && k.T.Attr("layout") == "row" {
				kb = L.rowBox(k, cols, contentW)
			} else {
				kb = L.box(k, contentW, 0, 0)
			}
			if kb == nil {
				continue
			}
			kb.x, kb.y = 0, y
			y += kb.h
			extW = max(extW, kb.w)
			b.kids = append(b.kids, kb)
		}
		ext = y
	case "row":
		// a row outside a table: cells left to right
		x := 0.0
		for _, k := range kids {
			kb := L.box(k, contentW, 0, 0)
			if kb == nil {
				continue
			}
			kb.x, kb.y = x, 0
			x += kb.w
			ext = max(ext, kb.h)
			b.kids = append(b.kids, kb)
		}
		extW = x
	default: // position
		for _, k := range kids {
			kb := L.box(k, contentW, 0, 0)
			if kb == nil {
				continue
			}
			ke := extentOf(k.T)
			dx, dy := anchorOffset(ke.anchor, kb.w, kb.h)
			kb.x, kb.y = ke.x+dx, ke.y+dy
			extW = max(extW, kb.x+kb.w)
			ext = max(ext, kb.y+kb.h)
			b.kids = append(b.kids, kb)
		}
	}
	if w == 0 {
		w = e.growW(extW + m.l + m.r)
		if layout != "position" && availW > 0 && !e.wFix && e.maxW == 0 && in.Kind == "subform" && layout == "tb" {
			w = max(w, 0)
		}
	}
	h := e.h
	switch {
	case forceH > 0:
		h = forceH
	case !e.hFix:
		h = e.growH(ext + m.t + m.b)
	}
	b.w, b.h = w, h
	if in.Kind != "pageArea" {
		b.deco = t.Child("border")
	}
	if in.Kind == "exclGroup" {
		b.flow = false
	}
	return b
}

// columnWidths reads a table's columnWidths; -1 shares what's left.
func columnWidths(s string, total float64) []float64 {
	var cols []float64
	auto := 0
	used := 0.0
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "-1") {
			cols = append(cols, -1)
			auto++
			continue
		}
		v := measure(f)
		cols = append(cols, v)
		used += v
	}
	if auto > 0 {
		share := 0.0
		if total > used {
			share = (total - used) / float64(auto)
		}
		for i, c := range cols {
			if c < 0 {
				cols[i] = share
			}
		}
	}
	return cols
}

// rowBox lays out a table row: each cell gets its columns' width, and
// every cell takes the tallest cell's height.
func (L *layouter) rowBox(in *Inst, cols []float64, availW float64) *lbox {
	kids := visibleKids(in)
	m := marginOf(in.T)
	place := func(forceH float64) ([]*lbox, float64, float64) {
		var bs []*lbox
		x, h, ci := 0.0, 0.0, 0
		for _, k := range kids {
			span := atoiDef(k.T.Attr("colSpan"), 1)
			w := 0.0
			if span < 0 {
				for ; ci < len(cols); ci++ {
					w += cols[ci]
				}
			} else {
				for j := 0; j < span && ci < len(cols); j++ {
					w += cols[ci]
					ci++
				}
			}
			var kb *lbox
			if w > 0 {
				kb = L.box(k, w, w, forceH)
			} else {
				kb = L.box(k, availW, 0, forceH)
			}
			if kb == nil {
				continue
			}
			kb.x, kb.y = x, 0
			x += kb.w
			h = max(h, kb.h)
			bs = append(bs, kb)
		}
		return bs, x, h
	}
	bs, w, h := place(0)
	if len(bs) > 1 {
		bs, w, _ = place(h) // stretch every cell to the row's height
	}
	e := extentOf(in.T)
	b := &lbox{in: in, kids: bs, m: m, deco: in.T.Child("border")}
	b.w = w + m.l + m.r
	if e.wFix {
		b.w = e.w
	}
	b.h = e.growH(h + m.t + m.b)
	if e.hFix {
		b.h = e.h
	}
	for _, kb := range bs {
		kb.x += m.l
		kb.y += m.t
	}
	b.m = margins{} // the cells already sit inside the row's margins
	b.bb, b.ba = breaksOf(in.T)
	return b
}

func breaksOf(t *Node) (before, after *brk) {
	if bb := t.Child("breakBefore"); bb != nil && bb.Attr("targetType") != "auto" {
		before = &brk{kind: bb.Attr("targetType"), target: strings.TrimPrefix(bb.Attr("target"), "#"), startNew: bb.Attr("startNew") == "1"}
		if before.kind == "" {
			before.kind = "auto"
		}
	}
	if ba := t.Child("breakAfter"); ba != nil && ba.Attr("targetType") != "auto" {
		after = &brk{kind: ba.Attr("targetType"), target: strings.TrimPrefix(ba.Attr("target"), "#"), startNew: ba.Attr("startNew") == "1"}
		if after.kind == "" {
			after.kind = "auto"
		}
	}
	if old := t.Child("break"); old != nil {
		if v := old.Attr("before"); v != "" && v != "auto" && before == nil {
			before = &brk{kind: legacyKind(v), target: strings.TrimPrefix(old.Attr("beforeTarget"), "#"), startNew: old.Attr("startNew") == "1"}
		}
		if v := old.Attr("after"); v != "" && v != "auto" && after == nil {
			after = &brk{kind: legacyKind(v), target: strings.TrimPrefix(old.Attr("afterTarget"), "#"), startNew: old.Attr("startNew") == "1"}
		}
	}
	// targetType defaults to auto: no break
	if before != nil && before.kind == "auto" {
		before = nil
	}
	if after != nil && after.kind == "auto" {
		after = nil
	}
	return
}

func legacyKind(v string) string {
	switch v {
	case "contentArea":
		return "contentArea"
	}
	return "pageArea" // pageArea, pageEven, pageOdd
}

// ---- draws ----

func (L *layouter) drawBox(in *Inst, availW, forceW, forceH float64) *lbox {
	t := in.T
	e := extentOf(t)
	m := marginOf(t)
	fnt := fontOf(t.Child("font"), defaultFont)
	p := paraOf(t)
	b := &lbox{in: in, m: m}
	w, h := e.w, e.h
	if forceW > 0 {
		w = forceW
	}
	if forceH > 0 {
		h = forceH
	}
	v := t.Child("value")
	var vk *Node
	if v != nil {
		vk = v.FirstElem()
	}
	var tb textBlock
	isText := vk == nil || vk.Local == "text" || vk.Local == "exData" || vk.Local == "integer" || vk.Local == "decimal" || vk.Local == "date" || vk.Local == "float"
	if isText {
		ps := valueParagraphs(v, fnt, p, L.embed)
		if txt := L.drawText(in); txt != "" {
			ps = plainParagraphs(txt, fnt, p)
		}
		wrapW := 0.0
		if e.wFix || forceW > 0 {
			wrapW = w - m.l - m.r
		}
		tb = layoutText(ps, wrapW, wrapW > 0, &L.fonts)
		if !e.wFix && forceW == 0 {
			w = e.growW(tb.w + m.l + m.r)
			if wrapW == 0 && e.maxW > 0 && tb.w+m.l+m.r > e.maxW {
				tb = layoutText(ps, e.maxW-m.l-m.r, true, &L.fonts)
			}
		}
		if !e.hFix && forceH == 0 {
			h = e.growH(tb.h + m.t + m.b)
		}
	}
	b.w, b.h = w, h
	b.items = append(b.items, borderItems(t.Child("border"), 0, 0, w, h)...)
	cx, cy, cw, ch := m.l, m.t, w-m.l-m.r, h-m.t-m.b
	if isText {
		if len(tb.lines) > 0 {
			dy := valign(p.vAlign, ch, tb.h)
			if !e.wFix && forceW == 0 {
				// a growable draw aligns its lines within its own width
				for i := range tb.lines {
					tb.lines[i].X += alignShift(p.hAlign, cw, tb.w)
				}
			}
			b.items = append(b.items, Item{T: "text", X: round2(cx), Y: round2(cy + dy), W: round2(cw), H: round2(ch), Lines: tb.lines})
		}
		return b
	}
	switch vk.Local {
	case "line":
		edge := firstEdge(vk)
		if edge == nil {
			edge = &Edge{W: 0.5}
		}
		x1, y1, x2, y2 := cx, cy, cx+cw, cy+ch
		if vk.Attr("slope") == "/" {
			y1, y2 = cy+ch, cy
		}
		if ch < 0.01 || cw < 0.01 {
			// horizontal or vertical: centre the stroke on the box
			if ch < 0.01 {
				y1, y2 = cy, cy
			}
			if cw < 0.01 {
				x1, x2 = cx, cx
			}
		}
		b.items = append(b.items, Item{T: "line", X: round2(x1), Y: round2(y1), X2: round2(x2), Y2: round2(y2), Edges: []Edge{*edge}})
	case "rectangle":
		it := rectItem(vk, cx, cy, cw, ch)
		if it != nil {
			b.items = append(b.items, *it)
		}
	case "arc":
		edge := firstEdge(vk)
		if edge == nil {
			edge = &Edge{W: 0.5}
		}
		it := Item{T: "ellipse", X: round2(cx), Y: round2(cy), W: round2(cw), H: round2(ch), Edges: []Edge{*edge}}
		if fl := vk.Child("fill"); fl != nil && presence(fl) == "visible" {
			it.Fill = colorOf(fl, "#ffffff")
		}
		b.items = append(b.items, it)
	case "image":
		if src := imageSrc(vk); src != "" {
			b.items = append(b.items, Item{T: "image", X: round2(cx), Y: round2(cy), W: round2(cw), H: round2(ch), Src: src})
		}
	}
	return b
}

// drawText fills in a draw's text that a script would make: page numbers
// are handled after pagination, so nothing here yet.
func (L *layouter) drawText(in *Inst) string { return "" }

func valign(a string, avail, h float64) float64 {
	switch a {
	case "middle":
		return (avail - h) / 2
	case "bottom":
		return avail - h
	}
	return 0
}

func alignShift(a string, avail, w float64) float64 {
	switch a {
	case "center":
		return (avail - w) / 2
	case "right":
		return avail - w
	}
	return 0
}

// ---- borders ----

// edgeOf reads an <edge>: nil when it's hidden.
func edgeOf(e *Node) *Edge {
	if e == nil {
		return &Edge{W: 0.5}
	}
	if presence(e) != "visible" {
		return nil
	}
	ed := &Edge{W: attrMeasure(e, "thickness", 0.5), Color: colorOf(e, "")}
	switch e.Attr("stroke") {
	case "dashed", "dotted", "dashDot", "dashDotDot":
		ed.Dash = e.Attr("stroke")
	}
	if ed.W <= 0 {
		ed.W = 0.25
	}
	return ed
}

func firstEdge(n *Node) *Edge {
	es := n.Children("edge")
	if len(es) == 0 {
		return &Edge{W: 0.5}
	}
	return edgeOf(es[0])
}

// borderItems draws a <border> (or a rectangle's edges and fill) around
// x, y, w, h.
func borderItems(bd *Node, x, y, w, h float64) []Item {
	if bd == nil || presence(bd) != "visible" {
		return nil
	}
	it := rectItem(bd, x, y, w, h)
	if it == nil {
		return nil
	}
	return []Item{*it}
}

func rectItem(n *Node, x, y, w, h float64) *Item {
	es := n.Children("edge")
	var edges []*Edge
	switch len(es) {
	case 0:
		edges = []*Edge{{W: 0.5}}
	default:
		for i := 0; i < 4; i++ {
			j := min(i, len(es)-1)
			edges = append(edges, edgeOf(es[j]))
		}
	}
	it := &Item{T: "rect", X: round2(x), Y: round2(y), W: round2(w), H: round2(h)}
	if fl := n.Child("fill"); fl != nil && presence(fl) == "visible" {
		it.Fill = colorOf(fl, "#ffffff")
		if pat := fl.Child("pattern"); pat != nil {
			it.Fill = colorOf(pat, it.Fill)
		}
	}
	if c := n.Child("corner"); c != nil && c.Attr("join") == "round" {
		// The radius only shapes round joins; square is the default.
		it.Radius = attrMeasure(c, "radius", 0)
	}
	same := true
	for _, e := range edges[1:] {
		if (e == nil) != (edges[0] == nil) || (e != nil && *e != *edges[0]) {
			same = false
		}
	}
	switch {
	case same && edges[0] == nil:
		if it.Fill == "" {
			return nil
		}
	case same:
		it.Edges = []Edge{*edges[0]}
	default:
		for _, e := range edges {
			if e == nil {
				it.Edges = append(it.Edges, Edge{W: 0})
			} else {
				it.Edges = append(it.Edges, *e)
			}
		}
	}
	return it
}

// ---- fields ----

type captionInfo struct {
	place   string // left, right, top, bottom, inline
	reserve float64
	hasRes  bool
	ps      []paragraph
	font    Font
	p       para
	m       margins
}

func (L *layouter) captionOf(t *Node, fieldFont Font) *captionInfo {
	c := t.Child("caption")
	if c == nil || presence(c) == "hidden" || presence(c) == "inactive" {
		return nil
	}
	ci := &captionInfo{place: c.Attr("placement"), font: fontOf(c.Child("font"), fieldFont), p: paraOf(c), m: marginOf(c)}
	if ci.place == "" {
		ci.place = "left"
	}
	if c.HasAttr("reserve") {
		ci.reserve, ci.hasRes = measure(c.Attr("reserve")), true
		if ci.reserve < 0 {
			ci.hasRes = false
		}
	}
	ci.ps = valueParagraphs(c.Child("value"), ci.font, ci.p, L.embed)
	if presence(c) == "invisible" {
		ci.ps = nil
	}
	return ci
}

func (L *layouter) fieldBox(in *Inst, availW, forceW, forceH float64) *lbox {
	t := in.T
	e := extentOf(t)
	m := marginOf(t)
	fnt := fontOf(t.Child("font"), defaultFont)
	p := paraOf(t)
	ui, un := uiKind(t)
	if in.match == "member" {
		// a radio button takes its group's value
		in.raw = in.Parent.raw
	}
	b := &lbox{in: in, m: m}
	w, h := e.w, e.h
	if forceW > 0 {
		w = forceW
	}
	if forceH > 0 {
		h = forceH
	}
	cap := L.captionOf(t, fnt)
	um := marginOf(un) // the widget's own insets
	if un != nil && un.Child("margin") == nil && (ui == "textEdit" || ui == "dateTimeEdit" || ui == "numericEdit" || ui == "choiceList" || ui == "passwordEdit") {
		um = margins{}
	}
	disp := L.f.display(in)
	if ui == "passwordEdit" && disp != "" {
		disp = strings.Repeat("*", len([]rune(disp)))
	}
	multi := un != nil && un.Attr("multiLine") == "1"
	if ui == "textEdit" && un == nil {
		multi = false
	}
	comb := 0
	if c := un.Child("comb"); c != nil {
		comb = atoiDef(c.Attr("numberOfCells"), 0)
	}

	// caption size
	var capTB textBlock
	capW, capH := 0.0, 0.0
	if cap != nil && len(cap.ps) > 0 {
		capTB = layoutText(cap.ps, 0, false, &L.fonts)
		capW, capH = capTB.w+cap.m.l+cap.m.r, capTB.h+cap.m.t+cap.m.b
	}
	reserveFor := func(horizontal bool) float64 {
		if cap == nil {
			return 0
		}
		if cap.hasRes {
			return cap.reserve
		}
		if horizontal {
			return capW
		}
		return capH
	}

	// value text, for its size when the field grows
	valueText := func(width float64) textBlock {
		if disp == "" || ui == "checkButton" || ui == "signature" || ui == "imageEdit" {
			return textBlock{}
		}
		ps := plainParagraphs(disp, fnt, p)
		if t.Child("value") != nil && t.Child("value").Child("exData") != nil && in.dnode == nil {
			ps = valueParagraphs(t.Child("value"), fnt, p, L.embed)
		}
		return layoutText(ps, width, multi && width > 0, &L.fonts)
	}
	checkSize := 10.0
	if ui == "checkButton" && un != nil && un.HasAttr("size") {
		checkSize = measure(un.Attr("size"))
	}

	horizontal := cap != nil && (cap.place == "left" || cap.place == "right")
	vertical := cap != nil && (cap.place == "top" || cap.place == "bottom")
	if !e.wFix && forceW == 0 {
		content := 0.0
		switch ui {
		case "checkButton":
			content = checkSize
		default:
			content = valueText(0).w + um.l + um.r
		}
		cw := content
		if horizontal {
			cw += reserveFor(true)
		} else if vertical {
			cw = max(cw, capW)
		}
		w = e.growW(cw + m.l + m.r)
	}
	// widget region
	wx, wy, ww, wh := m.l, m.t, w-m.l-m.r, 0.0
	if horizontal {
		r := reserveFor(true)
		ww -= r
		if cap.place == "left" {
			wx += r
		}
	}
	if !e.hFix && forceH == 0 {
		content := 0.0
		switch ui {
		case "checkButton":
			content = checkSize
		default:
			vt := valueText(ww - um.l - um.r)
			content = vt.h + um.t + um.b
			if vt.h == 0 {
				content = faceFor(fnt).lineHeight() + um.t + um.b
			}
		}
		ch := content
		if vertical {
			ch += reserveFor(false)
		} else if horizontal {
			ch = max(ch, capH)
		}
		h = e.growH(ch + m.t + m.b)
	}
	wh = h - m.t - m.b
	if vertical {
		r := reserveFor(false)
		wh -= r
		if cap.place == "top" {
			wy += r
		}
	}
	b.w, b.h = w, h

	// field border and caption
	b.items = append(b.items, borderItems(t.Child("border"), 0, 0, w, h)...)
	if cap != nil && len(capTB.lines) > 0 {
		cx, cy, cw, ch := m.l, m.t, w-m.l-m.r, h-m.t-m.b
		switch cap.place {
		case "left":
			cw = reserveFor(true)
		case "right":
			cx, cw = wx+ww, reserveFor(true)
		case "top":
			ch = reserveFor(false)
		case "bottom":
			cy, ch = wy+wh, reserveFor(false)
		}
		cx, cy, cw, ch = cx+cap.m.l, cy+cap.m.t, cw-cap.m.l-cap.m.r, ch-cap.m.t-cap.m.b
		if ui == "button" {
			cx, cy, cw, ch = wx+cap.m.l, wy+cap.m.t, ww-cap.m.l-cap.m.r, wh-cap.m.t-cap.m.b
		}
		capTB = layoutText(cap.ps, cw, cw > 0, &L.fonts)
		lines := capTB.lines
		dy := valign(cap.p.vAlign, ch, capTB.h)
		b.items = append(b.items, Item{T: "text", X: round2(cx), Y: round2(cy + dy), W: round2(cw), H: round2(ch), Lines: lines, NoPrint: ui == "button"})
	}

	// widget
	if un != nil && ui != "checkButton" {
		b.items = append(b.items, borderItems(un.Child("border"), wx, wy, ww, wh)...)
	}
	ix, iy, iw, ih := wx+um.l, wy+um.t, ww-um.l-um.r, wh-um.t-um.b
	fb := &FieldBox{SOM: in.SOM, Value: in.raw, Display: disp, Font: L.fonts.id(fnt), HAlign: p.hAlign, VAlign: p.vAlign}
	if a := t.Child("assist"); a != nil {
		fb.Tip = strings.TrimSpace(a.Child("toolTip").TextContent())
	}
	if v := t.Child("validate"); v != nil && v.Attr("nullTest") == "error" {
		fb.Required = true
	}
	switch t.Attr("access") {
	case "readOnly", "protected", "nonInteractive":
		fb.ReadOnly = true
	}
	if pageNumberScript(t) != "" {
		fb.Page = pageNumberScript(t)
		fb.ReadOnly = true
	}
	var lines []TextLine
	switch ui {
	case "checkButton":
		fb.Kind = "check"
		on, _, _ := checkStates(t)
		fb.On = on
		if in.match == "member" {
			fb.Kind = "radio"
			fb.Group = in.Parent.SOM
		}
		fb.Shape = un.Attr("shape")
		if fb.Shape == "" {
			fb.Shape = "square"
		}
		fb.Mark = un.Attr("mark")
		if fb.Mark == "" || fb.Mark == "default" {
			fb.Mark = "check"
			if fb.Shape == "round" {
				fb.Mark = "circle"
			}
		}
		// the box sits in the widget per the field's alignment
		sz := min(checkSize, max(iw, 0), max(ih, 0))
		if sz <= 0 {
			sz = checkSize
		}
		bx := ix + alignShift(p.hAlign, iw, sz)
		by := iy + valign(p.vAlign, ih, sz)
		if p.vAlign == "top" && ih > sz {
			by = iy + valign("middle", ih, sz) // Acrobat centres boxes unless told otherwise
		}
		ix, iy, iw, ih = bx, by, sz, sz
		if un.Child("border") == nil {
			b.items = append(b.items, Item{T: shapeItem(fb.Shape), X: round2(bx), Y: round2(by), W: round2(sz), H: round2(sz), Edges: []Edge{{W: 0.5}}, Fill: "#ffffff"})
		} else {
			// a check box's own border goes round the box, not the widget
			b.items = append(b.items, borderItems(un.Child("border"), bx, by, sz, sz)...)
		}
	case "choiceList":
		fb.Kind = "choice"
		fb.Options, fb.Saves = choiceItems(t)
		if un.Attr("open") == "always" || un.Attr("open") == "multiSelect" {
			fb.Kind = "list"
		}
	case "dateTimeEdit":
		fb.Kind = "date"
	case "numericEdit":
		fb.Kind = "number"
	case "passwordEdit":
		fb.Kind = "password"
	case "signature":
		fb.Kind = "signature"
	case "imageEdit":
		fb.Kind = "image"
	case "button":
		fb.Kind = "button"
		fb.Display = strings.TrimSpace(captionText(t))
		fb.ReadOnly = true
	case "barcode":
		fb.Kind = "barcode"
		fb.ReadOnly = true
	default:
		fb.Kind = "text"
		if multi {
			fb.Kind = "multiline"
		}
	}
	fb.Comb = comb
	if v := t.Child("value"); v != nil {
		if tx := v.Child("text"); tx != nil {
			fb.MaxChars = atoiDef(tx.Attr("maxChars"), 0)
		}
	}
	// the printed value
	if disp != "" && ui != "checkButton" && ui != "signature" && ui != "imageEdit" && ui != "button" {
		if comb > 0 && iw > 0 {
			lines = combLines(disp, fnt, iw, ih, comb, &L.fonts)
		} else {
			vt := valueText(iw)
			lines = vt.lines
			dy := valign(p.vAlign, ih, vt.h)
			for i := range lines {
				lines[i].Y = round2(lines[i].Y + dy)
			}
		}
	}
	it := Item{T: "field", X: round2(ix), Y: round2(iy), W: round2(iw), H: round2(ih), Field: fb, Lines: lines, Clip: true}
	if fb.Kind == "button" {
		it.X, it.Y, it.W, it.H = round2(wx), round2(wy), round2(ww), round2(wh)
	}
	b.items = append(b.items, it)
	return b
}

func shapeItem(shape string) string {
	if shape == "round" {
		return "ellipse"
	}
	return "rect"
}

// combLines centres one character in each of n cells.
func combLines(s string, f Font, w, h float64, n int, fonts *fontTable) []TextLine {
	fc := faceFor(f)
	cell := w / float64(n)
	line := TextLine{Y: round2((h-fc.lineHeight())/2 + fc.ascent()), W: w}
	id := fonts.id(f)
	for i, r := range []rune(s) {
		if i >= n {
			break
		}
		rw := fc.runeWidth(r)
		line.Runs = append(line.Runs, TextRun{X: round2(float64(i)*cell + (cell-rw)/2), Text: string(r), Font: id, W: round2(rw)})
	}
	return []TextLine{line}
}

// pageNumberScript says whether a field's script fills it with the page
// number ("page") or the page count ("pages").
func pageNumberScript(t *Node) string {
	var src strings.Builder
	for _, k := range t.Children("event") {
		src.WriteString(k.TextContent())
	}
	if c := t.Child("calculate"); c != nil {
		src.WriteString(c.TextContent())
	}
	s := src.String()
	switch {
	case strings.Contains(s, "pageCount()"), strings.Contains(s, "absPageCount("), strings.Contains(s, "PageCount("):
		return "pages"
	case strings.Contains(s, "layout.page("), strings.Contains(s, "absPage("), strings.Contains(s, "Page("):
		return "page"
	}
	return ""
}

func imageSrc(img *Node) string {
	ct := img.Attr("contentType")
	data := strings.Join(strings.Fields(img.TextContent()), "")
	if data == "" || img.Attr("transferEncoding") == "none" {
		return ""
	}
	if ct == "" {
		ct = "image/png"
		if strings.HasPrefix(data, "/9j/") {
			ct = "image/jpeg"
		}
	}
	return "data:" + ct + ";base64," + data
}

// ---- pagination ----

type pageAreaInfo struct {
	in    *Inst
	w, h  float64
	cas   []contentArea
	used  int // pages made from it
	maxN  int // -1 unlimited
	boxes []*lbox
}

type contentArea struct {
	x, y, w, h float64
	id, name   string
}

func (L *layouter) pageAreas() []*pageAreaInfo {
	var out []*pageAreaInfo
	root := L.f.root
	for _, k := range root.Kids {
		if k.Kind != "pageSet" {
			continue
		}
		for _, pa := range k.Kids {
			if pa.Kind != "pageArea" {
				continue
			}
			info := &pageAreaInfo{in: pa, w: 612, h: 792, maxN: -1}
			if md := pa.T.Child("medium"); md != nil {
				s, l := attrMeasure(md, "short", 612), attrMeasure(md, "long", 792)
				if md.Attr("orientation") == "landscape" {
					info.w, info.h = l, s
				} else {
					info.w, info.h = s, l
				}
			}
			if o := pa.T.Child("occur"); o != nil && o.HasAttr("max") {
				info.maxN = atoiDef(o.Attr("max"), -1)
			}
			for _, ca := range pa.T.Children("contentArea") {
				info.cas = append(info.cas, contentArea{
					x: attrMeasure(ca, "x", 0), y: attrMeasure(ca, "y", 0),
					w: attrMeasure(ca, "w", info.w), h: attrMeasure(ca, "h", info.h),
					id: ca.Attr("id"), name: ca.Attr("name"),
				})
			}
			if len(info.cas) == 0 {
				info.cas = []contentArea{{w: info.w, h: info.h}}
			}
			// master page content: positioned in the page
			for _, mk := range visibleKids(pa) {
				kb := L.box(mk, info.w, 0, 0)
				if kb == nil {
					continue
				}
				ke := extentOf(mk.T)
				dx, dy := anchorOffset(ke.anchor, kb.w, kb.h)
				kb.x, kb.y = ke.x+dx, ke.y+dy
				info.boxes = append(info.boxes, kb)
			}
			out = append(out, info)
		}
	}
	if len(out) == 0 {
		out = []*pageAreaInfo{{w: 612, h: 792, maxN: -1, cas: []contentArea{{w: 612, h: 792}}}}
	}
	return out
}

type pager struct {
	L      *layouter
	doc    *Doc
	areas  []*pageAreaInfo
	cur    *pageAreaInfo
	ca     int
	used   float64
	placed bool // something is on the current page
	page   *Page
}

func (pg *pager) findArea(target string) *pageAreaInfo {
	for _, a := range pg.areas {
		if a.in != nil && (a.in.T.Attr("id") == target || a.in.Name == target) {
			return a
		}
	}
	return nil
}

// nextArea is the page area for a new page: the target, or the current one
// while it may repeat, or the next in order.
func (pg *pager) nextArea(target string) *pageAreaInfo {
	if target != "" {
		if a := pg.findArea(target); a != nil {
			return a
		}
	}
	if pg.cur == nil {
		return pg.areas[0]
	}
	if pg.cur.maxN < 0 || pg.cur.used < pg.cur.maxN {
		return pg.cur
	}
	for i, a := range pg.areas {
		if a == pg.cur {
			for _, b := range pg.areas[i+1:] {
				if b.maxN < 0 || b.used < b.maxN {
					return b
				}
			}
		}
	}
	return pg.cur
}

func (pg *pager) newPage(target string) {
	if len(pg.doc.Pages) >= maxPages {
		panic(errTooBig)
	}
	a := pg.nextArea(target)
	a.used++
	pg.cur, pg.ca, pg.used, pg.placed = a, 0, 0, false
	pg.doc.Pages = append(pg.doc.Pages, Page{W: round2(a.w), H: round2(a.h)})
	pg.page = &pg.doc.Pages[len(pg.doc.Pages)-1]
	for _, mb := range a.boxes {
		pg.emit(mb, mb.x, mb.y)
	}
}

// nextContentArea moves to the next content area, or a new page.
func (pg *pager) nextContentArea(target string) {
	if target == "" && pg.ca+1 < len(pg.cur.cas) {
		pg.ca++
		pg.used = 0
		return
	}
	if target != "" {
		for i, c := range pg.cur.cas {
			if i > pg.ca && (c.id == target || c.name == target) {
				pg.ca, pg.used = i, 0
				return
			}
		}
	}
	pg.newPage("")
}

func (pg *pager) doBreak(b *brk) {
	if b == nil {
		return
	}
	switch b.kind {
	case "contentArea":
		if pg.used > 0 {
			pg.nextContentArea(b.target)
		}
	default:
		if !pg.placed && pg.used == 0 {
			// nothing on this page yet: just use the target page area for it
			if a := pg.findArea(b.target); a != nil && a != pg.cur {
				pg.cur.used--
				pg.doc.Pages = pg.doc.Pages[:len(pg.doc.Pages)-1]
				pg.cur = nil
				pg.newPage(b.target)
			}
			return
		}
		if !b.startNew && b.target != "" && pg.cur.in != nil && (pg.cur.in.T.Attr("id") == b.target || pg.cur.in.Name == b.target) && !pg.placed {
			return
		}
		pg.newPage(b.target)
	}
}

func (L *layouter) paginate(doc *Doc) {
	pg := &pager{L: L, doc: doc, areas: L.pageAreas()}
	root := L.f.root
	first := pg.areas[0]
	// the first page area is the one the first content breaks to, if it does
	kids := visibleKids(root)
	start := ""
	if len(kids) > 0 {
		if bb, _ := breaksOf(kids[0].T); bb != nil && bb.kind == "pageArea" {
			start = bb.target
		}
	}
	_ = first
	pg.newPage(start)
	layout := root.T.Attr("layout")
	ca := pg.cur.cas[pg.ca]
	for _, k := range kids {
		kb := L.box(k, ca.w, 0, 0)
		if kb == nil {
			continue
		}
		if layout == "" || layout == "position" {
			ke := extentOf(k.T)
			dx, dy := anchorOffset(ke.anchor, kb.w, kb.h)
			if kb.bb != nil && pg.placed {
				pg.doBreak(kb.bb)
			}
			c := pg.cur.cas[pg.ca]
			pg.emit(kb, c.x+ke.x+dx, c.y+ke.y+dy)
			pg.placed = true
			if kb.ba != nil {
				pg.doBreak(kb.ba)
			}
			continue
		}
		pg.flowPlace(kb)
	}
	// page numbers
	n := len(doc.Pages)
	for pi := range doc.Pages {
		L.fillPageNumbers(doc.Pages[pi].Items, pi+1, n)
	}
}

// fillPageNumbers writes page numbers and counts into the fields whose
// scripts would.
func (L *layouter) fillPageNumbers(items []Item, page, pages int) {
	for i := range items {
		it := &items[i]
		if it.T == "group" {
			// a fresh slice: master-page items are shared between pages
			it.Items = append([]Item(nil), it.Items...)
			L.fillPageNumbers(it.Items, page, pages)
		}
		if it.T == "text" {
			it.Lines = L.fillPlaceholders(it.Lines, it.W, page, pages)
		}
		if it.Field == nil || it.Field.Page == "" {
			continue
		}
		v := itoa(page)
		if it.Field.Page == "pages" {
			v = itoa(pages)
		}
		f := *it.Field
		f.Value, f.Display = v, v
		it.Field = &f
		fc := faceFor(L.fonts.list[f.Font])
		w := fc.width(v)
		y := valign(f.VAlign, it.H, fc.lineHeight()) + fc.ascent()
		it.Lines = []TextLine{{X: round2(alignShift(f.HAlign, it.W, w)), Y: round2(y), W: round2(w), Runs: []TextRun{{Text: v, Font: f.Font, W: round2(w)}}}}
	}
}

// fillPlaceholders puts the page number and count into lines of text
// that embed them, measuring the runs again. Lines are copied, never
// changed in place.
func (L *layouter) fillPlaceholders(lines []TextLine, boxW float64, page, pages int) []TextLine {
	has := false
	for _, l := range lines {
		for _, r := range l.Runs {
			if strings.ContainsAny(r.Text, pagePlaceholder+pagesPlaceholder) {
				has = true
			}
		}
	}
	if !has {
		return lines
	}
	rep := strings.NewReplacer(pagePlaceholder, itoa(page), pagesPlaceholder, itoa(pages))
	out := make([]TextLine, len(lines))
	for i, l := range lines {
		nl := l
		nl.Runs = make([]TextRun, len(l.Runs))
		shift := 0.0
		for j, r := range l.Runs {
			r.X = round2(r.X + shift)
			if t := rep.Replace(r.Text); t != r.Text {
				w := faceFor(L.fonts.list[r.Font]).width(t)
				shift += w - r.W
				r.Text, r.W = t, round2(w)
			}
			nl.Runs[j] = r
		}
		nl.W = round2(l.W + shift)
		out[i] = nl
	}
	return out
}

func itoa(n int) string {
	return strings.TrimSpace(ftoa(float64(n)))
}

// flowPlace places a box in the flow of content areas, splitting flowed
// boxes where they don't fit.
func (pg *pager) flowPlace(b *lbox) {
	if b.bb != nil {
		pg.doBreak(b.bb)
	}
	for guard := 0; b != nil && guard < 10000; guard++ {
		c := pg.cur.cas[pg.ca]
		avail := c.h - pg.used
		if b.h <= avail+0.5 {
			pg.emit(b, c.x, c.y+pg.used)
			pg.used += b.h
			pg.placed = true
			break
		}
		if b.flow && len(b.kids) > 0 {
			head, tail := splitBox(b, avail)
			if head != nil {
				pg.emit(head, c.x, c.y+pg.used)
				pg.placed = true
				var nb *brk
				if tail != nil {
					nb = tail.bb
					tail.bb = nil
				}
				if nb != nil {
					pg.doBreak(nb)
				} else {
					pg.nextContentArea("")
				}
				b = tail
				continue
			}
		}
		if pg.used > 0 {
			pg.nextContentArea("")
			continue
		}
		// too big for an empty content area: place it anyway
		pg.emit(b, c.x, c.y+pg.used)
		pg.used += b.h
		pg.placed = true
		break
	}
	if b != nil && b.ba != nil {
		pg.doBreak(b.ba)
	}
}

// splitBox cuts a flowed box into the part that fits in avail and the rest.
// The rest carries any break that caused the cut.
func splitBox(b *lbox, avail float64) (head, tail *lbox) {
	kids := append([]*lbox(nil), b.kids...)
	sort.SliceStable(kids, func(i, j int) bool { return kids[i].y < kids[j].y })
	limit := avail - b.m.t - b.m.b
	var hk, tk []*lbox
	var cut *brk
	cutAt := -1
	for i, k := range kids {
		if i > 0 && k.bb != nil && k.bb.kind != "" {
			cutAt, cut = i, k.bb
			break
		}
		bottom := k.y + k.h
		if bottom <= limit+0.5 {
			hk = append(hk, k)
			if k.ba != nil && i < len(kids)-1 {
				cutAt, cut = i+1, k.ba
				break
			}
			continue
		}
		if k.flow && len(k.kids) > 0 && limit-k.y > 10 {
			kh, kt := splitBox(k, limit-k.y)
			if kh != nil {
				hk = append(hk, kh)
				rest := []*lbox{}
				if kt != nil {
					rest = append(rest, kt)
					cut = kt.bb
					kt.bb = nil
				}
				tk = append(rest, kids[i+1:]...)
				cutAt = -2
				break
			}
		}
		cutAt = i
		break
	}
	if cutAt >= 0 {
		tk = kids[cutAt:]
	} else if cutAt == -1 {
		tk = nil
	}
	if len(hk) == 0 {
		return nil, b
	}
	head = &lbox{in: b.in, x: b.x, y: b.y, w: b.w, kids: hk, flow: true, m: b.m, deco: b.deco, bb: b.bb, rot: b.rot, nop: b.nop}
	bottom := 0.0
	for _, k := range hk {
		bottom = max(bottom, k.y+k.h)
	}
	head.h = bottom + b.m.t + b.m.b
	head.items = b.items
	if len(tk) == 0 {
		return head, nil
	}
	shift := tk[0].y
	var moved []*lbox
	tb := 0.0
	for _, k := range tk {
		nk := *k
		nk.y -= shift
		moved = append(moved, &nk)
		tb = max(tb, nk.y+nk.h)
	}
	tail = &lbox{in: b.in, x: b.x, w: b.w, kids: moved, flow: true, m: b.m, deco: b.deco, ba: b.ba, bb: cut, rot: b.rot, nop: b.nop}
	tail.h = tb + b.m.t + b.m.b
	head.ba = nil
	return head, tail
}

// emit draws a box and its children on the current page with its top left
// at (ox, oy).
func (pg *pager) emit(b *lbox, ox, oy float64) {
	items := pg.collect(b)
	if len(items) == 0 {
		return
	}
	if b.rot != 0 {
		pg.page.Items = append(pg.page.Items, Item{T: "group", X: round2(ox), Y: round2(oy), Rot: b.rot, Items: items, NoPrint: b.nop})
		return
	}
	for _, it := range items {
		pg.page.Items = append(pg.page.Items, offset(it, ox, oy))
	}
}

// collect is a box's items relative to its own top left, children
// included.
func (pg *pager) collect(b *lbox) []Item {
	var out []Item
	if b.deco != nil {
		for _, it := range borderItems(b.deco, 0, 0, b.w, b.h) {
			it.NoPrint = b.nop
			out = append(out, it)
		}
	}
	out = append(out, b.items...)
	for _, k := range b.kids {
		kx, ky := b.m.l+k.x, b.m.t+k.y
		sub := pg.collect(k)
		if k.rot != 0 {
			if len(sub) > 0 {
				out = append(out, Item{T: "group", X: round2(kx), Y: round2(ky), Rot: k.rot, Items: sub, NoPrint: k.nop})
			}
			continue
		}
		for _, it := range sub {
			out = append(out, offset(it, kx, ky))
		}
	}
	return out
}

func offset(it Item, dx, dy float64) Item {
	it.X = round2(it.X + dx)
	it.Y = round2(it.Y + dy)
	if it.T == "line" {
		it.X2 = round2(it.X2 + dx)
		it.Y2 = round2(it.Y2 + dy)
	}
	return it
}
