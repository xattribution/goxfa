package xfa

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"hash"
)

// formChecksum is the form packet's checksum attribute for these packets,
// as Acrobat and PDFium compute it: one SHA-1 over a re-serialization of
// the template, then the datasets, in base64. Acrobat uses the form
// packet's saved state (page numbers among it) only while this matches.
//
// The re-serialization follows PDFium's CXFA_SAXReaderHandler
// (xfa_checksum.cpp): a start tag as "<name" plus ` a="v"` for each
// attribute and ">"; an empty element as "<name ...></name>"; an end tag as
// "</name>"; text as written, entities not converted; CDATA, comments and
// processing instructions as "<![CDATA[...]]>", "<!--...-->" and
// "<?target data?>". Text made only of bytes <= 0x20 is dropped when a
// start tag, comment or instruction follows it, and kept before an end tag.
func formChecksum(packets ...[]byte) string {
	h := sha1.New()
	for _, p := range packets {
		saxHash(h, p)
	}
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func saxHash(h hash.Hash, b []byte) {
	var buf []byte
	flush := func(check bool) {
		if len(buf) > 0 && (!check || !allSpace(buf)) {
			h.Write(buf)
		}
		buf = buf[:0]
	}
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	i := 0
	for i < len(b) {
		if b[i] != '<' {
			j := bytes.IndexByte(b[i:], '<')
			if j < 0 {
				buf = append(buf, b[i:]...)
				break
			}
			buf = append(buf, b[i:i+j]...)
			i += j
			continue
		}
		rest := b[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			end := bytes.Index(rest[4:], []byte("-->"))
			if end < 0 {
				i = len(b)
				continue
			}
			flush(true)
			buf = append(append(append(buf, "<!--"...), rest[4:4+end]...), "-->"...)
			flush(false)
			i += 4 + end + 3
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			end := bytes.Index(rest[9:], []byte("]]>"))
			if end < 0 {
				i = len(b)
				continue
			}
			buf = append(buf, rest[:9+end+3]...)
			i += 9 + end + 3
		case bytes.HasPrefix(rest, []byte("<?")):
			end := bytes.Index(rest[2:], []byte("?>"))
			if end < 0 {
				i = len(b)
				continue
			}
			body := rest[2 : 2+end]
			k := 0
			for k < len(body) && !isSpace(body[k]) && body[k] != '?' {
				k++
			}
			target, data := body[:k], body[k:]
			for len(data) > 0 && isSpace(data[0]) {
				data = data[1:]
			}
			flush(true)
			buf = append(append(buf, "<?"...), target...)
			if len(data) > 0 {
				buf = append(append(buf, ' '), data...)
			}
			buf = append(buf, "?>"...)
			flush(false)
			i += 2 + end + 2
		case bytes.HasPrefix(rest, []byte("<!")):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				i = len(b)
				continue
			}
			i += end + 1
		case bytes.HasPrefix(rest, []byte("</")):
			end := bytes.IndexByte(rest, '>')
			if end < 0 {
				i = len(b)
				continue
			}
			buf = append(append(append(buf, "</"...), bytes.TrimRight(rest[2:end], " \t\r\n")...), '>')
			flush(false)
			i += end + 1
		default:
			// a start tag: its name, then its attributes
			k := 1
			for k < len(rest) && !isSpace(rest[k]) && rest[k] != '/' && rest[k] != '>' {
				k++
			}
			name := rest[1:k]
			flush(true)
			buf = append(append(buf, '<'), name...)
			closed := false
			for k < len(rest) {
				for k < len(rest) && isSpace(rest[k]) {
					k++
				}
				if k >= len(rest) {
					break
				}
				if rest[k] == '>' {
					k++
					break
				}
				if rest[k] == '/' {
					closed = true
					k++
					continue
				}
				a := k
				for k < len(rest) && rest[k] != '=' && !isSpace(rest[k]) && rest[k] != '>' && rest[k] != '/' {
					k++
				}
				an := rest[a:k]
				for k < len(rest) && isSpace(rest[k]) {
					k++
				}
				if k >= len(rest) || rest[k] != '=' {
					continue
				}
				k++
				for k < len(rest) && isSpace(rest[k]) {
					k++
				}
				if k >= len(rest) {
					break
				}
				q := rest[k]
				k++
				v := k
				for k < len(rest) && rest[k] != q {
					k++
				}
				av := rest[v:k]
				k++
				buf = append(append(append(append(append(buf, ' '), an...), `="`...), av...), '"')
			}
			if closed {
				buf = append(append(append(buf, "></"...), name...), '>')
			} else {
				buf = append(buf, '>')
			}
			flush(false)
			i += k
		}
	}
	flush(true)
}

func allSpace(b []byte) bool {
	for _, c := range b {
		if c > 0x20 {
			return false
		}
	}
	return true
}
