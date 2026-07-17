// Hex encoding via SPMD: single vpshufb / i8x16.swizzle does the nibble->hex map.
// Source-centric loop: iterate the input with unit stride and write two output
// bytes per source byte. This keeps loads/stores contiguous, unrolls a full
// 32-byte block per iteration, and is correct at every input length.
package main

import "os"

const hextable = "0123456789abcdef"

//go:noinline
func EncodeSrc(dst, src []byte) int {
	go for i := range src {
		dst[i*2] = hextable[src[i]>>4]
		dst[i*2+1] = hextable[src[i]&0x0f]
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
	EncodeSrc(dst, src)
	println(string(dst))
}
