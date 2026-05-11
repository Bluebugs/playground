// lo.Contains via SPMD with early-exit: reduce.Any -> vtestps + branch.
package main

import (
	"os"
	"reduce"
)

//go:noinline
func containsSPMD(data []int32, target int32) bool {
	go for _, v := range data {
		found := v == target
		if reduce.Any(found) {
			return true
		}
	}
	return false
}

func main() {
	base := int32(len(os.Args))
	data := make([]int32, 16)
	for i := range data {
		data[i] = int32(i)*4 + base
	}
	println(containsSPMD(data, data[5]))           // true
	println(containsSPMD(data, -1+-int32(len(os.Args)))) // false
}
