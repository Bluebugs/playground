// SAXPY: y = a*x + y. lanes.FMA lowers to @llvm.fma, a single-rounding fused
// multiply-add: vfmadd213ps on x86 AVX2/FMA3, f32x4 mul+add on WASM SIMD128.
// (Plain `a*xi + y[i]` keeps two IEEE roundings, so LLVM cannot fuse it.)
package main

import (
	"lanes"
	"os"
)

//go:noinline
func saxpy(a float32, x, y []float32) {
	go for i, xi := range x {
		y[i] = lanes.FMA(a, xi, y[i])
	}
}

func main() {
	base := float32(len(os.Args))
	x := make([]float32, 8)
	y := make([]float32, 8)
	for i := range x {
		x[i] = float32(i+1) + base - 1
		y[i] = float32((i+1)*10) + base - 1
	}
	saxpy(2.0, x, y)
	for _, v := range y {
		println(v)
	}
}
