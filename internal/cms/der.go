package cms

import (
	"bytes"
	"encoding/asn1"
	"math/big"
	"sort"
	"time"
)

// A small DER writer: enough for CMS, built by hand so every byte is
// where the standards put it.

func tlv(tag byte, content []byte) []byte {
	out := []byte{tag}
	n := len(content)
	switch {
	case n < 0x80:
		out = append(out, byte(n))
	case n < 0x100:
		out = append(out, 0x81, byte(n))
	case n < 0x10000:
		out = append(out, 0x82, byte(n>>8), byte(n))
	case n < 0x1000000:
		out = append(out, 0x83, byte(n>>16), byte(n>>8), byte(n))
	default:
		out = append(out, 0x84, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return append(out, content...)
}

func seq(parts ...[]byte) []byte { return tlv(0x30, bytes.Join(parts, nil)) }

// setOf is a DER SET OF: its elements sorted by their encodings.
func setOf(parts ...[]byte) []byte {
	s := append([][]byte(nil), parts...)
	sort.Slice(s, func(i, j int) bool { return bytes.Compare(s[i], s[j]) < 0 })
	return tlv(0x31, bytes.Join(s, nil))
}

// explicit is a context-specific constructed tag around content.
func explicit(n int, content []byte) []byte { return tlv(0xa0|byte(n), content) }

// implicit replaces an encoding's tag with a context-specific constructed
// one.
func implicit(n int, enc []byte) []byte {
	out := append([]byte(nil), enc...)
	out[0] = 0xa0 | byte(n)
	return out
}

func octets(b []byte) []byte { return tlv(0x04, b) }

func oid(o asn1.ObjectIdentifier) []byte {
	b, _ := asn1.Marshal(o)
	return b
}

func integer(v *big.Int) []byte {
	b, _ := asn1.Marshal(v)
	return b
}

func small(v int) []byte { return integer(big.NewInt(int64(v))) }

func null() []byte { return []byte{0x05, 0x00} }

func boolean(v bool) []byte {
	if v {
		return []byte{0x01, 0x01, 0xff}
	}
	return []byte{0x01, 0x01, 0x00}
}

// utcTime is a signing time: UTCTime through 2049, then GeneralizedTime.
func utcTime(t time.Time) []byte {
	t = t.UTC()
	if t.Year() < 2050 {
		return tlv(0x17, []byte(t.Format("060102150405Z")))
	}
	return tlv(0x18, []byte(t.Format("20060102150405Z")))
}

// alg is an AlgorithmIdentifier, with NULL parameters when asked.
func alg(o asn1.ObjectIdentifier, withNull bool) []byte {
	if withNull {
		return seq(oid(o), null())
	}
	return seq(oid(o))
}
