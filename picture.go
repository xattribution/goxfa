package xfa

import (
	"strconv"
	"strings"
	"time"
)

// Picture clauses, the part forms use: date patterns for display and
// storage, and the num and text patterns left as they are. A clause that
// isn't understood shows the raw value.

// formatPicture formats raw with a picture clause such as
// "date{DD MMM YYYY}", "date.short{}" or "date{YYYYMMDD}|date{MM/DD/YYYY}"
// (alternatives: the first that works is used).
func formatPicture(pic, raw, ui string) (string, bool) {
	for _, alt := range strings.Split(pic, "|") {
		alt = strings.TrimSpace(alt)
		cat, body := splitPicture(alt)
		switch {
		case strings.HasPrefix(cat, "date"):
			d, ok := parseDate(raw)
			if !ok {
				continue
			}
			pattern := body
			switch {
			case strings.HasSuffix(cat, ".short"), pattern == "" && cat == "date":
				pattern = "M/D/YY"
			case strings.HasSuffix(cat, ".medium"), strings.HasSuffix(cat, ".default"):
				pattern = "MMM D, YYYY"
			case strings.HasSuffix(cat, ".long"):
				pattern = "MMMM D, YYYY"
			case strings.HasSuffix(cat, ".full"):
				pattern = "EEEE, MMMM D, YYYY"
			}
			if pattern == "" {
				pattern = "M/D/YY"
			}
			return formatDate(pattern, d), true
		case cat == "" && strings.ContainsAny(alt, "YMD") && !strings.ContainsAny(alt, "9Zz"):
			if d, ok := parseDate(raw); ok {
				return formatDate(alt, d), true
			}
		}
	}
	return "", false
}

// splitPicture splits "date.short{MM/DD}" into "date.short" and "MM/DD".
func splitPicture(p string) (string, string) {
	i := strings.IndexByte(p, '{')
	if i < 0 || !strings.HasSuffix(p, "}") {
		return "", p
	}
	return strings.ToLower(strings.TrimSpace(p[:i])), p[i+1 : len(p)-1]
}

// parseDate reads the stored forms dates take: YYYY-MM-DD, YYYYMMDD and
// MM/DD/YYYY.
func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, l := range []string{"2006-01-02", "20060102", "01/02/2006", "1/2/2006", "2006-01-02T15:04:05", "02 Jan 2006", "2 Jan 2006"} {
		if d, err := time.Parse(l, s); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

// formatDate applies an XFA date pattern: D DD J JJJ M MM MMM MMMM E EEE
// EEEE YY YYYY, with literal text in single quotes.
func formatDate(p string, d time.Time) string {
	var b strings.Builder
	rs := []rune(p)
	for i := 0; i < len(rs); {
		c := rs[i]
		if c == '\'' {
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j == i+1 && j < len(rs) {
				b.WriteRune('\'') // '' is a quote
			} else {
				b.WriteString(string(rs[i+1 : min(j, len(rs))]))
			}
			i = j + 1
			continue
		}
		n := 1
		for i+n < len(rs) && rs[i+n] == c {
			n++
		}
		switch c {
		case 'D':
			if n == 1 {
				b.WriteString(strconv.Itoa(d.Day()))
			} else {
				b.WriteString(pad(d.Day(), 2))
			}
		case 'J':
			if n >= 3 {
				b.WriteString(pad(d.YearDay(), 3))
			} else {
				b.WriteString(strconv.Itoa(d.YearDay()))
			}
		case 'M':
			switch n {
			case 1:
				b.WriteString(strconv.Itoa(int(d.Month())))
			case 2:
				b.WriteString(pad(int(d.Month()), 2))
			case 3:
				b.WriteString(d.Month().String()[:3])
			default:
				b.WriteString(d.Month().String())
			}
		case 'E':
			switch {
			case n >= 4:
				b.WriteString(d.Weekday().String())
			case n == 3:
				b.WriteString(d.Weekday().String()[:3])
			default:
				b.WriteString(strconv.Itoa(int(d.Weekday()) + 1))
			}
		case 'Y':
			if n == 2 {
				b.WriteString(pad(d.Year()%100, 2))
			} else {
				b.WriteString(pad(d.Year(), 4))
			}
		default:
			b.WriteString(strings.Repeat(string(c), n))
		}
		i += n
	}
	return b.String()
}

func pad(v, n int) string {
	s := strconv.Itoa(v)
	for len(s) < n {
		s = "0" + s
	}
	return s
}

// parsePicture reads s with a date picture clause (or a bare pattern):
// the inverse of formatPicture, for dates typed the way they're shown.
func parsePicture(pic, s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, alt := range strings.Split(pic, "|") {
		cat, body := splitPicture(strings.TrimSpace(alt))
		if cat != "" && cat != "date" {
			continue
		}
		layout, ok := goLayout(body)
		if !ok {
			continue
		}
		if d, err := time.Parse(layout, s); err == nil {
			return d, true
		}
	}
	return time.Time{}, false
}

// goLayout turns an XFA date pattern into a Go time layout, when it can.
func goLayout(p string) (string, bool) {
	var b strings.Builder
	rs := []rune(p)
	for i := 0; i < len(rs); {
		c := rs[i]
		if c == '\'' {
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			lit := string(rs[i+1 : min(j, len(rs))])
			if strings.ContainsAny(lit, "0123456789JjMmPpTtZz_") {
				return "", false // would read as a layout element
			}
			b.WriteString(lit)
			i = j + 1
			continue
		}
		n := 1
		for i+n < len(rs) && rs[i+n] == c {
			n++
		}
		var el string
		switch {
		case c == 'D' && n == 1:
			el = "2"
		case c == 'D' && n == 2:
			el = "02"
		case c == 'M' && n == 1:
			el = "1"
		case c == 'M' && n == 2:
			el = "01"
		case c == 'M' && n == 3:
			el = "Jan"
		case c == 'M' && n == 4:
			el = "January"
		case c == 'Y' && n == 2:
			el = "06"
		case c == 'Y' && n == 4:
			el = "2006"
		case c == 'E' && n == 3:
			el = "Mon"
		case c == 'E' && n == 4:
			el = "Monday"
		case strings.ContainsRune("DJMEYGwWAahHkKMSFZz", c):
			return "", false
		default:
			el = strings.Repeat(string(c), n)
			if strings.ContainsAny(el, "0123456789") {
				return "", false
			}
		}
		b.WriteString(el)
		i += n
	}
	return b.String(), b.Len() > 0
}
