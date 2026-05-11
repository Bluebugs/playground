// Vectorized table lookup: [16]byte LUT indexed by varying byte compiles
// to a single vpshufb (x86 SSSE3/AVX2) or i8x16.swizzle (WASM SIMD128).
package main

import "os"

//go:noinline
func Lookup(out, idx []byte, lut [16]byte) {
	go for i, n := range idx {
		out[i] = lut[n&0x0f]
	}
}

func main() {
	const hex = "0123456789abcdef"
	var lut [16]byte
	copy(lut[:], hex)
	// Defeat constant folding by deriving the indices from os.Stdin (with a
	// fixed fallback so the example still runs with stdin closed).
	buf := make([]byte, 16)
	n, _ := os.Stdin.Read(buf)
	if n == 0 {
		buf = []byte{0x0, 0x5, 0xa, 0xf, 0x3, 0xc, 0x7, 0x1}
		n = len(buf)
	}
	nibbles := buf[:n]
	out := make([]byte, len(nibbles))
	Lookup(out, nibbles, lut)
	println(string(out))
}
