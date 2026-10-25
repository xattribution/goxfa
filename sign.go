package xfa

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"errors"
	"fmt"
	"image"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"github.com/xattribution/goxfa/internal/cms"
)

// SignOptions describe a signature.
type SignOptions struct {
	// Field is the signature field's SOM path, as Fields lists it.
	Field string
	// Signer holds the private key: a smart card's key through
	// winsign, or any crypto.Signer. RSA and ECDSA keys work.
	Signer crypto.Signer
	// Chain is the signing certificate first, then the certificates that
	// issued it. Include them up to the root, so readers that trust the
	// root can build the chain offline.
	Chain []*x509.Certificate

	// Name, Reason, Location and ContactInfo go in the signature as
	// written. Readers take the signer's name from the certificate; Name
	// is what the signature's appearance shows.
	Name, Reason, Location, ContactInfo string
	// Time is the signing time; the zero time is now. Without a
	// timestamp it's only the computer's clock, and readers say so.
	Time time.Time

	// CAdES makes a PAdES-style signature (/ETSI.CAdES.detached) instead
	// of the default /adbe.pkcs7.detached.
	CAdES bool
	// OCSP responses and CRLs (DER), embedded so the signature validates
	// offline and after the certificate expires. FetchOCSP gets them.
	OCSP, CRLs [][]byte
	// Timestamp, when set, gets an RFC 3161 timestamp token for the
	// signature value. TimestampAuthority makes one for a URL.
	Timestamp func(signature []byte) ([]byte, error)

	// Graphic, when set, is drawn in the signature's appearance: on the
	// left half with the text on the right, as Acrobat lays out a
	// signature with a graphic, or across the whole box when Text is
	// empty. It keeps its proportions, and transparent pixels stay
	// clear. At most 4096 pixels on a side.
	Graphic image.Image
	// Text replaces the appearance's lines of text. Nil draws "Digitally
	// signed by NAME" and the date; an empty, non-nil slice draws none.
	// Set Time to know the date the signature will carry.
	Text []string
}

// SignatureField is a signature field and what's in it.
type SignatureField struct {
	SOM    string `json:"som"`
	Label  string `json:"label,omitempty"`
	Signed bool   `json:"signed"`
	// Signer is the signing certificate's common name, as the signature
	// carries it (it is not verified here). On a CAC it ends in the
	// holder's ID number; show Name instead.
	Signer string `json:"signer,omitempty"`
	// Name is the signer's name to show: the signature's /Name, or one
	// made from the certificate (LAST, FIRST M, without an ID number).
	Name string    `json:"name,omitempty"`
	Time time.Time `json:"time,omitzero"`
	// Locks are the fields this signature locks once it's signed: the
	// field's manifest in the template.
	Locks []string `json:"locks,omitempty"`
}

var (
	// ErrSigned means the signature field already holds a signature.
	ErrSigned = errors.New("xfa: the field is already signed")
	// ErrLocked means a signature locks the field.
	ErrLocked = errors.New("xfa: the field is locked")
)

// SignatureFields lists the form's signature fields, signed or not.
func (f *Form) SignatureFields() []SignatureField {
	signed := f.pdfSigs()
	var out []SignatureField
	for _, in := range f.order {
		if ui, _ := uiKind(in.T); ui != "signature" {
			continue
		}
		sf := SignatureField{SOM: in.SOM, Label: strings.TrimSpace(captionText(in.T)), Locks: f.manifest(in)}
		if s, ok := signed[in.SOM]; ok {
			sf.Signed, sf.Signer, sf.Name, sf.Time = true, s.signer, s.name, s.time
		}
		out = append(out, sf)
	}
	return out
}

type pdfSig struct {
	signer string // the certificate's common name
	name   string // the name to show: the signature's /Name, or one made from the certificate
	time   time.Time
}

// pdfSigs is pdfSignatures, read once.
func (f *Form) pdfSigs() map[string]pdfSig {
	if f.sigCache == nil {
		f.sigCache = f.pdfSignatures()
	}
	return f.sigCache
}

