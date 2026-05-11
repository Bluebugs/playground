// SAXPY: y = a*x + y. Fused multiply-add via vfmadd* (x86) or f32x4.mul/add (WASM).
package main

import "os"

//go:noinline
func saxpy(a float32, x, y []float32) {
	go for i, xi := range x {
		y[i] = a*xi + y[i]
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
