// Command fnosicon resizes the project logo into the two fixed sizes fnOS
// requires. It uses only the Go standard library so release builds do not
// need ImageMagick or a separate image dependency.
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: fnosicon <source.png> <output-dir> <64|256>")
		os.Exit(2)
	}
	sourcePath, outputDir, sizeArg := os.Args[1], os.Args[2], os.Args[3]
	var size int
	if _, err := fmt.Sscanf(sizeArg, "%d", &size); err != nil || (size != 64 && size != 256) {
		fmt.Fprintln(os.Stderr, "size must be 64 or 256")
		os.Exit(2)
	}
	f, err := os.Open(sourcePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	source, err := png.Decode(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode logo:", err)
		os.Exit(1)
	}
	scaled := resize(source, size)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	name := fmt.Sprintf("ICON_%d.PNG", size)
	if size == 64 {
		name = "ICON.PNG"
	}
	out, err := os.Create(filepath.Join(outputDir, name))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer out.Close()
	if err := png.Encode(out, scaled); err != nil {
		fmt.Fprintln(os.Stderr, "encode icon:", err)
		os.Exit(1)
	}
}

// resize performs area-average (box) downsampling. It is sufficient for the
// fixed 512px source and the required 64px/256px targets.
func resize(source image.Image, size int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	srcWidth := source.Bounds().Dx()
	srcHeight := source.Bounds().Dy()
	for y := 0; y < size; y++ {
		startY := y * srcHeight / size
		endY := (y + 1) * srcHeight / size
		for x := 0; x < size; x++ {
			startX := x * srcWidth / size
			endX := (x + 1) * srcWidth / size
			var r, g, b, a uint64
			var count uint64
			for srcY := startY; srcY < endY; srcY++ {
				for srcX := startX; srcX < endX; srcX++ {
					pr, pg, pb, pa := source.At(srcX+source.Bounds().Min.X, srcY+source.Bounds().Min.Y).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					b += uint64(pb)
					a += uint64(pa)
					count++
				}
			}
			if count == 0 {
				count = 1
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r / count >> 8),
				G: uint8(g / count >> 8),
				B: uint8(b / count >> 8),
				A: uint8(a / count >> 8),
			})
		}
	}
	return dst
}
