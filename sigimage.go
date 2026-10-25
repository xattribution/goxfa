package xfa

import (
	"errors"
	"image"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// maxGraphic is the most pixels a signature graphic may have on a side.
const maxGraphic = 4096

// graphicObjects turns a signature's graphic into a PDF image: RGB
// samples, and a soft mask for its transparency when it has any. The
// image comes first.
func graphicObjects(img image.Image, alloc func() int) ([]updObj, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, errors.New("xfa: the signature graphic is empty")
	}
	if w > maxGraphic || h > maxGraphic {
		return nil, errors.New("xfa: the signature graphic is too large")
	}
	rgb := make([]byte, 0, w*h*3)
	alpha := make([]byte, 0, w*h)
	clear := false
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if a == 0 {
				// clear pixels go white, so readers that skip the mask
				// show paper rather than black
				rgb = append(rgb, 255, 255, 255)
			} else {
				// un-premultiply
				rgb = append(rgb, byte(r*0xffff/a>>8), byte(g*0xffff/a>>8), byte(bl*0xffff/a>>8))
			}
			alpha = append(alpha, byte(a>>8))
			if a != 0xffff {
				clear = true
			}
		}
	}
	im := updObj{num: alloc(), stream: rgb, dict: types.Dict{
		"Type":             types.Name("XObject"),
		"Subtype":          types.Name("Image"),
		"Width":            types.Integer(w),
		"Height":           types.Integer(h),
		"ColorSpace":       types.Name("DeviceRGB"),
		"BitsPerComponent": types.Integer(8),
	}}
	objs := []updObj{im}
	if clear {
		mask := updObj{num: alloc(), stream: alpha, dict: types.Dict{
			"Type":             types.Name("XObject"),
			"Subtype":          types.Name("Image"),
			"Width":            types.Integer(w),
			"Height":           types.Integer(h),
			"ColorSpace":       types.Name("DeviceGray"),
			"BitsPerComponent": types.Integer(8),
		}}
		im.dict["SMask"] = *types.NewIndirectRef(mask.num, 0)
		objs = append(objs, mask)
	}
	return objs, nil
}
