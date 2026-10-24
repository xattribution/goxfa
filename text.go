package xfa

import (
	"strconv"
	"strings"
	"unicode"
)

// Text: plain and rich text (XFA's exData, a subset of XHTML and CSS),
// broken into lines for a width.

// span is a run of text in one font.
type span struct {
	text string
	font Font
}

// paragraph is a block of spans with its formatting.
type paragraph struct {
	spans []span
	p     para
}

// TextLine is one laid-out line: its baseline position and its runs.
type TextLine struct {
	X    float64   `json:"x"` // left edge, relative to the item
	Y    float64   `json:"y"` // baseline, relative to the item's top
	W    float64   `json:"w"` // width as measured
	Runs []TextRun `json:"runs"`
}

// TextRun is text in one font on a line.
type TextRun struct {
	X    float64 `json:"x"` // relative to the line's X
	Text string  `json:"text"`
	Font int     `json:"font"` // index into Doc.Fonts
	W    float64 `json:"w"`
}

// plainParagraphs splits plain text into paragraphs at line breaks.
func plainParagraphs(s string, f Font, p para) []paragraph {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.NewReplacer("\u2029", "\n", "\u2028", "\n").Replace(s)
	var out []paragraph
	for _, line := range strings.Split(s, "\n") {
		out = append(out, paragraph{spans: []span{{text: line, font: f}}, p: p})
	}
	return out
}

// embedFunc gives the text for a span that embeds another object
// (xfa:embed="#id" or a SOM expression): Designer's floating fields.
type embedFunc func(ref string) string

// Page-number placeholders: embedded floating fields whose scripts write the
// page number or count are filled in once the pages are known.
const (
	pagePlaceholder  = "\ue000"
	pagesPlaceholder = "\ue001"
)

// richParagraphs reads an exData XHTML body.
func richParagraphs(ex *Node, f Font, p para, emb embedFunc) []paragraph {
	body := ex
	for _, k := range ex.Elems() {
		if k.Local == "body" || k.Local == "html" {
			body = k
			if b := k.Child("body"); b != nil {
				body = b
			}
			break
		}
	}
	var out []paragraph
	cur := paragraph{p: p}
	flush := func(force bool) {
		if force || len(cur.spans) > 0 {
			out = append(out, cur)
		}
		cur = paragraph{p: cur.p}
	}
	var walk func(n *Node, f Font, block bool)
	walk = func(n *Node, f Font, block bool) {
		for _, k := range n.Kids {
			switch k.Kind {
			case TextNode:
				t := collapseSpace(k.Text)
				if t != "" {
					cur.spans = append(cur.spans, span{text: t, font: f})
				}
			case ElemNode:
				kf := f
				pp := cur.p
				style := parseStyle(k.Attr("style"))
				switch strings.ToLower(k.Local) {
				case "b", "strong":
					kf.Bold = true
				case "i", "em":
					kf.Italic = true
				case "u":
					kf.Underline = true
				case "br":
					flush(true)
					continue
				}
				kf = applyStyle(kf, style)
				if ref := k.Attr("embed"); ref != "" {
					if emb != nil {
						if t := emb(ref); t != "" {
							cur.spans = append(cur.spans, span{text: t, font: kf})
						}
					}
					continue
				}
				if style["xfa-spacerun"] == "yes" {
					cur.spans = append(cur.spans, span{text: strings.ReplaceAll(k.TextContent(), " ", " "), font: kf})
					continue
				}
				switch strings.ToLower(k.Local) {
				case "p", "div", "li", "h1", "h2", "h3", "h4", "h5", "h6":
					flush(false)
					pp = applyParaStyle(p, style)
					cur.p = pp
					walk(k, kf, true)
					flush(true)
					cur.p = p
				default:
					walk(k, kf, block)
				}
			}
		}
	}
	walk(body, applyStyle(f, parseStyle(body.Attr("style"))), true)
	flush(false)
	// trim leading spaces at each paragraph's start
	for i := range out {
		if len(out[i].spans) > 0 {
			out[i].spans[0].text = strings.TrimLeft(out[i].spans[0].text, " ")
		}
	}
	return out
}

// collapseSpace turns runs of white space into one space, as HTML does.
func collapseSpace(s string) string {
	var b strings.Builder
	sp := false
	for _, r := range s {
		if r == ' ' {
			b.WriteRune(' ')
			sp = false
			continue
		}
		if unicode.IsSpace(r) {
			if !sp {
				b.WriteByte(' ')
			}
			sp = true
			continue
		}
		sp = false
		b.WriteRune(r)
	}
	return b.String()
}

