package xfa

import (
	"errors"
	"strconv"
	"strings"
)

// Merging the template with the data (XFA's data binding): every container
// becomes one or more instances, and fields find their values.
//
//   - A named subform with bind match="once" (the default) opens the data
//     group of its name. A repeating subform gets one instance per data
//     group of its name, at least occur min and at most occur max; with no
//     data it gets occur initial.
//   - Unnamed subforms, areas, subform sets and match="none" subforms open
//     no group: their children bind in the parent's.
//   - A field with match="once" takes the next unclaimed data value of its
//     name in its group; "global" shares the first value of its name in
//     the record with every global field of that name; "dataRef" goes
//     where its ref points; "none" keeps its template value.
//
// Data nodes that don't exist yet are made when a value is written.

// Inst is one instance of a template container in the merged form.
type Inst struct {
	T        *Node  // template element
	Kind     string // subform, subformSet, field, draw, exclGroup, area, pageArea
	Name     string
	SOM      string
	Parent   *Inst
	Kids     []*Inst
	Presence string

	// data binding
	group *Node // data group this subform opened (or its parent's)
	dnode *Node // a field's data value; nil when unbound or not made yet
	dpath *dataPath
	match string

	raw string // a field's or exclusion group's stored value

	// layout, filled per layout pass
	ui     string
	uiNode *Node
}

// dataPath says where a not-yet-made data node goes: under the instance's
// group (made if needed), the index-th child named name.
type dataPath struct {
	in         *Inst // the subform instance whose group holds it
	name       string
	index      int
	steps      []step // for dataRef: from the record or the group
	fromRecord bool
}

type step struct {
	name  string
	index int
}

type binder struct {
	f       *Form
	claimed map[*Node]bool
	global  map[string]*Node
	somIdx  map[*Inst]map[string]int
	count   int
}

// maxInsts bounds the merged form: a template or data asking for more
// instances than any real form has is refused rather than laid out.
const maxInsts = 100000

var errTooBig = errors.New("xfa: the form asks for more objects than this package lays out")

