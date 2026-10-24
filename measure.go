package xfa

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// measure reads an XFA measurement ("12.5mm", "0.5in", "10pt") in points.
// A bare number is inches, XFA's default unit.
func measure(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') && s[i-1] != '.' {
		i--
	}
	v, err := strconv.ParseFloat(s[:i], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(s[i:])) {
	case "mm":
		return v * 72 / 25.4
	case "cm":
		return v * 72 / 2.54
	case "pt":
		return v
	case "px":
		return v * 72 / 96
	case "pc", "picas":
		return v * 12
	case "mp":
		return v / 1000
	}
	return v * 72
}

// attrMeasure is attribute a of n in points, or def when it's missing.
func attrMeasure(n *Node, a string, def float64) float64 {
	if n == nil || !n.HasAttr(a) {
		return def
	}
	return measure(n.Attr(a))
}

// color reads XFA's "r,g,b" (0-255) as "#rrggbb"; "" when it's missing or
// malformed.
func color(s string) string {
	parts := strings.Split(s, ",")
	if len(parts) < 3 {
		return ""
	}
	var c [3]int
	for i := 0; i < 3; i++ {
		v, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return ""
		}
		c[i] = min(255, max(0, v))
	}
	return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])
}

// colorOf is the color of a <color> child of n, or def.
func colorOf(n *Node, def string) string {
	c := n.Child("color")
	if c == nil || !c.HasAttr("value") {
		return def
	}
	if v := color(c.Attr("value")); v != "" {
		return v
	}
	return def
}

func atoiDef(s string, def int) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
