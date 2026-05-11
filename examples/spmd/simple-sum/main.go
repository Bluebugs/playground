// Simple sum operation using SPMD Go.
// Reduction across lanes -- v128.* on WASM, vpaddd on AVX2.
package main

import (
	"fmt"
	"lanes"
	"os"
	"reduce"
)

//go:noinline
func Sum(data []int) int {
	var total lanes.Varying[int] = 0

	go for _, value := range data {
		total += value
	}

	return reduce.Add(total)
}

func main() {
	// len(os.Args) is opaque to the optimizer, defeating constant folding.
	base := len(os.Args)
	data := make([]int, 16)
	for i := range data {
		data[i] = i + base
	}
	fmt.Printf("Sum: %d\n", Sum(data))
}