var reCACName = regexp.MustCompile(`^([A-Z][A-Z' -]*)\.([A-Z][A-Z' -]*)(?:\.([A-Z][A-Z' -]*))?(?:\.[A-Z]+)?\.\d{10}$`)

// displayName is a person's name from a certificate. Names like
// LAST.FIRST.MIDDLE.1234567890 show as LAST, FIRST M, without the number.
func displayName(c *x509.Certificate) string {
	cn := strings.TrimSpace(c.Subject.CommonName)
	if m := reCACName.FindStringSubmatch(cn); m != nil {
		s := m[1] + ", " + m[2]
		if m[3] != "" {
			s += " " + m[3][:1]
		}
		return s
	}
	return cn
}

// pdfSignatures are the signed AcroForm fields, by full name. An XFA
// form's signature field is the AcroForm field named by its SOM path.
func (f *Form) pdfSignatures() map[string]pdfSig {
	out := map[string]pdfSig{}
	x := f.pdf.x
	acro := f.acroForm()
	if acro == nil {
		return out
	}
	var walk func(o types.Object, prefix string, depth int)
	walk = func(o types.Object, prefix string, depth int) {
		if depth > 32 {
			return
		}
		arr, err := x.DereferenceArray(o)
		if err != nil {
			return
		}
		for _, k := range arr {
			d, err := x.DereferenceDict(k)
			if err != nil || d == nil {
				continue
			}
			name := prefix
			if t, err := x.DereferenceText(d["T"]); err == nil && t != "" {
				if name != "" {
					name += "."
				}
				name += t
			}
			if v, ok := d.Find("V"); ok {
				if sd, err := x.DereferenceDict(v); err == nil && sd != nil {
					if ty := sd.NameEntry("Type"); ty == nil || *ty == "Sig" {
						if _, has := sd.Find("Contents"); has {
							out[name] = sigInfo(sd)
						}
					}
				}
			}
			if kids, ok := d.Find("Kids"); ok {
				walk(kids, name, depth+1)
			}
		}
	}
	if fs, ok := acro.Find("Fields"); ok {
		walk(fs, "", 0)
	}
	return out
}

func sigInfo(sd types.Dict) pdfSig {
	var s pdfSig
	c, ok := sd.Find("Contents")
	if !ok {
		return s
	}
	var der []byte
	switch v := c.(type) {
	case types.HexLiteral:
		der, _ = v.Bytes()
	case types.StringLiteral:
		der, _ = types.Unescape(v.Value())
	}
	if cert := cms.SignerCertificate(der); cert != nil {
		s.signer = cert.Subject.CommonName
		s.name = displayName(cert)
	}
	if n, ok := sd.Find("Name"); ok {
		if t, err := types.StringOrHexLiteral(n); err == nil && t != nil && *t != "" {
			s.name = *t
		}
	}
	if m, ok := sd.Find("M"); ok {
		if t, err := types.StringOrHexLiteral(m); err == nil && t != nil {
			if d, ok := types.DateTime(*t, true); ok {
				s.time = d
			}
		}
	}
	return s
}

func (f *Form) acroForm() types.Dict {
	x := f.pdf.x
	root, err := x.Catalog()
	if err != nil {
		return nil
	}
	ao, ok := root.Find("AcroForm")
	if !ok {
		return nil
	}
	d, err := x.DereferenceDict(ao)
	if err != nil {
		return nil
	}
	return d
}

// manifest is the SOM paths a signature field locks once signed.
func (f *Form) manifest(in *Inst) []string {
	_, ui := uiKind(in.T)
	m := ui.Child("manifest")
	if m == nil {
		return nil
	}
	if use := strings.TrimPrefix(m.Attr("use"), "#"); use != "" {
		if p := f.template.FindID(use); p != nil {
			m = p
		}
	}
	action := m.Attr("action")
	if action == "" {
		action = "include"
	}
	var refs []string
	for _, r := range m.Children("ref") {
		s := strings.TrimSpace(r.TextContent())
		for _, p := range []string{"xfa[0].template[0].", "xfa[0].form[0].", "$template.", "$form.", "xfa.template.", "xfa.form."} {
			s = strings.TrimPrefix(s, p)
		}
		if s != "" {
			refs = append(refs, s)
		}
	}
	covered := func(som string) bool {
		for _, r := range refs {
			if som == r || strings.HasPrefix(som, r+".") {
				return true
			}
		}
		return false
	}
	var out []string
	for _, k := range f.order {
		if k == in || k.match == "member" {
			continue
		}
		c := covered(k.SOM)
		if action == "all" || (action == "include" && c) || (action == "exclude" && !c) {
			out = append(out, k.SOM)
		}
	}
	return out
}

