// Base64 decoder (Mula-Lemire). Cascading byte->i16->i32 SPMD loops emit
// vpmaddubsw + vpmaddwd + vpshufb + vpermd on AVX2.
package main

import (
	"lanes"
	"os"
)

var decodeLUT = [16]byte{
	0, 0, 16, 4, 191, 191, 185, 185,
	0, 0, 0, 0, 0, 0, 0, 0,
}

//go:noinline
func decodeAndPack(dst, src []byte) int {
	n := len(src)
	sextets := make([]byte, n)
	go for i, ch := range src {
		s := ch + decodeLUT[ch>>4]
		if ch == byte('+') {
			s += 3
		}
		sextets[i] = s
	}
	halfLen := n / 2
	merged := make([]int16, halfLen)
	go for g := range merged {
		merged[g] = int16(sextets[g*2])*64 + int16(sextets[g*2+1])
	}
	quarterLen := halfLen / 2
	packed := make([]int32, quarterLen)
	go for g := range packed {
		packed[g] = int32(merged[g*2])*4096 + int32(merged[g*2+1])
	}
	go for g := range packed {
		dst[g*3+0] = byte(packed[g] >> 16)
		dst[g*3+1] = byte(packed[g] >> 8)
		dst[g*3+2] = byte(packed[g])
	}
	return quarterLen * 3
}

func decodeSextet(ch byte) byte {
	switch {
	case 'A' <= ch && ch <= 'Z':
		return ch - 'A'
	case 'a' <= ch && ch <= 'z':
		return ch - 'a' + 26
	case '0' <= ch && ch <= '9':
		return ch - '0' + 52
	case ch == '+':
		return 62
	case ch == '/':
		return 63
	}
	return 0
}

//go:noinline
func spmdDecode(src []byte) ([]byte, bool) {
	if len(src) == 0 {
		return nil, true
	}
	if len(src)%4 != 0 {
		return nil, false
	}
	padCount := 0
	for i := len(src) - 1; i >= len(src)-2 && src[i] == '='; i-- {
		padCount++
	}
	groups := len(src) / 4
	hotGroups := groups
	if padCount > 0 {
		hotGroups--
	}
	hotBytes := hotGroups * 4
	dst := make([]byte, groups*3+64)
	var bv lanes.Varying[byte]
	chunkSize := max(4, lanes.Count[byte](bv))
	outOffset := 0
	for off := 0; off+chunkSize <= hotBytes; off += chunkSize {
		outOffset += decodeAndPack(dst[outOffset:], src[off:off+chunkSize])
	}
	rem := hotBytes % chunkSize
	if rem > 0 && rem%4 == 0 {
		padded := make([]byte, chunkSize)
		copy(padded, src[hotBytes-rem:hotBytes])
		for i := rem; i < chunkSize; i++ {
			padded[i] = 'A'
		}
		tmpDst := make([]byte, chunkSize)
		decodeAndPack(tmpDst, padded)
		validOut := rem * 3 / 4
		copy(dst[outOffset:], tmpDst[:validOut])
		outOffset += validOut
	}
	if hotGroups < groups {
		tail := src[hotGroups*4:]
		c0, c1 := decodeSextet(tail[0]), decodeSextet(tail[1])
		var c2, c3 byte
		if tail[2] != '=' {
			c2 = decodeSextet(tail[2])
		}
		if tail[3] != '=' {
			c3 = decodeSextet(tail[3])
		}
		dst[outOffset+0] = (c0 << 2) | (c1 >> 4)
		dst[outOffset+1] = (c1 << 4) | (c2 >> 2)
		dst[outOffset+2] = (c2 << 6) | c3
		outOffset += 3
	}
	return dst[:outOffset-padCount], true
}

func main() {
	// Read base64 input from stdin to defeat constant folding; fall back to
	// a literal when stdin is empty.
	buf := make([]byte, 1024)
	n, _ := os.Stdin.Read(buf)
	var enc []byte
	if n == 0 {
		enc = []byte("SGVsbG8gU1BNRCB3b3JsZCEhIQ==")
	} else {
		// Trim a trailing newline, if any.
		for n > 0 && (buf[n-1] == '\n' || buf[n-1] == '\r') {
			n--
		}
		enc = buf[:n]
	}
	out, ok := spmdDecode(enc)
	if !ok {
		println("decode failed")
		return
	}
	println(string(out))
}
