package xfa

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Reading the XFA packets out of a PDF, and writing changed packets back
// as an incremental update: new revisions of the changed streams appended
// after the original bytes, the way Acrobat saves, so earlier signatures
// and Reader usage rights still cover the bytes they signed.

// ErrNoXFA means the PDF has no XFA form.
var ErrNoXFA = errors.New("xfa: the PDF has no XFA form")

const maxPacket = 64 << 20

type packet struct {
	name string
	ref  types.IndirectRef
	raw  []byte // decoded stream content
}

type pdfFile struct {
	data    []byte
	x       *model.XRefTable
	packets []packet // in /XFA order
	whole   bool     // the whole XDP is one stream
	perms   bool     // the catalog has /Perms (Reader usage rights)
	sigs    bool     // the AcroForm says the file is signed
}

func readPDF(data []byte) (pf *pdfFile, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("xfa: couldn't read the PDF (%v)", p)
		}
	}()
	conf := model.NewDefaultConfiguration()
	conf.Offline = true
	conf.Cmd = model.LISTFORMFIELDS
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := api.ReadContext(context.Background(), bytes.NewReader(data), conf)
	if err != nil {
		return nil, fmt.Errorf("xfa: couldn't read the PDF: %w", err)
	}
	x := ctx.XRefTable
	root, err := x.Catalog()
	if err != nil {
		return nil, err
	}
	pf = &pdfFile{data: data, x: x}
	_, pf.perms = root.Find("Perms")
	ao, ok := root.Find("AcroForm")
	if !ok {
		return nil, ErrNoXFA
	}
	acro, err := x.DereferenceDict(ao)
	if err != nil || acro == nil {
		return nil, ErrNoXFA
	}
	if sf := acro.IntEntry("SigFlags"); sf != nil && *sf&1 != 0 {
		pf.sigs = true
	}
	xo, ok := acro.Find("XFA")
	if !ok {
		return nil, ErrNoXFA
	}
	obj, err := x.Dereference(xo)
	if err != nil {
		return nil, err
	}
	switch t := obj.(type) {
	case types.Array:
		for i := 0; i+1 < len(t); i += 2 {
			name, _ := x.DereferenceText(t[i])
			ir, ok := t[i+1].(types.IndirectRef)
			if !ok {
				continue
			}
			raw, err := streamContent(x, ir)
			if err != nil {
				return nil, fmt.Errorf("xfa: the %s packet: %w", name, err)
			}
			pf.packets = append(pf.packets, packet{name: name, ref: ir, raw: raw})
		}
	case types.StreamDict:
		ir, ok := xo.(types.IndirectRef)
		if !ok {
			return nil, ErrNoXFA
		}
		raw, err := streamContent(x, ir)
		if err != nil {
			return nil, err
		}
		pf.whole = true
		pf.packets = append(pf.packets, packet{name: "xdp", ref: ir, raw: raw})
	default:
		return nil, ErrNoXFA
	}
	return pf, nil
}

func streamContent(x *model.XRefTable, ir types.IndirectRef) ([]byte, error) {
	sd, _, err := x.DereferenceStreamDict(ir)
	if err != nil || sd == nil {
		return nil, errors.New("not a stream")
	}
	if err := sd.Decode(); err != nil {
		return nil, err
	}
	if len(sd.Content) > maxPacket {
		return nil, errors.New("larger than 64 MB")
	}
	return sd.Content, nil
}

// ---- incremental update ----

type newStream struct {
	gen     int
	content []byte // unencoded
}