// locked is the fields signed signatures lock, each with the signature
// that locks it.
func (f *Form) locked() map[string]string {
	if f.locks != nil {
		return f.locks
	}
	f.locks = map[string]string{}
	signed := f.pdfSigs()
	for _, in := range f.order {
		if _, ok := signed[in.SOM]; !ok {
			continue
		}
		f.locks[in.SOM] = in.SOM // a signed field can't take a value
		for _, som := range f.manifest(in) {
			f.locks[som] = in.SOM
		}
	}
	return f.locks
}

// sigPlaceholders are written in the update and filled in once the
// signature is made.
const byteRangeHole = "[0 0000000000 0000000000 0000000000]"

// Sign signs the form's signature field and returns the signed PDF:
// the original bytes, then one incremental update with any values set
// since the form was opened, the signature field and the signature. The
// form then stands for the signed PDF, so later values go after the
// signature.
//
// The signature covers the whole file. A dynamic form's signature field
// is the AcroForm field named by its SOM path, with an invisible widget;
// Acrobat shows the signature in the XFA field's place.
func (f *Form) Sign(o SignOptions) ([]byte, error) {
	in := f.bySOM[o.Field]
	if in == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoField, o.Field)
	}
	if ui, _ := uiKind(in.T); ui != "signature" {
		return nil, fmt.Errorf("xfa: %s isn't a signature field", o.Field)
	}
	if _, ok := f.pdfSigs()[o.Field]; ok {
		return nil, ErrSigned
	}
	if o.Signer == nil || len(o.Chain) == 0 {
		return nil, errors.New("xfa: signing needs a signer and its certificate")
	}
	if f.pdf.whole {
		return nil, errors.New("xfa: signing forms kept as one XDP stream isn't supported")
	}
	if err := checkSigningCert(o.Chain[0]); err != nil {
		return nil, err
	}
	if g := o.Graphic; g != nil && (g.Bounds().Dx() <= 0 || g.Bounds().Dy() <= 0 || g.Bounds().Dx() > maxGraphic || g.Bounds().Dy() > maxGraphic) {
		return nil, errors.New("xfa: the signature graphic must be 1 to 4096 pixels on a side")
	}
	when := o.Time
	if when.IsZero() {
		when = time.Now()
	}

	x := f.pdf.x
	next := nextObjNum(x)
	alloc := func() int { next++; return next - 1 }
	var objs []updObj

	// values set since the form was opened go in the signed revision
	if f.dirty {
		for _, p := range f.pdf.packets {
			if p.name == "datasets" {
				objs = append(objs, updObj{num: p.ref.ObjectNumber.Value(), gen: p.ref.GenerationNumber.Value(), stream: f.dsDoc.Bytes()})
			}
		}
	}
	// and so do the page numbers, which Acrobat would otherwise count as
	// filled in after the signature (pagestate.go)
	if fp, b := f.pageState(); fp != nil {
		objs = append(objs, updObj{num: fp.ref.ObjectNumber.Value(), gen: fp.ref.GenerationNumber.Value(), stream: b})
	}

	// the AcroForm, which may sit inside the catalog
	root, err := x.Catalog()
	if err != nil {
		return nil, err
	}
	ao, ok := root.Find("AcroForm")
	if !ok {
		return nil, ErrNoXFA
	}
	acro, err := x.DereferenceDict(ao)
	if err != nil || acro == nil {
		return nil, ErrNoXFA
	}
	acro = acro.Clone().(types.Dict)
	acro["SigFlags"] = types.Integer(3)

	// the page the widget goes on
	pageRef := firstPage(x, root)
	if pageRef == nil {
		return nil, errors.New("xfa: the PDF has no page to put the signature's widget on")
	}

	sigNum := alloc()
	sigRef := *types.NewIndirectRef(sigNum, 0)

	// find or make the field, a level per SOM segment
	segs := strings.Split(o.Field, ".")
	fields, _ := x.DereferenceArray(acro["Fields"])
	fields = append(types.Array(nil), fields...)
	var parentRef *types.IndirectRef
	var parentDict types.Dict // the existing parent being rewritten, or nil while at the top
	var parentNum, parentGen int
	level := fields
	for i, seg := range segs {
		var found *types.IndirectRef
		var fd types.Dict
		for _, k := range level {
			ir, ok := k.(types.IndirectRef)
			if !ok {
				continue
			}
			d, err := x.DereferenceDict(ir)
			if err != nil || d == nil {
				continue
			}
			if t, _ := x.DereferenceText(d["T"]); t == seg {
				found, fd = &ir, d
				break
			}
		}
		last := i == len(segs)-1
		if found != nil && !last {
			parentRef, parentDict = found, fd.Clone().(types.Dict)
			parentNum, parentGen = found.ObjectNumber.Value(), found.GenerationNumber.Value()
			kids, _ := x.DereferenceArray(fd["Kids"])
			level = kids
			continue
		}
		if found != nil && last {
			// an existing signature field (static forms): fill it in
			leaf := fd.Clone().(types.Dict)
			if ft := leaf.NameEntry("FT"); ft != nil && *ft != "Sig" {
				return nil, fmt.Errorf("xfa: the PDF's field %s isn't a signature field", o.Field)
			}
			leaf["FT"] = types.Name("Sig")
			leaf["V"] = sigRef
			objs = append(objs, updObj{num: found.ObjectNumber.Value(), gen: found.GenerationNumber.Value(), value: leaf})
			if ap, err := f.appearance(leaf, o, when, alloc); err == nil && ap != nil {
				objs = append(objs, ap...)
				leaf["AP"] = types.Dict{"N": *types.NewIndirectRef(ap[0].num, 0)}
			}
			break
		}
		// make the rest of the path
		var top types.IndirectRef
		var prev *types.IndirectRef = parentRef
		var prevDict types.Dict
		for j := i; j < len(segs); j++ {
			n := alloc()
			ref := *types.NewIndirectRef(n, 0)
			d := types.Dict{"T": pdfText(segs[j])}
			if prev != nil {
				d["Parent"] = *prev
			}
			if j == len(segs)-1 {
				d["FT"] = types.Name("Sig")
				d["V"] = sigRef
				d["Type"] = types.Name("Annot")
				d["Subtype"] = types.Name("Widget")
				d["Rect"] = types.Array{types.Integer(0), types.Integer(0), types.Integer(0), types.Integer(0)}
				d["F"] = types.Integer(132) // print, locked
				d["P"] = *pageRef
				// viewers that lay out XFA draw this in the field's box
				bw, bh := f.fieldBoxSize(o.Field)
				ap := appearanceStream(bw, bh, o, when, alloc)
				objs = append(objs, ap...)
				d["AP"] = types.Dict{"N": *types.NewIndirectRef(ap[0].num, 0)}
				if err := addAnnot(x, pageRef, ref, &objs); err != nil {
					return nil, err
				}
			}
			if prevDict != nil {
				prevDict["Kids"] = types.Array{ref}
			}
			objs = append(objs, updObj{num: n, gen: 0, value: d})
			if j == i {
				top = ref
			}
			prev, prevDict = &ref, d
		}
		if parentDict != nil {
			kids, _ := x.DereferenceArray(parentDict["Kids"])
			parentDict["Kids"] = append(append(types.Array(nil), kids...), top)
			objs = append(objs, updObj{num: parentNum, gen: parentGen, value: parentDict})
		} else {
			fields = append(fields, top)
		}
		break
	}
	acro["Fields"] = fields

	// write the AcroForm back where it lives
	if ir, ok := ao.(types.IndirectRef); ok {
		objs = append(objs, updObj{num: ir.ObjectNumber.Value(), gen: ir.GenerationNumber.Value(), value: acro})
	} else {
		cat := root.Clone().(types.Dict)
		cat["AcroForm"] = acro
		objs = append(objs, updObj{num: x.Root.ObjectNumber.Value(), gen: x.Root.GenerationNumber.Value(), value: cat})
	}

	// the signature, with room for the CMS
	hash := hashFor(o.Signer.Public())
	reserve := 8192 + 2048*len(o.Chain)
	for _, c := range o.Chain {
		reserve += len(c.Raw)
	}
	for _, b := range append(append([][]byte(nil), o.OCSP...), o.CRLs...) {
		reserve += len(b)
	}
	if o.Timestamp != nil {
		reserve += 8192
	}
	sub := "adbe.pkcs7.detached"
	if o.CAdES {
		sub = "ETSI.CAdES.detached"
	}
	contentsHole := "<" + strings.Repeat("0", 2*reserve) + ">"
	sd := types.Dict{
		"Type":      types.Name("Sig"),
		"Filter":    types.Name("Adobe.PPKLite"),
		"SubFilter": types.Name(sub),
		"ByteRange": rawPDF(byteRangeHole),
		"Contents":  rawPDF(contentsHole),
		"M":         pdfText(pdfDate(when)),
	}
	for k, v := range map[string]string{"Name": o.Name, "Reason": o.Reason, "Location": o.Location, "ContactInfo": o.ContactInfo} {
		if v != "" {
			sd[k] = pdfText(v)
		}
	}
	objs = append(objs, updObj{num: sigNum, gen: 0, value: sd})

	out, err := appendUpdate(f.pdf.data, x, objs)
	if err != nil {
		return nil, err
	}

	// fill in the byte range, then the signature
	base := len(f.pdf.data)
	ci := bytes.Index(out[base:], []byte("/Contents "+contentsHole))
	bi := bytes.Index(out[base:], []byte("/ByteRange "+byteRangeHole))
	if ci < 0 || bi < 0 {
		return nil, errors.New("xfa: the signature's placeholders weren't written")
	}
	lt := base + ci + len("/Contents ")
	gt := lt + len(contentsHole)
	br := fmt.Sprintf("[0 %d %d %d", lt, gt, len(out)-gt)
	br += strings.Repeat(" ", len(byteRangeHole)-len(br)-1) + "]"
	copy(out[base+bi+len("/ByteRange "):], br)

	h := hash.New()
	h.Write(out[:lt])
	h.Write(out[gt:])
	der, err := cms.Sign(cms.Options{
		Digest: h.Sum(nil), Hash: hash, Signer: o.Signer, Chain: o.Chain,
		SigningTime:        signingTime(o),
		SigningCertificate: o.CAdES,
		OCSP:               o.OCSP, CRLs: o.CRLs,
		Timestamp: o.Timestamp,
	})
	if err != nil {
		return nil, err
	}
	hx := fmt.Sprintf("%X", der)
	if len(hx) > 2*reserve {
		return nil, fmt.Errorf("xfa: the signature is %d bytes, more than the %d reserved", len(der), reserve)
	}
	copy(out[lt+1:], hx)

	g, err := Open(out)
	if err != nil {
		return nil, fmt.Errorf("xfa: the signed PDF doesn't reopen: %w", err)
	}
	*f = *g
	return out, nil
}

