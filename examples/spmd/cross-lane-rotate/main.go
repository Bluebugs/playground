// Cross-lane rotation: lanes.Rotate compiles to a single shufflevector.
// Each lane reads its left-neighbor's value (offset 1, wrap-around).
package main

import (
	"fmt"
	"lanes"
	"os"
)

//go:noinline
func RotateDemo(data []int32) {
	go for i, v := range data {
		rotated := lanes.Rotate(v, 1)
		fmt.Printf("lane=%d original=%v rotated=%v\n", i, v, rotated)
	}
}

func main() {
	base := int32(len(os.Args)) * 10
	data := make([]int32, 8)
	for i := range data {
		data[i] = int32(i+1)*10 + base - 10
	}
	RotateDemo(data)
}
