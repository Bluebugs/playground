// SAXPY: y = a*x + y. lanes.FMA lowers to f32x4.relaxed_madd on WASM relaxed-SIMD
// (implementation-defined fused-or-unfused, avoids scalarization to $fmaf libcalls)
// and to vfmadd213ps on x86 AVX2+FMA3 (exact IEEE 754 single-rounding FMA).
// Plain `a*xi + y[i]` keeps two IEEE roundings, so LLVM cannot fuse it.
package main

import "lanes"

//go:noinline
func saxpy(a float32, x, y []float32) {
	go for i, xi := range x {
		y[i] = lanes.FMA(a, xi, y[i])
	}
}

func main() {
	x := make([]float32, 8)
	y := make([]float32, 8)
	for i := range x {
		x[i] = float32(i + 1)
		y[i] = float32((i + 1) * 10)
	}
	saxpy(2.0, x, y)
	for _, v := range y {
		println(v)
	}
}
