package xfa

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// A small XML tree that writes back what it read. encoding/xml's encoder
// rewrites namespace prefixes, and XFA packets must come back equivalent
// apart from the values changed, so packets are read with RawToken
// (prefixes kept as written) into this tree and written back by Node.write.

// NodeKind says what a Node is.
type NodeKind uint8

const (
	DocNode NodeKind = iota
	ElemNode
	TextNode
	CommentNode
	ProcInstNode
	DirectiveNode
)

// Node is one node of an XML packet.
type Node struct {
	Kind   NodeKind
	Space  string // the prefix as written ("xfa" in xfa:data), not the namespace URI
	Local  string // element name, or the target of a processing instruction
	Attrs  []xml.Attr
	Text   string // character data, comment, processing instruction body or directive
	Kids   []*Node
	Parent *Node
}

const maxXMLDepth = 512

// ParseXML reads a packet into a tree. It refuses documents nested deeper
// than 512 elements, which no real form needs.
func ParseXML(b []byte) (*Node, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	doc := &Node{Kind: DocNode}
	cur := doc
	depth := 0
	for {
		t, err := d.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := t.(type) {
		case xml.StartElement:
			if depth++; depth > maxXMLDepth {
				return nil, errors.New("xfa: XML nested too deeply")
			}
			n := &Node{Kind: ElemNode, Space: t.Name.Space, Local: t.Name.Local, Parent: cur}
			n.Attrs = append([]xml.Attr(nil), t.Attr...)
			cur.Kids = append(cur.Kids, n)
			cur = n
		case xml.EndElement:
			if cur.Parent == nil {
				return nil, errors.New("xfa: unbalanced XML")
			}
			depth--
			cur = cur.Parent
		case xml.CharData:
			cur.Kids = append(cur.Kids, &Node{Kind: TextNode, Text: string(t), Parent: cur})
		case xml.Comment:
			cur.Kids = append(cur.Kids, &Node{Kind: CommentNode, Text: string(t), Parent: cur})
		case xml.ProcInst:
			cur.Kids = append(cur.Kids, &Node{Kind: ProcInstNode, Local: t.Target, Text: string(t.Inst), Parent: cur})
		case xml.Directive:
			cur.Kids = append(cur.Kids, &Node{Kind: DirectiveNode, Text: string(t), Parent: cur})
		}
	}
	return doc, nil
}

// Bytes writes the tree back as XML.
func (n *Node) Bytes() []byte {
	var b bytes.Buffer
	n.write(&b)
	return b.Bytes()
}

func (n *Node) write(b *bytes.Buffer) {
	switch n.Kind {
	case DocNode:
		for _, k := range n.Kids {
			k.write(b)
		}
	case ElemNode:
		b.WriteByte('<')
		b.WriteString(n.QName())
		for _, a := range n.Attrs {
			b.WriteByte(' ')
			if a.Name.Space != "" {
				b.WriteString(a.Name.Space)
				b.WriteByte(':')
			}
			b.WriteString(a.Name.Local)
			b.WriteString(`="`)
			escapeAttr(b, a.Value)
			b.WriteByte('"')
		}
		if len(n.Kids) == 0 {
			b.WriteString("/>")
			return
		}
		b.WriteByte('>')
		for _, k := range n.Kids {
			k.write(b)
		}
		b.WriteString("</")
		b.WriteString(n.QName())
		b.WriteByte('>')
	case TextNode:
		escapeText(b, n.Text)
	case CommentNode:
		b.WriteString("<!--")
		b.WriteString(n.Text)
		b.WriteString("-->")
	case ProcInstNode:
		b.WriteString("<?")
		b.WriteString(n.Local)
		if n.Text != "" {
			b.WriteByte(' ')
			b.WriteString(n.Text)
		}
		b.WriteString("?>")
	case DirectiveNode:
		b.WriteString("<!")
		b.WriteString(n.Text)
		b.WriteByte('>')
	}
}

func escapeText(b *bytes.Buffer, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteRune(r)
		}
	}
}

func escapeAttr(b *bytes.Buffer, s string) {
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '"':
			b.WriteString("&quot;")
		case '\n':
			b.WriteString("&#xA;")
		case '\r':
			b.WriteString("&#xD;")
		case '\t':
			b.WriteString("&#x9;")
		default:
			b.WriteRune(r)
		}
	}
}