// appendUpdate writes data plus new revisions of the given streams.
func appendUpdate(data []byte, x *model.XRefTable, streams map[int]newStream) ([]byte, error) {
	c, err := newCrypt(x)
	if err != nil {
		return nil, err
	}
	prev, err := lastStartXref(data)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Grow(len(data) + 1<<16)
	out.Write(data)
	if n := len(data); n > 0 && data[n-1] != '\n' && data[n-1] != '\r' {
		out.WriteByte('\n')
	}
	var nums []int
	for n := range streams {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	offs, gens := map[int]int{}, map[int]int{}
	for _, n := range nums {
		st := streams[n]
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(st.content)
		if err := zw.Close(); err != nil {
			return nil, err
		}
		content := z.Bytes()
		if c != nil && c.stm {
			if content, err = c.seal(content, n, st.gen, c.aesStm); err != nil {
				return nil, err
			}
		}
		offs[n], gens[n] = out.Len(), st.gen
		fmt.Fprintf(&out, "%d %d obj\n<< /Filter /FlateDecode /Length %d >>\nstream\n", n, st.gen, len(content))
		out.Write(content)
		out.WriteString("\nendstream\nendobj\n")
	}
	trailer := "/Root " + x.Root.PDFString()
	if x.Info != nil {
		trailer += " /Info " + x.Info.PDFString()
	}
	if len(x.ID) > 0 {
		trailer += " /ID " + x.ID.PDFString()
	}
	if x.Encrypt != nil {
		trailer += " /Encrypt " + x.Encrypt.PDFString()
	}
	trailer += fmt.Sprintf(" /Prev %d", prev)
	size := 0
	if x.Size != nil {
		size = *x.Size
	}
	size = max(size, x.MaxObjNr+1)
	if xrefStreamAt(data, prev) {
		xn := size
		size++
		nums = append(nums, xn)
		offs[xn] = out.Len()
		var e bytes.Buffer
		for _, n := range nums {
			e.WriteByte(1)
			binary.Write(&e, binary.BigEndian, uint32(offs[n]))
			binary.Write(&e, binary.BigEndian, uint16(gens[n]))
		}
		fmt.Fprintf(&out, "%d 0 obj\n<< /Type /XRef /Size %d /Index [%s] /W [1 4 2] %s /Length %d >>\nstream\n", xn, size, xrefIndex(nums), trailer, e.Len())
		out.Write(e.Bytes())
		fmt.Fprintf(&out, "\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", offs[xn])
		return out.Bytes(), nil
	}
	start := out.Len()
	out.WriteString("xref\n")
	for i := 0; i < len(nums); {
		j := i + 1
		for j < len(nums) && nums[j] == nums[j-1]+1 {
			j++
		}
		fmt.Fprintf(&out, "%d %d\n", nums[i], j-i)
		for _, n := range nums[i:j] {
			fmt.Fprintf(&out, "%010d %05d n \n", offs[n], gens[n])
		}
		i = j
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d %s >>\nstartxref\n%d\n%%%%EOF\n", size, trailer, start)
	return out.Bytes(), nil
}

func xrefIndex(nums []int) string {
	var parts []string
	for i := 0; i < len(nums); {
		j := i + 1
		for j < len(nums) && nums[j] == nums[j-1]+1 {
			j++
		}
		parts = append(parts, fmt.Sprintf("%d %d", nums[i], j-i))
		i = j
	}
	return strings.Join(parts, " ")
}

var reStartXref = regexp.MustCompile(`startxref\s+(\d+)`)

func lastStartXref(data []byte) (int, error) {
	tail := data[max(0, len(data)-2048):]
	m := reStartXref.FindAllSubmatch(tail, -1)
	if len(m) == 0 {
		return 0, errors.New("xfa: the PDF has no startxref to add to")
	}
	n, err := strconv.Atoi(string(m[len(m)-1][1]))
	if err != nil || n <= 0 || n >= len(data) {
		return 0, errors.New("xfa: the PDF's startxref doesn't point inside it")
	}
	return n, nil
}

func xrefStreamAt(data []byte, off int) bool {
	rest := bytes.TrimLeft(data[off:min(len(data), off+64)], " \t\r\n\x00")
	return !bytes.HasPrefix(rest, []byte("xref"))
}

// ---- encryption for what the update adds ----

// crypt encrypts new stream revisions the way the file's standard security
// handler does (ISO 32000-2, 7.6.3): RC4 or AES-128 with a per-object key,
// or AES-256 with the file key. pdfcpu reads such files with the empty user
// password and gives the file key.
type crypt struct {
	key            []byte
	r              int
	stm            bool
	aesStm, aesStr bool
}

func newCrypt(x *model.XRefTable) (*crypt, error) {
	if x.Encrypt == nil {
		return nil, nil
	}
	ed, err := x.DereferenceDict(*x.Encrypt)
	if err != nil || ed == nil {
		return nil, errors.New("xfa: the PDF's encryption settings can't be read")
	}
	if f := ed.NameEntry("Filter"); f == nil || *f != "Standard" {
		return nil, errors.New("xfa: the PDF uses certificate security, which this package can't write")
	}
	if x.E == nil || len(x.EncKey) == 0 {
		return nil, errors.New("xfa: the PDF's encryption key isn't known")
	}
	c := &crypt{key: x.EncKey, r: x.E.R, stm: true, aesStm: x.AES4Streams, aesStr: x.AES4Strings}
	if x.E.V >= 4 {
		if n := ed.NameEntry("StmF"); n == nil || *n == "Identity" {
			c.stm = false
		}
	}
	return c, nil
}

func (c *crypt) objKey(n, g int, useAES bool) []byte {
	if useAES && (c.r == 5 || c.r == 6) {
		return c.key
	}
	b := append(append([]byte(nil), c.key...), byte(n), byte(n>>8), byte(n>>16), byte(g), byte(g>>8))
	if useAES {
		b = append(b, "sAlT"...)
	}
	sum := md5.Sum(b)
	return sum[:min(len(c.key)+5, 16)]
}

func (c *crypt) seal(b []byte, n, g int, useAES bool) ([]byte, error) {
	k := c.objKey(n, g, useAES)
	if !useAES {
		ci, err := rc4.NewCipher(k)
		if err != nil {
			return nil, err
		}
		out := make([]byte, len(b))
		ci.XORKeyStream(out, b)
		return out, nil
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(b)%aes.BlockSize
	plain := append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	out := make([]byte, aes.BlockSize+len(plain))
	if _, err := io.ReadFull(rand.Reader, out[:aes.BlockSize]); err != nil {
		return nil, err
	}
	cipher.NewCBCEncrypter(block, out[:aes.BlockSize]).CryptBlocks(out[aes.BlockSize:], plain)
	return out, nil
}
