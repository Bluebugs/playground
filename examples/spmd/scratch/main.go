// Blank starter — edit this file freely.
// The //go:noinline directive below lets the WASM and AVX2 tabs isolate
// this function's code; without it those tabs fall back to showing main.
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
