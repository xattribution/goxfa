package xfa

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"strings"
)

// Flatten writes a laid-out form as an ordinary PDF: every page drawn with
// the PDF base-14 fonts the layout measured with, field values printed in
// place, and nothing left to fill. Items marked not for print (buttons and
// the like) are left out. Any PDF viewer shows the result.
func Flatten(doc *Doc) ([]byte, error) {
	w := &pdfWriter{fonts: map[string]int{}}
	w.objs = append(w.objs, "") // object 0 is unused
	catalog := w.reserve()
	pages := w.reserve()
	var kids []string
	for _, p := range doc.Pages {
		var c bytes.Buffer
		res := &pageRes{fonts: map[string]string{}, images: map[int]string{}}
		pe := &pdfEmitter{w: w, doc: doc, c: &c, res: res}
		for _, it := range p.Items {
			pe.item(it, p.H, false)
		}
		content := w.stream("", c.Bytes(), true)
		var fr, xr []string
		for name, ref := range res.fonts {
			fr = append(fr, fmt.Sprintf("/%s %s", ref, objRef(w.font(name))))
		}
		for n, name := range res.images {
			xr = append(xr, fmt.Sprintf("/%s %s", name, objRef(n)))
		}
		resStr := "<< /ProcSet [/PDF /Text /ImageC] /Font << " + strings.Join(sorted(fr), " ") + " >>"
		if len(xr) > 0 {
			resStr += " /XObject << " + strings.Join(sorted(xr), " ") + " >>"
		}
		resStr += " >>"
		pg := w.add(fmt.Sprintf("<< /Type /Page /Parent %s /MediaBox [0 0 %s %s] /Resources %s /Contents %s >>",
			objRef(pages), num(p.W), num(p.H), resStr, objRef(content)))
		kids = append(kids, objRef(pg))
	}
	w.set(pages, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids)))
	w.set(catalog, fmt.Sprintf("<< /Type /Catalog /Pages %s >>", objRef(pages)))
	return w.bytes(catalog), nil
}

func sorted(s []string) []string {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	return s
}

type pdfWriter struct {
	objs  []string // object bodies by number; streams are kept in streams
	strm  map[int][]byte
	fonts map[string]int
}

func (w *pdfWriter) reserve() int {
	w.objs = append(w.objs, "")
	return len(w.objs) - 1
}

func (w *pdfWriter) set(n int, body string) { w.objs[n] = body }

func (w *pdfWriter) add(body string) int {
	w.objs = append(w.objs, body)
	return len(w.objs) - 1
}

// stream adds a stream object; dict holds its other entries.
func (w *pdfWriter) stream(dict string, data []byte, compress bool) int {
	if compress {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(data)
		zw.Close()
		data = z.Bytes()
		dict += " /Filter /FlateDecode"
	}
	n := w.add(fmt.Sprintf("<< %s /Length %d >>", strings.TrimSpace(dict), len(data)))
	if w.strm == nil {
		w.strm = map[int][]byte{}
	}
	w.strm[n] = data
	return n
}

func (w *pdfWriter) font(base string) int {
	if n, ok := w.fonts[base]; ok {
		return n
	}
	enc := " /Encoding /WinAnsiEncoding"
	if base == "Symbol" || base == "ZapfDingbats" {
		enc = ""
	}
	n := w.add(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s%s >>", base, enc))
	w.fonts[base] = n
	return n
}

func (w *pdfWriter) bytes(root int) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offs := make([]int, len(w.objs))
	for n := 1; n < len(w.objs); n++ {
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\n", n, w.objs[n])
		if s, ok := w.strm[n]; ok {
			b.WriteString("stream\n")
			b.Write(s)
			b.WriteString("\nendstream\n")
		}
		b.WriteString("endobj\n")
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(w.objs))
	for n := 1; n < len(w.objs); n++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[n])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(w.objs), root, x)
	return b.Bytes()
}

func objRef(n int) string { return fmt.Sprintf("%d 0 R", n) }