func (f *Form) merge() (err error) {
	defer func() {
		if p := recover(); p != nil {
			if p != errTooBig {
				panic(p)
			}
			err = errTooBig
		}
	}()
	rs := f.template.Child("subform")
	if rs == nil {
		return errNoRoot
	}
	b := &binder{f: f, claimed: map[*Node]bool{}, global: map[string]*Node{}, somIdx: map[*Inst]map[string]int{}}
	f.bySOM = map[string]*Inst{}
	f.order = nil
	root := &Inst{T: rs, Kind: "subform", Name: rs.Attr("name"), Presence: presence(rs), group: f.dataRoot}
	root.SOM = somName(root.Name, "subform") + "[0]"
	f.root = root
	b.children(root)
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

const errNoRoot = errString("xfa: the template has no root subform")

func somName(name, kind string) string {
	if name != "" {
		return name
	}
	return "#" + kind
}

func (b *binder) add(parent, in *Inst) {
	if b.count++; b.count > maxInsts {
		panic(errTooBig)
	}
	m := b.somIdx[parent]
	if m == nil {
		m = map[string]int{}
		b.somIdx[parent] = m
	}
	n := somName(in.Name, in.Kind)
	in.SOM = parent.SOM + "." + n + "[" + strconv.Itoa(m[n]) + "]"
	m[n]++
	in.Parent = parent
	parent.Kids = append(parent.Kids, in)
	switch in.Kind {
	case "field", "exclGroup":
		b.f.bySOM[in.SOM] = in
		b.f.order = append(b.f.order, in)
	case "subform", "subformSet", "area", "pageArea":
		b.f.bySOM[in.SOM] = in
	}
}

// children makes the instances of a subform's (or area's, page area's)
// containers, in template order.
func (b *binder) children(p *Inst) {
	for _, k := range p.T.Elems() {
		switch k.Local {
		case "pageSet":
			ps := &Inst{T: k, Kind: "pageSet", Name: k.Attr("name"), Presence: "visible", group: p.group}
			b.add(p, ps)
			for _, pa := range k.Elems() {
				switch pa.Local {
				case "pageArea":
					in := &Inst{T: pa, Kind: "pageArea", Name: pa.Attr("name"), Presence: "visible", group: p.group}
					b.add(ps, in)
					b.children(in)
				case "pageSet":
					// nested page sets: flatten their page areas into this one
					for _, pa2 := range pa.Children("pageArea") {
						in := &Inst{T: pa2, Kind: "pageArea", Name: pa2.Attr("name"), Presence: "visible", group: p.group}
						b.add(ps, in)
						b.children(in)
					}
				}
			}
		case "subform":
			b.subform(p, k)
		case "subformSet":
			in := &Inst{T: k, Kind: "subformSet", Name: k.Attr("name"), Presence: presence(k), group: p.group}
			b.add(p, in)
			b.children(in)
		case "area":
			in := &Inst{T: k, Kind: "area", Name: k.Attr("name"), Presence: presence(k), group: p.group}
			b.add(p, in)
			b.children(in)
		case "field", "exclGroup":
			in := &Inst{T: k, Kind: k.Local, Name: k.Attr("name"), Presence: presence(k), group: p.group}
			b.add(p, in)
			b.bindValue(in)
			if k.Local == "exclGroup" {
				for _, m := range k.Children("field") {
					mi := &Inst{T: m, Kind: "field", Name: m.Attr("name"), Presence: presence(m), group: p.group}
					b.add(in, mi)
					mi.match = "member"
				}
			}
		case "draw":
			in := &Inst{T: k, Kind: "draw", Name: k.Attr("name"), Presence: presence(k), group: p.group}
			b.add(p, in)
		}
	}
}

// subform makes a template subform's instances.
func (b *binder) subform(p *Inst, t *Node) {
	name := t.Attr("name")
	match := "once"
	if bind := t.Child("bind"); bind != nil && bind.Attr("match") != "" {
		match = bind.Attr("match")
	}
	if name == "" {
		match = "none"
	}
	lo, hi, initial := occurOf(t)
	var groups []*Node
	switch match {
	case "once", "global":
		for _, d := range p.group.Elems() {
			if d.Local == name && !b.claimed[d] && isGroup(d) {
				groups = append(groups, d)
				if hi == 1 {
					break
				}
			}
		}
	case "dataRef":
		groups = b.resolveRef(p, t.Child("bind").Attr("ref"))
	}
	n := initial
	if len(groups) > 0 {
		n = max(lo, len(groups))
		if hi >= 0 {
			n = min(n, hi)
		}
	}
	for i := 0; i < n; i++ {
		in := &Inst{T: t, Kind: "subform", Name: name, Presence: presence(t), group: p.group, match: match}
		if match != "none" {
			if i < len(groups) {
				in.group = groups[i]
				b.claimed[groups[i]] = true
			} else {
				in.group = nil // made when a value is written
				in.dpath = &dataPath{in: p, name: name, index: i}
			}
		}
		b.add(p, in)
		b.children(in)
	}
}

// isGroup says whether a data node is a data group: it has element
// children, or says so with xfa:dataNode, or is empty (a group with no
// values yet).
func isGroup(d *Node) bool {
	switch d.Attr("dataNode") {
	case "dataGroup":
		return true
	case "dataValue":
		return false
	}
	if len(d.Elems()) > 0 {
		return true
	}
	return strings.TrimSpace(d.TextContent()) == ""
}

// bindValue finds a field's or exclusion group's data value.
func (b *binder) bindValue(in *Inst) {
	t := in.T
	match := "once"
	if bind := t.Child("bind"); bind != nil && bind.Attr("match") != "" {
		match = bind.Attr("match")
	}
	if in.Name == "" && match != "dataRef" {
		match = "none"
	}
	if in.Kind == "field" {
		if ui, _ := uiKind(t); ui == "button" || ui == "signature" {
			if match == "once" {
				match = "none"
			}
		}
	}
	in.match = match
	switch match {
	case "none":
	case "global":
		if d := b.global[in.Name]; d != nil {
			in.dnode = d
		} else if d := findDesc(b.f.dataRoot, in.Name); d != nil {
			in.dnode = d
			b.global[in.Name] = d
		} else {
			in.dpath = &dataPath{in: rootOf(in), name: in.Name}
		}
	case "dataRef":
		ref := t.Child("bind").Attr("ref")
		if ns := b.resolveRef(in.Parent, ref); len(ns) > 0 {
			in.dnode = ns[0]
		} else {
			in.dpath = b.refPath(in.Parent, ref)
		}
	default:
		g := groupInst(in.Parent)
		if g != nil && g.group != nil {
			i := 0
			for _, d := range g.group.Elems() {
				if d.Local != in.Name {
					continue
				}
				if !b.claimed[d] {
					in.dnode = d
					b.claimed[d] = true
					break
				}
				i++
			}
			if in.dnode == nil {
				in.dpath = &dataPath{in: g, name: in.Name, index: i}
			}
		} else {
			in.dpath = &dataPath{in: g, name: in.Name}
		}
	}
	in.raw = b.f.initialValue(in)
}

// groupInst is the nearest instance that opened a data group (or will).
func groupInst(in *Inst) *Inst {
	for p := in; p != nil; p = p.Parent {
		if p.Kind == "subform" && (p.match == "once" || p.match == "global" || p.match == "dataRef" || p.Parent == nil) {
			return p
		}
	}
	return nil
}

func rootOf(in *Inst) *Inst {
	for in.Parent != nil {
		in = in.Parent
	}
	return in
}

func findDesc(n *Node, name string) *Node {
	for _, k := range n.Elems() {
		if k.Local == name {
			return k
		}
		if d := findDesc(k, name); d != nil {
			return d
		}
	}
	return nil
}

// resolveRef follows a bind ref ("$.a.b", "$record.a[2]", "$data.x") from
// the data group of the given instance.
func (b *binder) resolveRef(from *Inst, ref string) []*Node {
	dp := b.refPath(from, ref)
	if dp == nil {
		return nil
	}
	var cur []*Node
	if dp.fromRecord {
		cur = []*Node{b.f.dataRoot}
	} else {
		g := groupInst(from)
		if g == nil || g.group == nil {
			return nil
		}
		cur = []*Node{g.group}
	}
	for _, s := range dp.steps {
		var next []*Node
		for _, c := range cur {
			i := 0
			for _, k := range c.Elems() {
				if k.Local != s.name {
					continue
				}
				if s.index < 0 || s.index == i {
					next = append(next, k)
				}
				i++
			}
		}
		cur = next
		if len(cur) == 0 {
			return nil
		}
	}
	return cur
}

func (b *binder) refPath(from *Inst, ref string) *dataPath {
	ref = strings.TrimSpace(ref)
	dp := &dataPath{in: groupInst(from)}
	switch {
	case strings.HasPrefix(ref, "$record."), strings.HasPrefix(ref, "$data."), strings.HasPrefix(ref, "!"):
		dp.fromRecord = true
		ref = ref[strings.IndexByte(ref, '.')+1:]
		if strings.HasPrefix(ref, b.f.dataRoot.Local+".") || strings.HasPrefix(ref, b.f.dataRoot.Local+"[") {
			ref = ref[strings.IndexByte(ref, '.')+1:]
		}
	case ref == "$":
		return dp
	case strings.HasPrefix(ref, "$."):
		ref = ref[2:]
	case strings.HasPrefix(ref, "$"):
		return nil
	}
	for _, seg := range strings.Split(ref, ".") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		s := step{name: seg}
		if i := strings.IndexByte(seg, '['); i >= 0 && strings.HasSuffix(seg, "]") {
			s.name = seg[:i]
			idx := seg[i+1 : len(seg)-1]
			if idx == "*" {
				s.index = -1
			} else {
				s.index = atoiDef(idx, 0)
			}
		}
		dp.steps = append(dp.steps, s)
	}
	if len(dp.steps) > 0 {
		last := dp.steps[len(dp.steps)-1]
		dp.name, dp.index = last.name, max(0, last.index)
	}
	return dp
}

