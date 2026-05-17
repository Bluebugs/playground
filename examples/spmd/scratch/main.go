// Blank starter — edit this file freely. Name your SPMD functions anything:
// the WASM and AVX2 tabs filter to every main.* function (manifest symbols
// "main.*"), so renaming or adding functions just works. //go:noinline keeps
// the AVX2 disassembly readable as a distinct symbol; if wasm-opt inlines a
// function away the WASM tab falls back to the wrapper that absorbed it.
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
)

//go:noinline
func Compute(data []int) int {
	var acc lanes.Varying[int] = 0

	go for _, v := range data {
		acc += v
	}

	return reduce.Add(acc)
}

func main() {
	// len(os.Args) is opaque to the optimizer, defeating constant folding.
	base := len(os.Args)
	data := make([]int, 16)
	for i := range data {
		data[i] = i + base
	}
	fmt.Printf("Result: %d\n", Compute(data))
}
