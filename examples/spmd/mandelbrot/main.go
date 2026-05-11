// Mandelbrot set via SPMD: varying control flow + per-lane break.
package main

import (
	"lanes"
	"os"
)

//go:noinline
func mandelSPMD(cRe, cIm lanes.Varying[float32], maxIter int32) lanes.Varying[int32] {
	var zRe lanes.Varying[float32] = cRe
	var zIm lanes.Varying[float32] = cIm
	var iterations lanes.Varying[int32] = maxIter

	for iter := range maxIter {
		magSquared := zRe*zRe + zIm*zIm
		diverged := magSquared > 4.0

		if diverged {
			iterations = iter
			break
		}

		newRe := zRe*zRe - zIm*zIm
		newIm := 2.0 * zRe * zIm
		zRe = cRe + newRe
		zIm = cIm + newIm
	}
	return iterations
}

//go:noinline
func mandelbrotSPMD(x0, y0, x1, y1 float32, width, height, maxIter int32, output []int32) {
	dx := (x1 - x0) / float32(width)
	dy := (y1 - y0) / float32(height)
	for j := int32(0); j < height; j++ {
		y := y0 + float32(j)*dy
		go for i := range width {
			x := x0 + lanes.Varying[float32](i)*dx
			output[j*width+i] = mandelSPMD(x, y, maxIter)
		}
	}
}

func main() {
	// Use len(os.Args) to perturb bounds slightly so the entire image cannot
	// be precomputed as a constant blob.
	jitter := float32(len(os.Args)) * 0.001
	const W, H, Iter int32 = 32, 16, 64
	output := make([]int32, W*H)
	mandelbrotSPMD(-2.0+jitter, -1.0, 1.0, 1.0, W, H, Iter, output)
	gradient := " .:-=+*#%@"
	for y := int32(0); y < H; y++ {
		for x := int32(0); x < W; x++ {
			v := output[y*W+x]
			if v >= Iter {
				v = Iter - 1
			}
			print(string(gradient[int(v)*len(gradient)/int(Iter)]))
		}
		println()
	}
}