// makeData finds or makes the data node a field writes to.
func (f *Form) makeData(in *Inst) *Node {
	if in.dnode != nil {
		return in.dnode
	}
	dp := in.dpath
	if dp == nil {
		return nil
	}
	var parent *Node
	if len(dp.steps) > 0 {
		if dp.fromRecord {
			parent = f.dataRoot
		} else {
			parent = f.groupOf(dp.in)
		}
		for _, s := range dp.steps[:len(dp.steps)-1] {
			parent = nthChild(parent, s.name, max(0, s.index), true)
		}
	} else {
		parent = f.groupOf(dp.in)
	}
	if parent == nil {
		return nil
	}
	in.dnode = nthChild(parent, dp.name, dp.index, true)
	in.dpath = nil
	if in.match == "global" {
		for _, o := range f.order {
			if o.match == "global" && o.Name == in.Name && o.dnode == nil {
				o.dnode = in.dnode
				o.dpath = nil
			}
		}
	}
	return in.dnode
}

// groupOf is a subform instance's data group, made (with its parents) if
// it doesn't exist yet.
func (f *Form) groupOf(in *Inst) *Node {
	if in == nil {
		return f.dataRoot
	}
	if in.group != nil {
		return in.group
	}
	if in.dpath == nil {
		if in.Parent == nil {
			return f.dataRoot
		}
		return f.groupOf(groupInst(in.Parent))
	}
	parent := f.groupOf(groupInst(in.dpath.in))
	in.group = nthChild(parent, in.dpath.name, in.dpath.index, true)
	in.dpath = nil
	return in.group
}

// nthChild is the index-th element child of n named name; with make, the
// missing ones are appended.
func nthChild(n *Node, name string, index int, make bool) *Node {
	if n == nil {
		return nil
	}
	i := 0
	for _, k := range n.Elems() {
		if k.Local == name {
			if i == index {
				return k
			}
			i++
		}
	}
	if !make {
		return nil
	}
	var k *Node
	for ; i <= index; i++ {
		k = n.Append(NewElem(name))
	}
	return k
}