// signingTime is left out of CAdES signatures, which carry the time in
// /M and in a timestamp instead.
func signingTime(o SignOptions) time.Time {
	if o.CAdES {
		return time.Time{}
	}
	if o.Time.IsZero() {
		return time.Now()
	}
	return o.Time
}

func hashFor(pub crypto.PublicKey) crypto.Hash {
	if k, ok := pub.(*ecdsa.PublicKey); ok {
		switch k.Curve {
		case elliptic.P384():
			return crypto.SHA384
		case elliptic.P521():
			return crypto.SHA512
		}
	}
	return crypto.SHA256
}

// checkSigningCert refuses certificates that can't sign documents: on a
// smart card, the encryption key and (usually) the authentication key.
func checkSigningCert(c *x509.Certificate) error {
	if c.KeyUsage != 0 && c.KeyUsage&(x509.KeyUsageDigitalSignature|x509.KeyUsageContentCommitment) == 0 {
		return errors.New("xfa: this certificate isn't for signing; choose the card's signature certificate")
	}
	now := time.Now()
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return fmt.Errorf("xfa: this certificate is valid only from %s to %s", c.NotBefore.Format("02 Jan 06"), c.NotAfter.Format("02 Jan 06"))
	}
	return nil
}

// addAnnot puts a widget on the page's /Annots, rewriting the page or
// its annotation array.
func addAnnot(x interface {
	DereferenceDict(types.Object) (types.Dict, error)
	DereferenceArray(types.Object) (types.Array, error)
}, page *types.IndirectRef, w types.IndirectRef, objs *[]updObj) error {
	pd, err := x.DereferenceDict(*page)
	if err != nil || pd == nil {
		return errors.New("xfa: the page can't be read")
	}
	if a, ok := pd.Find("Annots"); ok {
		if ir, ok := a.(types.IndirectRef); ok {
			arr, _ := x.DereferenceArray(ir)
			*objs = append(*objs, updObj{num: ir.ObjectNumber.Value(), gen: ir.GenerationNumber.Value(), value: append(append(types.Array(nil), arr...), w)})
			return nil
		}
	}
	pd = pd.Clone().(types.Dict)
	arr, _ := x.DereferenceArray(pd["Annots"])
	pd["Annots"] = append(append(types.Array(nil), arr...), w)
	*objs = append(*objs, updObj{num: page.ObjectNumber.Value(), gen: page.GenerationNumber.Value(), value: pd})
	return nil
}