func num(v float64) string {
	if math.Abs(v) < 0.0005 {
		return "0"
	}
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

type pageRes struct {
	fonts  map[string]string // base font -> resource name
	images map[int]string    // object -> resource name
}

type pdfEmitter struct {
	w   *pdfWriter
	doc *Doc
	c   *bytes.Buffer
	res *pageRes
}

func (pe *pdfEmitter) fontRes(base string) string {
	if r, ok := pe.res.fonts[base]; ok {
		return r
	}
	r := fmt.Sprintf("F%d", len(pe.res.fonts))
	pe.res.fonts[base] = r
	return r
}

func rgb(c string) (float64, float64, float64) {
	if len(c) != 7 || c[0] != '#' {
		return 0, 0, 0
	}
	var r, g, b int
	fmt.Sscanf(c[1:], "%02x%02x%02x", &r, &g, &b)
	return float64(r) / 255, float64(g) / 255, float64(b) / 255
}

func (pe *pdfEmitter) setStroke(e Edge) {
	r, g, b := rgb(e.Color)
	fmt.Fprintf(pe.c, "%s %s %s RG %s w ", num(r), num(g), num(b), num(e.W))
	switch e.Dash {
	case "dashed":
		fmt.Fprintf(pe.c, "[%s %s] 0 d ", num(e.W*3), num(e.W*3))
	case "dotted":
		fmt.Fprintf(pe.c, "[%s %s] 0 d ", num(e.W), num(e.W*2))
	case "dashDot":
		fmt.Fprintf(pe.c, "[%s %s %s %s] 0 d ", num(e.W*3), num(e.W*2), num(e.W), num(e.W*2))
	case "dashDotDot":
		fmt.Fprintf(pe.c, "[%s %s %s %s %s %s] 0 d ", num(e.W*3), num(e.W*2), num(e.W), num(e.W*2), num(e.W), num(e.W*2))
	default:
		pe.c.WriteString("[] 0 d ")
	}
}

func (pe *pdfEmitter) setFill(c string) {
	r, g, b := rgb(c)
	fmt.Fprintf(pe.c, "%s %s %s rg ", num(r), num(g), num(b))
}

// item draws one item; base is the y of the frame's top in PDF space (the
// page height, or 0 inside a group, where y runs down from the origin).
func (pe *pdfEmitter) item(it Item, base float64, inGroup bool) {
	if it.NoPrint {
		return
	}
	Y := func(y float64) float64 { return base - y }
	switch it.T {
	case "rect":
		pe.rect(it, Y)
	case "line":
		if len(it.Edges) > 0 && it.Edges[0].W > 0 {
			pe.setStroke(it.Edges[0])
			fmt.Fprintf(pe.c, "%s %s m %s %s l S\n", num(it.X), num(Y(it.Y)), num(it.X2), num(Y(it.Y2)))
		}
	case "ellipse":
		path := ellipsePath(it.X, Y(it.Y+it.H), it.W, it.H)
		if it.Fill != "" {
			pe.setFill(it.Fill)
			pe.c.WriteString(path + " f\n")
		}
		if len(it.Edges) > 0 && it.Edges[0].W > 0 {
			pe.setStroke(it.Edges[0])
			pe.c.WriteString(path + " S\n")
		}
	case "text":
		pe.lines(it.Lines, it.X, it.Y, Y)
	case "field":
		pe.field(it, Y)
	case "image":
		pe.image(it, Y)
	case "group":
		rad := it.Rot * math.Pi / 180
		cos, sin := math.Cos(rad), math.Sin(rad)
		fmt.Fprintf(pe.c, "q 1 0 0 1 %s %s cm %s %s %s %s 0 0 cm\n", num(it.X), num(Y(it.Y)), num(cos), num(sin), num(-sin), num(cos))
		for _, k := range it.Items {
			pe.item(k, 0, true)
		}
		pe.c.WriteString("Q\n")
	}
}

func (pe *pdfEmitter) rect(it Item, Y func(float64) float64) {
	x, y, w, h := it.X, Y(it.Y+it.H), it.W, it.H
	path := fmt.Sprintf("%s %s %s %s re", num(x), num(y), num(w), num(h))
	if it.Radius > 0 {
		path = roundRectPath(x, y, w, h, min(it.Radius, w/2, h/2))
	}
	if it.Fill != "" {
		pe.setFill(it.Fill)
		pe.c.WriteString(path + " f\n")
	}
	switch len(it.Edges) {
	case 1:
		if it.Edges[0].W > 0 {
			pe.setStroke(it.Edges[0])
			pe.c.WriteString(path + " S\n")
		}
	case 4:
		pts := [][4]float64{{x, y + h, x + w, y + h}, {x + w, y + h, x + w, y}, {x + w, y, x, y}, {x, y, x, y + h}}
		for i, e := range it.Edges {
			if e.W <= 0 {
				continue
			}
			pe.setStroke(e)
			p := pts[i]
			fmt.Fprintf(pe.c, "%s %s m %s %s l S\n", num(p[0]), num(p[1]), num(p[2]), num(p[3]))
		}
	}
}

func roundRectPath(x, y, w, h, r float64) string {
	k := r * 0.5523
	return fmt.Sprintf("%s %s m %s %s l %s %s %s %s %s %s c %s %s l %s %s %s %s %s %s c %s %s l %s %s %s %s %s %s c %s %s l %s %s %s %s %s %s c h",
		num(x+r), num(y),
		num(x+w-r), num(y), num(x+w-r+k), num(y), num(x+w), num(y+r-k), num(x+w), num(y+r),
		num(x+w), num(y+h-r), num(x+w), num(y+h-r+k), num(x+w-r+k), num(y+h), num(x+w-r), num(y+h),
		num(x+r), num(y+h), num(x+r-k), num(y+h), num(x), num(y+h-r+k), num(x), num(y+h-r),
		num(x), num(y+r), num(x), num(y+r-k), num(x+r-k), num(y), num(x+r), num(y))
}

func ellipsePath(x, y, w, h float64) string {
	rx, ry := w/2, h/2
	cx, cy := x+rx, y+ry
	kx, ky := rx*0.5523, ry*0.5523
	return fmt.Sprintf("%s %s m %s %s %s %s %s %s c %s %s %s %s %s %s c %s %s %s %s %s %s c %s %s %s %s %s %s c h",
		num(cx+rx), num(cy),
		num(cx+rx), num(cy+ky), num(cx+kx), num(cy+ry), num(cx), num(cy+ry),
		num(cx-kx), num(cy+ry), num(cx-rx), num(cy+ky), num(cx-rx), num(cy),
		num(cx-rx), num(cy-ky), num(cx-kx), num(cy-ry), num(cx), num(cy-ry),
		num(cx+kx), num(cy-ry), num(cx+rx), num(cy-ky), num(cx+rx), num(cy))
}

func (pe *pdfEmitter) lines(lines []TextLine, ox, oy float64, Y func(float64) float64) {
	for _, l := range lines {
		for _, r := range l.Runs {
			if r.Text == "" || r.Font < 0 || r.Font >= len(pe.doc.Fonts) {
				continue
			}
			f := pe.doc.Fonts[r.Font]
			fc := faceFor(f)
			x := ox + l.X + r.X
			y := Y(oy + l.Y)
			pe.setFill(f.Color)
			fmt.Fprintf(pe.c, "BT /%s %s Tf %s Tz %s %s Td %s Tj ET\n", pe.fontRes(fc.base), num(f.Size), num(fc.scale*100), num(x), num(y), pdfString(r.Text))
			if f.Underline || f.Strike {
				w := r.W
				if w == 0 {
					w = fc.width(r.Text)
				}
				r2, g, b := rgb(f.Color)
				uy := y - f.Size*0.12
				if f.Strike {
					uy = y + f.Size*0.28
				}
				fmt.Fprintf(pe.c, "%s %s %s RG %s w [] 0 d %s %s m %s %s l S\n", num(r2), num(g), num(b), num(f.Size*0.06), num(x), num(uy), num(x+w), num(uy))
			}
		}
	}
}

func pdfString(s string) string {
	var b strings.Builder
	b.WriteByte('(')
	for _, r := range s {
		c := winAnsi(r)
		switch c {
		case '(', ')', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			if c < 32 || c > 126 {
				fmt.Fprintf(&b, "\\%03o", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte(')')
	return b.String()
}

func (pe *pdfEmitter) field(it Item, Y func(float64) float64) {
	f := it.Field
	switch f.Kind {
	case "check", "radio":
		on := f.Value == f.On && f.On != ""
		if !on {
			return
		}
		// marks are drawn as paths, so no viewer needs a symbol font
		s := min(it.W, it.H)
		cx, cy := it.X+it.W/2, Y(it.Y+it.H/2)
		pe.setFill("")
		switch f.Mark {
		case "circle":
			r := s * 0.28
			fmt.Fprintf(pe.c, "%s f\n", ellipsePath(cx-r, cy-r, 2*r, 2*r))
		case "square":
			r := s * 0.28
			fmt.Fprintf(pe.c, "%s %s %s %s re f\n", num(cx-r), num(cy-r), num(2*r), num(2*r))
		case "diamond":
			r := s * 0.34
			fmt.Fprintf(pe.c, "%s %s m %s %s l %s %s l %s %s l h f\n", num(cx), num(cy+r), num(cx+r), num(cy), num(cx), num(cy-r), num(cx-r), num(cy))
		case "star":
			r := s * 0.36
			var b strings.Builder
			for i := 0; i < 10; i++ {
				rr := r
				if i%2 == 1 {
					rr = r * 0.4
				}
				a := math.Pi/2 + float64(i)*math.Pi/5
				op := "l"
				if i == 0 {
					op = "m"
				}
				fmt.Fprintf(&b, "%s %s %s ", num(cx+rr*math.Cos(a)), num(cy+rr*math.Sin(a)), op)
			}
			fmt.Fprintf(pe.c, "%sh f\n", b.String())
		case "cross":
			r := s * 0.3
			fmt.Fprintf(pe.c, "q 0 G %s w 1 J %s %s m %s %s l %s %s m %s %s l S Q\n", num(s*0.12), num(cx-r), num(cy-r), num(cx+r), num(cy+r), num(cx-r), num(cy+r), num(cx+r), num(cy-r))
		default: // check
			fmt.Fprintf(pe.c, "q 0 G %s w 1 J 1 j %s %s m %s %s l %s %s l S Q\n", num(s*0.12),
				num(cx-s*0.3), num(cy+s*0.02), num(cx-s*0.08), num(cy-s*0.24), num(cx+s*0.32), num(cy+s*0.28))
		}
		return
	case "button", "image":
		return
	}
	if len(it.Lines) == 0 {
		return
	}
	fmt.Fprintf(pe.c, "q %s %s %s %s re W n\n", num(it.X-1), num(Y(it.Y+it.H)-1), num(it.W+2), num(it.H+2))
	pe.lines(it.Lines, it.X, it.Y, Y)
	pe.c.WriteString("Q\n")
}

func (pe *pdfEmitter) image(it Item, Y func(float64) float64) {
	n, iw, ih, err := pe.imageObj(it.Src)
	if err != nil || iw == 0 || ih == 0 {
		return
	}
	name, ok := pe.res.images[n]
	if !ok {
		name = fmt.Sprintf("Im%d", len(pe.res.images))
		pe.res.images[n] = name
	}
	// keep the image's proportions inside its box, centred
	sc := min(it.W/float64(iw), it.H/float64(ih))
	w, h := float64(iw)*sc, float64(ih)*sc
	x := it.X + (it.W-w)/2
	y := Y(it.Y+it.H) + (it.H-h)/2
	fmt.Fprintf(pe.c, "q %s 0 0 %s %s %s cm /%s Do Q\n", num(w), num(h), num(x), num(y), name)
}

func (pe *pdfEmitter) imageObj(src string) (int, int, int, error) {
	i := strings.Index(src, ";base64,")
	if i < 0 {
		return 0, 0, 0, fmt.Errorf("not a data URL")
	}
	data, err := base64.StdEncoding.DecodeString(src[i+8:])
	if err != nil {
		return 0, 0, 0, err
	}
	if strings.Contains(src[:i], "jpeg") || strings.Contains(src[:i], "jpg") {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
		if err == nil {
			n := pe.w.stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace %s /BitsPerComponent 8 /Filter /DCTDecode", cfg.Width, cfg.Height, jpegColorSpace(data, "/DeviceRGB")), data, false)
			return n, cfg.Width, cfg.Height, nil
		}
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0, 0, err
	}
	bd := img.Bounds()
	w, h := bd.Dx(), bd.Dy()
	rgbBuf := make([]byte, 0, w*h*3)
	alpha := make([]byte, 0, w*h)
	hasAlpha := false
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if a > 0 && a < 0xffff {
				// un-premultiply
				r, g, b = r*0xffff/a, g*0xffff/a, b*0xffff/a
			}
			rgbBuf = append(rgbBuf, byte(r>>8), byte(g>>8), byte(b>>8))
			alpha = append(alpha, byte(a>>8))
			if a != 0xffff {
				hasAlpha = true
			}
		}
	}
	dict := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8", w, h)
	if hasAlpha {
		sm := pe.w.stream(fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8", w, h), alpha, true)
		dict += " /SMask " + objRef(sm)
	}
	n := pe.w.stream(dict, rgbBuf, true)
	return n, w, h, nil
}

// jpegColorSpace reads the component count from the JPEG's frame header.
func jpegColorSpace(b []byte, def string) string {
	for i := 2; i+9 < len(b); {
		if b[i] != 0xff {
			return def
		}
		m := b[i+1]
		l := int(b[i+2])<<8 | int(b[i+3])
		if m >= 0xc0 && m <= 0xcf && m != 0xc4 && m != 0xc8 && m != 0xcc {
			switch b[i+9] {
			case 1:
				return "/DeviceGray"
			case 4:
				return "/DeviceCMYK"
			}
			return "/DeviceRGB"
		}
		i += 2 + l
	}
	return def
}
