package xfa

import (
	"context"
	"strings"
	"sync"

	"github.com/pdfcpu/pdfcpu/pkg/font"
	"golang.org/x/text/encoding/charmap"
)

// Fonts. XFA forms name typefaces (Myriad Pro, Arial, Times New Roman) that
// the PDF usually doesn't embed. Layout measures text with the PDF base-14
// font closest to each typeface, scaled horizontally to the typeface's
// width, and the flattened PDF draws with that same font and scale, so
// what was measured is what is drawn. The viewer draws each laid-out line
// with the named typeface and stretches it to the measured width.

// Font is a resolved font: a typeface, size and style.
type Font struct {
	Typeface  string  `json:"typeface"`
	Size      float64 `json:"size"`
	Bold      bool    `json:"bold,omitempty"`
	Italic    bool    `json:"italic,omitempty"`
	Underline bool    `json:"underline,omitempty"`
	Strike    bool    `json:"strike,omitempty"`
	Color     string  `json:"color,omitempty"` // "#rrggbb"; "" is black
	// HScale is the font's horizontal scale (fontHorizontalScale), 1 when 0.
	HScale float64 `json:"hScale,omitempty"`
}

type family int

const (
	helvetica family = iota
	times
	courier
	symbol
	zapf
)

// faceOf maps a typeface name to a base-14 family and a width scale: the
// typeface's average width relative to the base font's.
func faceOf(typeface string) (family, float64) {
	t := strings.ToLower(typeface)
	switch {
	case strings.Contains(t, "courier"), strings.Contains(t, "mono"), strings.Contains(t, "consolas"), strings.Contains(t, "lucida console"):
		return courier, 1
	case strings.Contains(t, "times"), strings.Contains(t, "georgia"), strings.Contains(t, "garamond"),
		strings.Contains(t, "minion"), strings.Contains(t, "book antiqua"), strings.Contains(t, "palatino"),
		strings.Contains(t, "cambria"), strings.Contains(t, "century schoolbook"):
		return times, 1
	case strings.Contains(t, "symbol"):
		return symbol, 1
	case strings.Contains(t, "zapf"), strings.Contains(t, "wingding"), strings.Contains(t, "dingbat"):
		return zapf, 1
	case strings.Contains(t, "narrow"), strings.Contains(t, "condensed"):
		return helvetica, 0.82
	case strings.Contains(t, "myriad"):
		return helvetica, 0.92
	case strings.Contains(t, "calibri"):
		return helvetica, 0.88
	case strings.Contains(t, "verdana"):
		return helvetica, 1.12
	case strings.Contains(t, "tahoma"):
		return helvetica, 0.96
	}
	return helvetica, 1 // Arial, Helvetica, Segoe UI and the rest of the sans faces
}

// baseName is the base-14 font name for a family and style.
func baseName(f family, bold, italic bool) string {
	switch f {
	case symbol:
		return "Symbol"
	case zapf:
		return "ZapfDingbats"
	case times:
		switch {
		case bold && italic:
			return "Times-BoldItalic"
		case bold:
			return "Times-Bold"
		case italic:
			return "Times-Italic"
		}
		return "Times-Roman"
	case courier:
		switch {
		case bold && italic:
			return "Courier-BoldOblique"
		case bold:
			return "Courier-Bold"
		case italic:
			return "Courier-Oblique"
		}
		return "Courier"
	}
	switch {
	case bold && italic:
		return "Helvetica-BoldOblique"
	case bold:
		return "Helvetica-Bold"
	case italic:
		return "Helvetica-Oblique"
	}
	return "Helvetica"
}

// metrics are one base font's widths by WinAnsi code (per 1000 em) and its
// ascender and descender.
type metrics struct {
	w         [256]float64
	asc, desc float64 // per em, descender positive
}

var (
	metricsMu sync.Mutex
	metricsBy = map[string]*metrics{}
)

func metricsFor(name string) *metrics {
	metricsMu.Lock()
	defer metricsMu.Unlock()
	if m := metricsBy[name]; m != nil {
		return m
	}
	m := &metrics{}
	ctx := context.Background()
	for c := 0; c < 256; c++ {
		w, err := font.CharWidth(ctx, name, rune(c))
		if err != nil {
			w = 500
		}
		m.w[c] = float64(w)
	}
	switch {
	case strings.HasPrefix(name, "Times"):
		m.asc, m.desc = 0.683, 0.217
	case strings.HasPrefix(name, "Courier"):
		m.asc, m.desc = 0.629, 0.157
	case name == "Symbol", name == "ZapfDingbats":
		m.asc, m.desc = 0.75, 0.2
	default:
		m.asc, m.desc = 0.718, 0.207
	}
	metricsBy[name] = m
	return m
}

// face is a Font ready for measuring: its base font, width scale and size.
type face struct {
	Font
	base  string
	scale float64
	m     *metrics
}

func faceFor(f Font) *face {
	fam, sc := faceOf(f.Typeface)
	name := baseName(fam, f.Bold, f.Italic)
	if f.Size <= 0 {
		f.Size = 10
	}
	if f.HScale > 0 {
		sc *= f.HScale
	}
	return &face{Font: f, base: name, scale: sc, m: metricsFor(name)}
}

var win1252 = charmap.Windows1252.NewEncoder()

// winAnsi is r in Windows-1252, or '?' when it has no place there.
func winAnsi(r rune) byte {
	if r < 0x80 {
		return byte(r)
	}
	switch r {
	case ' ':
		return 0xa0
	case '‐', '‑', '‒':
		return '-'
	case ' ', ' ', ' ', ' ', ' ':
		return ' '
	}
	b, err := win1252.Bytes([]byte(string(r)))
	if err != nil || len(b) != 1 {
		return '?'
	}
	return b[0]
}

// width of s in points.
func (f *face) width(s string) float64 {
	w := 0.0
	for _, r := range s {
		w += f.m.w[winAnsi(r)]
	}
	return w / 1000 * f.Size * f.scale
}

func (f *face) runeWidth(r rune) float64 {
	return f.m.w[winAnsi(r)] / 1000 * f.Size * f.scale
}

func (f *face) ascent() float64  { return f.m.asc * f.Size }
func (f *face) descent() float64 { return f.m.desc * f.Size }

// lineHeight is the default distance between baselines: Acrobat's 1.15 em
// for the faces forms use.
func (f *face) lineHeight() float64 { return f.Size * 1.15 }