// pdfDate is a PDF date: D:YYYYMMDDHHmmSS+HH'mm'.
func pdfDate(t time.Time) string {
	_, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("D:%s%s%02d'%02d'", t.Format("20060102150405"), sign, off/3600, off%3600/60)
}

// appearance draws a visible widget's signature: the name and the time,
// for fields that have a place on a page (static forms).
func (f *Form) appearance(widget types.Dict, o SignOptions, when time.Time, alloc func() int) ([]updObj, error) {
	r, err := f.pdf.x.DereferenceArray(widget["Rect"])
	if err != nil || len(r) != 4 {
		return nil, err
	}
	var v [4]float64
	for i, e := range r {
		switch n := e.(type) {
		case types.Integer:
			v[i] = float64(n)
		case types.Float:
			v[i] = float64(n)
		}
	}
	w, h := v[2]-v[0], v[3]-v[1]
	if w < 0 {
		w = -w
	}
	if h < 0 {
		h = -h
	}
	if w < 4 || h < 4 {
		// a widget with no place on a page: draw for the XFA field's box
		w, h = f.fieldBoxSize(o.Field)
	}
	return appearanceStream(w, h, o, when, alloc), nil
}

// fieldBoxSize is the size of a field's box in the form's layout, where
// viewers that lay out XFA draw the signature's appearance.
func (f *Form) fieldBoxSize(som string) (w, h float64) {
	w, h = 200, 30
	doc, err := f.Layout()
	if err != nil {
		return
	}
	var find func([]Item) bool
	find = func(items []Item) bool {
		for _, it := range items {
			if it.Field != nil && it.Field.SOM == som && it.W >= 4 && it.H >= 4 {
				w, h = it.W, it.H
				return true
			}
			if find(it.Items) {
				return true
			}
		}
		return false
	}
	for _, p := range doc.Pages {
		if find(p.Items) {
			break
		}
	}
	return
}

