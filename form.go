package xfa

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Form is an XFA form read from a PDF: its packets, the form built from
// the template and the data, and what's changed since it was opened.
type Form struct {
	pdf *pdfFile

	tmplDoc, dsDoc, cfgDoc, formDoc *Node
	xdpDoc                          *Node // the whole XDP, for forms that keep it in one stream
	template                        *Node // the <template> element
	datasets                        *Node // xfa:datasets
	data                            *Node // xfa:data
	dataRoot                        *Node // the root data group (named after the root subform)

	dynamic bool // laid out by the viewer (dynamicRender required), not by the PDF's own pages
	dirty   bool

	root  *Inst // the merged form: one instance per container
	bySOM map[string]*Inst
	order []*Inst // fields and exclusion groups in document order
}

// Open reads an XFA form from a PDF. Encrypted forms open with the empty
// user password, as Reader opens them.
func Open(pdf []byte) (f *Form, err error) {
	defer func() {
		if p := recover(); p != nil {
			f, err = nil, fmt.Errorf("xfa: couldn't read the form (%v)", p)
		}
	}()
	pf, err := readPDF(pdf)
	if err != nil {
		return nil, err
	}
	f = &Form{pdf: pf}
	if pf.whole {
		doc, err := ParseXML(pf.packets[0].raw)
		if err != nil {
			return nil, fmt.Errorf("xfa: the XDP isn't valid XML: %w", err)
		}
		f.xdpDoc = doc
		xdp := doc.FirstElem()
		for _, k := range xdp.Elems() {
			switch k.Local {
			case "template":
				f.template = k
			case "datasets":
				f.datasets = k
			case "config":
				f.cfgDoc = k
			}
		}
	} else {
		for _, p := range pf.packets {
			switch p.name {
			case "template", "datasets", "config", "form":
			default:
				continue
			}
			doc, err := ParseXML(p.raw)
			if err != nil {
				return nil, fmt.Errorf("xfa: the %s packet isn't valid XML: %w", p.name, err)
			}
			switch p.name {
			case "template":
				f.tmplDoc, f.template = doc, doc.FirstElem()
			case "datasets":
				f.dsDoc, f.datasets = doc, doc.FirstElem()
			case "config":
				f.cfgDoc = doc
			case "form":
				f.formDoc = doc
			}
		}
	}
	if f.template == nil || f.template.Local != "template" {
		return nil, ErrNoXFA
	}
	f.dynamic = strings.Contains(string(cfgText(f.cfgDoc, "dynamicRender")), "required")
	resolveProtos(f.template)
	f.bindData()
	if err := f.merge(); err != nil {
		return nil, err
	}
	return f, nil
}

func cfgText(cfg *Node, name string) string {
	var found string
	var walk func(n *Node)
	walk = func(n *Node) {
		if n == nil || found != "" {
			return
		}
		for _, k := range n.Kids {
			if k.Kind != ElemNode {
				continue
			}
			if k.Local == name {
				found = strings.TrimSpace(k.TextContent())
				return
			}
			walk(k)
		}
	}
	walk(cfg)
	return found
}

// Dynamic reports whether the form is a dynamic XFA form: one whose pages
// exist only as its design, which browsers and most PDF readers can't show.
// Static XFA forms carry ordinary PDF pages as well.
func (f *Form) Dynamic() bool { return f.dynamic }

// Signed reports whether the PDF says it carries signatures.
func (f *Form) Signed() bool { return f.pdf.sigs }

// bindData finds or makes the data packet: xfa:datasets/xfa:data/<root>.
func (f *Form) bindData() {
	if f.datasets == nil {
		f.dsDoc = &Node{Kind: DocNode}
		ds := f.dsDoc.Append(NewElem("xfa:datasets"))
		ds.SetAttr("xmlns:xfa", "http://www.xfa.org/schema/xfa-data/1.0/")
		f.datasets = ds
	}
	f.data = f.datasets.Child("data")
	if f.data == nil {
		f.data = f.datasets.Append(NewElem(f.datasets.Space + prefixSep(f.datasets.Space) + "data"))
	}
	rootName := ""
	if rs := f.template.Child("subform"); rs != nil {
		rootName = rs.Attr("name")
	}
	for _, k := range f.data.Elems() {
		if rootName == "" || k.Local == rootName {
			f.dataRoot = k
			break
		}
	}
	if f.dataRoot == nil {
		name := rootName
		if name == "" {
			name = "form1"
		}
		f.dataRoot = f.data.Append(NewElem(name))
	}
}

func prefixSep(p string) string {
	if p == "" {
		return ""
	}
	return ":"
}

// Save writes the form's values back into the PDF: a new revision of the
// datasets packet appended to the original file, as Acrobat saves. The
// original bytes, earlier signatures and Reader usage rights stay as they
// were. Nothing changed gives the original bytes.
func (f *Form) Save() ([]byte, error) {
	if !f.dirty {
		return f.pdf.data, nil
	}
	if f.pdf.whole {
		p := f.pdf.packets[0]
		gen := p.ref.GenerationNumber.Value()
		return appendUpdate(f.pdf.data, f.pdf.x, map[int]newStream{p.ref.ObjectNumber.Value(): {gen: gen, content: f.xdpDoc.Bytes()}})
	}
	for _, p := range f.pdf.packets {
		if p.name == "datasets" {
			gen := p.ref.GenerationNumber.Value()
			return appendUpdate(f.pdf.data, f.pdf.x, map[int]newStream{p.ref.ObjectNumber.Value(): {gen: gen, content: f.dsDoc.Bytes()}})
		}
	}
	return nil, errors.New("xfa: the form has no datasets packet to write the values into")
}

// DataXML is the datasets packet as it stands, for inspection or export.
func (f *Form) DataXML() []byte {
	var b bytes.Buffer
	f.datasets.write(&b)
	return b.Bytes()
}
