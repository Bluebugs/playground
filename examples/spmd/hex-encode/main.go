// Hex encoding via SPMD: single vpshufb / i8x16.swizzle does the nibble->hex map.
package main

import "os"

const hextable = "0123456789abcdef"

//go:noinline
func Encode(dst, src []byte) int {
	go for i := range dst {
		v := src[i>>1]
		if i%2 == 0 {
			dst[i] = hextable[v>>4]
		} else {
			dst[i] = hextable[v&0x0f]
		}
	}
	return len(src) * 2
}

func main() {
	// Read input from stdin so the compiler cannot constant-fold the encode away.
	src := make([]byte, 64)
	n, _ := os.Stdin.Read(src)
	if n == 0 {
		src = []byte("hello SPMD world")
		n = len(src)
	}
	src = src[:n]
	dst := make([]byte, n*2)
	Encode(dst, src)
	println(string(dst))
}