func parseStyle(s string) map[string]string {
	m := map[string]string{}
	for _, decl := range strings.Split(s, ";") {
		k, v, ok := strings.Cut(decl, ":")
		if !ok {
			continue
		}
		m[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return m
}

func applyStyle(f Font, st map[string]string) Font {
	if v := st["font-family"]; v != "" {
		f.Typeface = strings.Trim(strings.Split(v, ",")[0], `'" `)
	}
	if v := st["font-size"]; v != "" {
		if sz := measure(v); sz > 0 {
			f.Size = sz
		}
	}
	if v := st["font-weight"]; v != "" {
		f.Bold = v == "bold" || v == "bolder" || v == "700" || v == "800" || v == "900"
	}
	if v := st["font-style"]; v != "" {
		f.Italic = v == "italic" || v == "oblique"
	}
	if v := st["text-decoration"]; v != "" {
		f.Underline = strings.Contains(v, "underline")
		f.Strike = strings.Contains(v, "line-through")
	}
	if v := st["xfa-font-horizontal-scale"]; strings.HasSuffix(v, "%") {
		if pc, err := strconv.ParseFloat(strings.TrimSuffix(v, "%"), 64); err == nil && pc > 10 && pc < 1000 {
			f.HScale = pc / 100
			if pc == 100 {
				f.HScale = 0
			}
		}
	}
	if v := st["color"]; strings.HasPrefix(v, "#") && len(v) == 7 {
		f.Color = strings.ToLower(v)
	}
	if v := st["font"]; v != "" {
		// shorthand: [style] [weight] size family
		for _, part := range strings.Fields(v) {
			switch {
			case part == "bold":
				f.Bold = true
			case part == "italic":
				f.Italic = true
			case strings.HasSuffix(part, "pt") || strings.HasSuffix(part, "px"):
				if sz := measure(part); sz > 0 {
					f.Size = sz
				}
			}
		}
	}
	return f
}

func applyParaStyle(p para, st map[string]string) para {
	switch st["text-align"] {
	case "left", "center", "right", "justify":
		p.hAlign = st["text-align"]
	}
	if v, ok := st["margin-top"]; ok {
		p.spaceAbove = measure(v)
	}
	if v, ok := st["margin-bottom"]; ok {
		p.spaceBelow = measure(v)
	}
	if v, ok := st["margin-left"]; ok {
		p.marginLeft = measure(v)
	}
	if v, ok := st["margin-right"]; ok {
		p.marginRight = measure(v)
	}
	if v, ok := st["text-indent"]; ok {
		p.textIndent = measure(v)
	}
	if v, ok := st["line-height"]; ok {
		p.lineHeight = measure(v)
	}
	return p
}

// fontTable collects the fonts a layout uses.
type fontTable struct {
	list []Font
	idx  map[Font]int
}

func (t *fontTable) id(f Font) int {
	if t.idx == nil {
		t.idx = map[Font]int{}
	}
	if i, ok := t.idx[f]; ok {
		return i
	}
	t.idx[f] = len(t.list)
	t.list = append(t.list, f)
	return t.idx[f]
}

// textBlock is laid-out text: its lines and total size.
type textBlock struct {
	lines []TextLine
	w, h  float64
}

// layoutText breaks paragraphs into lines no wider than width (no
// breaking when wrap is false or width is 0), and positions them for the
// paragraph's alignment within width. Baselines are relative to the
// block's top.
func layoutText(ps []paragraph, width float64, wrap bool, fonts *fontTable) textBlock {
	var tb textBlock
	y := 0.0
	for pi, p := range ps {
		y += p.p.spaceAbove
		ml, indent := p.p.marginLeft, p.p.textIndent
		if indent < 0 && ml < -indent {
			// Acrobat keeps a hanging first line inside the box: the
			// other lines move right instead
			ml = -indent
		}
		avail := width - ml - p.p.marginRight
		lines := breakLines(p, avail, max(indent, 0), wrap && width > 0)
		if len(lines) == 0 {
			// an empty paragraph still takes a line
			f := defaultFont
			if len(p.spans) > 0 {
				f = p.spans[0].font
			}
			lines = []rawLine{{h: lineHeightFor(faceFor(f), p.p), asc: faceFor(f).ascent()}}
		}
		for li, l := range lines {
			x := ml
			if li == 0 {
				x += indent
			}
			lw := l.w
			switch p.p.hAlign {
			case "center":
				if width > 0 {
					x += (avail - lw) / 2
				}
			case "right":
				if width > 0 {
					x += avail - lw
				}
			}
			line := TextLine{X: round2(x), Y: round2(y + l.asc), W: round2(lw)}
			for _, r := range l.runs {
				line.Runs = append(line.Runs, TextRun{X: round2(r.x), Text: r.text, Font: fonts.id(r.font), W: round2(r.w)})
			}
			tb.lines = append(tb.lines, line)
			tb.w = max(tb.w, x+lw)
			y += l.h
		}
		y += p.p.spaceBelow
		_ = pi
	}
	tb.h = y
	return tb
}

type rawRun struct {
	text string
	font Font
	x, w float64
}

type rawLine struct {
	runs   []rawRun
	w      float64
	h, asc float64
}

func lineHeightFor(fc *face, p para) float64 {
	if p.lineHeight > 0 {
		return p.lineHeight
	}
	return fc.lineHeight()
}

// breakLines wraps one paragraph greedily at spaces (and inside words that
// are wider than the line).
func breakLines(p paragraph, width, indent float64, wrap bool) []rawLine {
	type word struct {
		text  string
		font  Font
		space bool
	}
	var words []word
	for _, s := range p.spans {
		cur := ""
		for _, r := range s.text {
			if r == ' ' || r == '\t' {
				if cur != "" {
					words = append(words, word{text: cur, font: s.font})
					cur = ""
				}
				words = append(words, word{text: string(r), font: s.font, space: true})
				continue
			}
			cur += string(r)
			if r == '-' && len(cur) > 1 {
				// a line may break after a hyphen inside a word
				words = append(words, word{text: cur, font: s.font})
				cur = ""
			}
		}
		if cur != "" {
			words = append(words, word{text: cur, font: s.font})
		}
	}
	if len(words) == 0 {
		return nil
	}
	var lines []rawLine
	var cur rawLine
	x := 0.0
	addRun := func(t string, f Font, w float64) {
		if n := len(cur.runs); n > 0 && cur.runs[n-1].font == f {
			cur.runs[n-1].text += t
			cur.runs[n-1].w += w
		} else {
			cur.runs = append(cur.runs, rawRun{text: t, font: f, x: x, w: w})
		}
		x += w
		fc := faceFor(f)
		cur.h = max(cur.h, lineHeightFor(fc, p.p))
		cur.asc = max(cur.asc, fc.ascent())
	}
	endLine := func() {
		// drop trailing spaces from the measured width
		for len(cur.runs) > 0 {
			last := &cur.runs[len(cur.runs)-1]
			t := strings.TrimRight(last.text, " \t")
			if t == last.text {
				break
			}
			fc := faceFor(last.font)
			last.w -= fc.width(last.text[len(t):])
			last.text = t
			if last.text == "" {
				cur.runs = cur.runs[:len(cur.runs)-1]
				continue
			}
			break
		}
		cur.w = 0
		if n := len(cur.runs); n > 0 {
			cur.w = cur.runs[n-1].x + cur.runs[n-1].w
		}
		if cur.h == 0 {
			fc := faceFor(defaultFont)
			if len(p.spans) > 0 {
				fc = faceFor(p.spans[0].font)
			}
			cur.h, cur.asc = lineHeightFor(fc, p.p), fc.ascent()
		}
		lines = append(lines, cur)
		cur = rawLine{}
		x = 0
	}
	// Designer sizes boxes to Acrobat's measure of the text, which can be a
	// hair under ours: give the line that much room.
	slack := max(0.1, width*0.002)
	fit := width - indent + slack // the first line gives up its indent
	for _, wd := range words {
		if len(lines) > 0 {
			fit = width + slack
		}
		fc := faceFor(wd.font)
		w := fc.width(wd.text)
		if wd.space {
			if x == 0 && len(lines) > 0 && wrap {
				continue // no leading space on a wrapped line
			}
			if wd.text == "\t" {
				w = fc.width("    ")
			}
			addRun(" ", wd.font, w)
			continue
		}
		if wrap && x > 0 && x+w > fit {
			endLine()
		}
		if wrap && w > fit && width > 0 {
			// a word wider than the line: break it by characters
			part := ""
			pw := 0.0
			for _, r := range wd.text {
				rw := fc.runeWidth(r)
				if x+pw+rw > fit && (part != "" || x > 0) {
					if part != "" {
						addRun(part, wd.font, pw)
					}
					endLine()
					part, pw = "", 0
				}
				part += string(r)
				pw += rw
			}
			if part != "" {
				addRun(part, wd.font, pw)
			}
			continue
		}
		addRun(wd.text, wd.font, w)
	}
	if len(cur.runs) > 0 || len(lines) == 0 {
		endLine()
	}
	return lines
}

// valueParagraphs reads a container's value as paragraphs: exData rich
// text, or plain text from the value's text element.
func valueParagraphs(v *Node, f Font, p para, emb embedFunc) []paragraph {
	if v == nil {
		return nil
	}
	for _, k := range v.Elems() {
		switch k.Local {
		case "exData":
			ct := k.Attr("contentType")
			if ct == "text/html" || ct == "text/xml" || ct == "" {
				return richParagraphs(k, f, p, emb)
			}
			return plainParagraphs(k.TextContent(), f, p)
		case "text", "integer", "decimal", "float", "date", "time", "dateTime", "boolean":
			return plainParagraphs(k.TextContent(), f, p)
		}
	}
	return nil
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
