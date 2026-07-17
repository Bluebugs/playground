#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${1:-http://localhost:8080}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MANIFEST="$SCRIPT_DIR/examples/manifest.json"

if ! command -v jq >/dev/null 2>&1; then
  echo "ERROR: jq is required but not found in PATH" >&2
  exit 1
fi

if [ ! -f "$MANIFEST" ]; then
  echo "ERROR: manifest not found at $MANIFEST" >&2
  exit 1
fi

# POST a Go source file with text/plain content type.
post_src() {
  local src="$1"; shift
  local url="$1"; shift
  curl -fsS -X POST -H "Content-Type: text/plain" --data-binary "@$src" "$url" "$@"
}

fail=0

# ---- Generic smoke: every example × every format × simd on/off ----

mapfile -t examples < <(jq -r '.[].key' "$MANIFEST")

for ex in "${examples[@]}"; do
  src="$SCRIPT_DIR/examples/spmd/$ex/main.go"
  if [ ! -f "$src" ]; then
    echo "SKIP (no source): $ex"
    continue
  fi

  # "run" format hits the existing /api/compile endpoint.
  for simd in true false; do
    out=$(mktemp)
    if post_src "$src" "$BASE_URL/api/compile?format=wasi&compiler=tinygo&simd=$simd" -o "$out" 2>/dev/null; then
      if [ ! -s "$out" ]; then
        echo "FAIL empty [run simd=$simd]: $ex"
        fail=$((fail + 1))
      fi
    else
      echo "FAIL request [run simd=$simd]: $ex"
      fail=$((fail + 1))
    fi
    rm -f "$out"
  done

  for fmt in wat asm; do
    for simd in true false; do
      out=$(mktemp)
      if post_src "$src" "$BASE_URL/api/$fmt?simd=$simd" -o "$out" 2>/dev/null; then
        if [ ! -s "$out" ]; then
          echo "FAIL empty [$fmt simd=$simd]: $ex"
          fail=$((fail + 1))
        fi
      else
        echo "FAIL request [$fmt simd=$simd]: $ex"
        fail=$((fail + 1))
      fi
      rm -f "$out"
    done
  done
done

# ---- Targeted assertions on hex-encode ----

HEX_SRC="$SCRIPT_DIR/examples/spmd/hex-encode/main.go"
if [ -f "$HEX_SRC" ]; then
  asm_simd=$(post_src "$HEX_SRC" "$BASE_URL/api/asm?simd=true&symbols=main.EncodeSrc")
  asm_scalar=$(post_src "$HEX_SRC" "$BASE_URL/api/asm?simd=false&symbols=main.EncodeSrc")

  if echo "$asm_simd" | grep -q vpshufb; then
    echo "OK: hex-encode AVX2 SIMD contains vpshufb"
  else
    echo "FAIL: hex-encode AVX2 SIMD lacks vpshufb"
    fail=$((fail + 1))
  fi

  if echo "$asm_scalar" | grep -q vpshufb; then
    echo "FAIL: hex-encode scalar unexpectedly contains vpshufb"
    fail=$((fail + 1))
  else
    echo "OK: hex-encode scalar does not contain vpshufb"
  fi
fi

# ---- Targeted assertions on base64-mula-lemire ----

B64_SRC="$SCRIPT_DIR/examples/spmd/base64-mula-lemire/main.go"
if [ -f "$B64_SRC" ]; then
  asm_b64=$(post_src "$B64_SRC" "$BASE_URL/api/asm?simd=true&symbols=main.spmdDecode,main.decodeAndPack")

  for mnem in vpmaddubsw vpmaddwd vpshufb vpermd; do
    if echo "$asm_b64" | grep -q "$mnem"; then
      echo "OK: base64-mula-lemire AVX2 contains $mnem"
    else
      echo "FAIL: base64-mula-lemire AVX2 lacks $mnem"
      fail=$((fail + 1))
    fi
  done
fi

# ---- Summary ----

if [ "$fail" -eq 0 ]; then
  echo "OK: all endpoint checks passed"
else
  echo "FAILED: $fail check(s) failed"
fi
exit "$fail"