// appearanceStream draws the signature's appearance in a w×h box: the
// text ("Digitally signed by NAME" and the time, unless o.Text says
// otherwise) and, with o.Graphic, the graphic on its left.
func appearanceStream(w, h float64, o SignOptions, when time.Time, alloc func() int) []updObj {
	lines := o.Text
	if lines == nil {
		name := o.Name
		if name == "" {
			name = displayName(o.Chain[0])
		}
		lines = []string{"Digitally signed by " + name, "Date: " + appearanceDate(when)}
	}
	const pad = 2.0
	var c strings.Builder
	n := alloc()
	objs := []updObj{{num: n}}
	resources := types.Dict{}
	tx, tw := pad, w-2*pad // where the text goes
	if o.Graphic != nil {
		gx, gw := pad, w-2*pad
		if len(lines) > 0 {
			gw = w/2 - 2*pad
			tx, tw = w/2+pad, w/2-2*pad
		}
		if img, err := graphicObjects(o.Graphic, alloc); err == nil && gw > 1 && h > 2*pad+1 {
			b := o.Graphic.Bounds()
			iw, ih := float64(b.Dx()), float64(b.Dy())
			bh := h - 2*pad
			scale := min(gw/iw, bh/ih)
			dw, dh := iw*scale, ih*scale
			dx, dy := gx+(gw-dw)/2, pad+(bh-dh)/2
			fmt.Fprintf(&c, "q %s 0 0 %s %s %s cm /Im0 Do Q\n", num2(dw), num2(dh), num2(dx), num2(dy))
			resources["XObject"] = types.Dict{"Im0": *types.NewIndirectRef(img[0].num, 0)}
			objs = append(objs, img...)
		}
	}
	if len(lines) > 0 && tw > 1 {
		base := min(h/(1.2*float64(len(lines))+0.4), 9)
		fc := faceFor(Font{Typeface: "Helvetica", Size: base})
		size := base
		for _, l := range lines {
			// the widest line sets the size; widths are at the base size
			if lw := fc.width(l); lw > tw {
				size = min(size, base*tw/lw)
			}
		}
		c.WriteString("BT /Helv " + num2(size) + " Tf 0 g\n")
		// the block of lines centered top to bottom
		y := (h+1.2*size*float64(len(lines)))/2 - 0.95*size
		for i, l := range lines {
			fmt.Fprintf(&c, "1 0 0 1 %s %s Tm %s Tj\n", num2(tx), num2(y-float64(i)*size*1.2), pdfString(l))
		}
		c.WriteString("ET\n")
		resources["Font"] = types.Dict{"Helv": types.Dict{
			"Type": types.Name("Font"), "Subtype": types.Name("Type1"), "BaseFont": types.Name("Helvetica"), "Encoding": types.Name("WinAnsiEncoding"),
		}}
	}
	objs[0].stream = []byte(c.String())
	objs[0].dict = types.Dict{
		"Type":      types.Name("XObject"),
		"Subtype":   types.Name("Form"),
		"BBox":      types.Array{types.Integer(0), types.Integer(0), types.Float(w), types.Float(h)},
		"Resources": resources,
	}
	return objs
}

func num2(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

// appearanceDate is the signing time as Acrobat shows it in a signature,
// "2026.10.25 19:41:00 -10'00'", with the zone's own minutes (+05'30').
func appearanceDate(t time.Time) string {
	_, off := t.Zone()
	sign := "+"
	if off < 0 {
		sign, off = "-", -off
	}
	return t.Format("2006.01.02 15:04:05 ") + fmt.Sprintf("%s%02d'%02d'", sign, off/3600, off%3600/60)
}

// firstPage is the first leaf of the page tree.
func firstPage(x *model.XRefTable, root types.Dict) *types.IndirectRef {
	o, ok := root.Find("Pages")
	for depth := 0; ok && depth < 32; depth++ {
		ir, isRef := o.(types.IndirectRef)
		d, err := x.DereferenceDict(o)
		if err != nil || d == nil {
			return nil
		}
		if t := d.NameEntry("Type"); t != nil && *t == "Page" {
			if !isRef {
				return nil
			}
			return &ir
		}
		kids, err := x.DereferenceArray(d["Kids"])
		if err != nil || len(kids) == 0 {
			return nil
		}
		o, ok = kids[0], true
	}
	return nil
}