// QName is the element's name as written, with its prefix.
func (n *Node) QName() string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// Attr is the value of the attribute with this local name (any prefix).
func (n *Node) Attr(local string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// HasAttr says whether the attribute is present.
func (n *Node) HasAttr(local string) bool {
	if n == nil {
		return false
	}
	for _, a := range n.Attrs {
		if a.Name.Local == local {
			return true
		}
	}
	return false
}

// SetAttr sets an attribute by local name, adding it without a prefix when
// it's missing.
func (n *Node) SetAttr(local, v string) {
	for i, a := range n.Attrs {
		if a.Name.Local == local {
			n.Attrs[i].Value = v
			return
		}
	}
	n.Attrs = append(n.Attrs, xml.Attr{Name: xml.Name{Local: local}, Value: v})
}

// DelAttr removes an attribute by local name.
func (n *Node) DelAttr(local string) {
	out := n.Attrs[:0]
	for _, a := range n.Attrs {
		if a.Name.Local != local {
			out = append(out, a)
		}
	}
	n.Attrs = out
}

// Elems are the element children.
func (n *Node) Elems() []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, k := range n.Kids {
		if k.Kind == ElemNode {
			out = append(out, k)
		}
	}
	return out
}

// Child is the first element child with this local name.
func (n *Node) Child(local string) *Node {
	if n == nil {
		return nil
	}
	for _, k := range n.Kids {
		if k.Kind == ElemNode && k.Local == local {
			return k
		}
	}
	return nil
}

// Children are the element children with this local name.
func (n *Node) Children(local string) []*Node {
	if n == nil {
		return nil
	}
	var out []*Node
	for _, k := range n.Kids {
		if k.Kind == ElemNode && k.Local == local {
			out = append(out, k)
		}
	}
	return out
}

// Path follows child elements by local name.
func (n *Node) Path(names ...string) *Node {
	for _, s := range names {
		n = n.Child(s)
		if n == nil {
			return nil
		}
	}
	return n
}

// FirstElem is the first element child (the root element of a document).
func (n *Node) FirstElem() *Node {
	if n == nil {
		return nil
	}
	for _, k := range n.Kids {
		if k.Kind == ElemNode {
			return k
		}
	}
	return nil
}

// TextContent joins the character data under n.
func (n *Node) TextContent() string {
	if n == nil {
		return ""
	}
	if n.Kind == TextNode {
		return n.Text
	}
	var b strings.Builder
	var walk func(*Node)
	walk = func(m *Node) {
		for _, k := range m.Kids {
			switch k.Kind {
			case TextNode:
				b.WriteString(k.Text)
			case ElemNode:
				walk(k)
			}
		}
	}
	walk(n)
	return b.String()
}

// SetText replaces n's children with one text node (none for "").
func (n *Node) SetText(s string) {
	n.Kids = n.Kids[:0]
	if s != "" {
		n.Kids = append(n.Kids, &Node{Kind: TextNode, Text: s, Parent: n})
	}
}

// NewElem makes an element with an optional prefix ("xfa:data").
func NewElem(qname string) *Node {
	n := &Node{Kind: ElemNode, Local: qname}
	if i := strings.IndexByte(qname, ':'); i >= 0 {
		n.Space, n.Local = qname[:i], qname[i+1:]
	}
	return n
}

// Append adds k as n's last child.
func (n *Node) Append(k *Node) *Node {
	k.Parent = n
	n.Kids = append(n.Kids, k)
	return k
}

// Remove takes k out of n's children.
func (n *Node) Remove(k *Node) {
	for i, c := range n.Kids {
		if c == k {
			n.Kids = append(n.Kids[:i], n.Kids[i+1:]...)
			k.Parent = nil
			return
		}
	}
}

// Index is n's position among its parent's element children of the same
// name (the [n] in a SOM path).
func (n *Node) Index() int {
	if n == nil || n.Parent == nil {
		return 0
	}
	i := 0
	for _, k := range n.Parent.Kids {
		if k == n {
			return i
		}
		if k.Kind == ElemNode && k.Local == n.Local {
			i++
		}
	}
	return 0
}

// FindID finds the element under n (or n) whose id attribute is id.
func (n *Node) FindID(id string) *Node {
	if n == nil {
		return nil
	}
	if n.Kind == ElemNode && n.Attr("id") == id {
		return n
	}
	for _, k := range n.Kids {
		if f := k.FindID(id); f != nil {
			return f
		}
	}
	return nil
}

func (n *Node) String() string {
	if n == nil {
		return "<nil>"
	}
	return fmt.Sprintf("<%s>", n.QName())
}
