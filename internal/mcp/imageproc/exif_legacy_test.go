package imageproc

import (
	"image"
	"image/draw"
)

// legacyApplyOrientation is the pre-fix orientation code, kept as the
// byte-identity reference for applyOrientation. It rotates/flips src to compensate for the EXIF Orientation tag
// value. Values 1 (no-op) and anything outside 1..8 return src unchanged.
func legacyApplyOrientation(src image.Image, orientation int) image.Image {
	switch orientation {
	case 0, 1:
		return src
	case 2:
		return legacyFlipHorizontal(src)
	case 3:
		return legacyRotate180(src)
	case 4:
		return legacyFlipVertical(src)
	case 5:
		return legacyFlipHorizontal(legacyRotate90(src))
	case 6:
		return legacyRotate90(src)
	case 7:
		return legacyFlipHorizontal(legacyRotate270(src))
	case 8:
		return legacyRotate270(src)
	default:
		return src
	}
}

func legacyFlipHorizontal(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(b.Dx()-1-x, y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func legacyFlipVertical(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(x, b.Dy()-1-y, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func legacyRotate90(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(b.Dy()-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

func legacyRotate180(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return legacyFlipHorizontal(legacyFlipVertical(dst))
}

func legacyRotate270(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dy(), b.Dx()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			dst.Set(y, b.Dx()-1-x, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
