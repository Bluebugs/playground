// SAXPY: y = a*x + y. lanes.FMA lowers to f32x4.relaxed_madd on WASM relaxed-SIMD
// (implementation-defined fused-or-unfused, avoids scalarization to $fmaf libcalls)
// and to vfmadd213ps on x86 AVX2+FMA3 (exact IEEE 754 single-rounding FMA).
// Plain `a*xi + y[i]` keeps two IEEE roundings, so LLVM cannot fuse it.
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
	args := os.Args
	n := len(args)
	x := make([]float32, 8)
	y := make([]float32, 8)
	for i := range x {
		x[i] = float32(i + 1)
		y[i] = float32((i + 1) * 10)
	}
	// Two call-sites prevent Binaryen wasm-opt's single-caller inliner from
	// removing $main.saxpy as a distinct function (needed so the WAT tab filter
	// finds the symbol). The second branch is never taken at runtime (WASI
	// programs always receive argv[0] so n>=1), but LLVM cannot prove this
	// because os.Args is an opaque runtime value — both calls survive to WASM.
	if n > 1 {
		saxpy(float32(n), x, y)
	}
	saxpy(2.0, x, y)
	for _, v := range y {
		println(v)
	}
}
