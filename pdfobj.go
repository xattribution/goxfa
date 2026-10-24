package xfa

import (
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Writing PDF objects for an incremental update. pdfcpu holds strings
// decrypted; they're encrypted again here with the key of the object they
// go into, as hex strings.

// rawPDF is PDF syntax written as it is: never encrypted. A signature's
// /Contents and /ByteRange placeholders are written this way.
type rawPDF string

func (r rawPDF) String() string      { return string(r) }
func (r rawPDF) Clone() types.Object { return r }
func (r rawPDF) PDFString() string   { return string(r) }

// pdfValue writes o as PDF syntax for object n, generation g.
func pdfValue(o types.Object, n, g int, c *crypt) (string, error) {
	var b strings.Builder
	if err := writeValue(&b, o, n, g, c, 0); err != nil {
		return "", err
	}
	return b.String(), nil
}

func writeValue(b *strings.Builder, o types.Object, n, g int, c *crypt, depth int) error {
	if depth > 64 {
		return errors.New("xfa: object nested too deeply to write")
	}
	switch v := o.(type) {
	case nil:
		b.WriteString("null")
	case rawPDF:
		b.WriteString(string(v))
	case types.Dict:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("<<")
		for _, k := range keys {
			b.WriteString(" ")
			b.WriteString(types.Name(k).PDFString())
			b.WriteString(" ")
			if err := writeValue(b, v[k], n, g, c, depth+1); err != nil {
				return err
			}
		}
		b.WriteString(" >>")
	case types.Array:
		b.WriteString("[")
		for i, e := range v {
			if i > 0 {
				b.WriteString(" ")
			}
			if err := writeValue(b, e, n, g, c, depth+1); err != nil {
				return err
			}
		}
		b.WriteString("]")
	case types.StringLiteral:
		raw, err := types.Unescape(v.Value())
		if err != nil {
			return err
		}
		return writeString(b, raw, n, g, c)
	case types.HexLiteral:
		raw, err := v.Bytes()
		if err != nil {
			return err
		}
		return writeString(b, raw, n, g, c)
	case types.Name, types.Integer, types.Float, types.Boolean, types.IndirectRef:
		b.WriteString(v.PDFString())
	default:
		return fmt.Errorf("xfa: can't write a %T into an update", o)
	}
	return nil
}

// writeString writes a string's bytes, encrypted when the file encrypts
// strings, always as hex.
func writeString(b *strings.Builder, raw []byte, n, g int, c *crypt) error {
	if c != nil && c.str {
		var err error
		if raw, err = c.seal(raw, n, g, c.aesStr); err != nil {
			return err
		}
	}
	b.WriteString("<" + hex.EncodeToString(raw) + ">")
	return nil
}

// pdfText is a text string: PDFDocEncoding when it's plain ASCII, UTF-16
// with a byte-order mark otherwise.
func pdfText(s string) types.HexLiteral {
	ascii := true
	for _, r := range s {
		if r > 0x7e || (r < 0x20 && r != '\n' && r != '\r' && r != '\t') {
			ascii = false
			break
		}
	}
	if ascii {
		return types.HexLiteral(hex.EncodeToString([]byte(s)))
	}
	u := []byte{0xfe, 0xff}
	for _, r := range s {
		if r > 0xffff {
			hi, lo := 0xd800+((r-0x10000)>>10), 0xdc00+((r-0x10000)&0x3ff)
			u = append(u, byte(hi>>8), byte(hi), byte(lo>>8), byte(lo))
			continue
		}
		u = append(u, byte(r>>8), byte(r))
	}
	return types.HexLiteral(hex.EncodeToString(u))
}
