// lo.Sum via SPMD: classic reduction baseline.
package main

import (
	"lanes"
	"os"
	"reduce"
)

//go:noinline
func sumSPMD(data []int32) int32 {
	var total lanes.Varying[int32] = 0
	go for _, v := range data {
		total += v
	}
	return reduce.Add(total)
}

func main() {
	base := int32(len(os.Args))
	data := make([]int32, 16)
	for i := range data {
		data[i] = int32(i+1) + base - 1
	}
	println(sumSPMD(data))
}
