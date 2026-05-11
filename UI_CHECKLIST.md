# Phase 2 UI Manual Checklist

Prereq: server running. `bash test_local.sh` exercises a smoke test and exits;
start the server manually with:

```
PATH="$PWD/../go/bin:$PATH" GOEXPERIMENT=spmd go run . -dir .
```

(Or use a long-running variant of `test_local.sh` that does not auto-close.)
Open http://localhost:8080/ in Firefox 128+ or Chrome 130+.

1. Page loads. Header shows "SPMD Playground", example dropdown reads "Simple
   sum" (first manifest entry), editor shows the simple-sum Go source, Run
   tab is the active highlighted tab.
2. Click the example dropdown -> all 9 entries listed with their `label` from
   `manifest.json`. Pick "Hex encode (table lookup)" -> editor source replaces
   with hex-encode/main.go, hint at the bottom mentions vpshufb / i8x16.swizzle.
3. Click "Run" -> "Compiling..." appears, then within a few seconds the
   hex-encoded default literal is shown: the hex encoding of
   "hello SPMD world", i.e. `68656c6c6f2053504d4420776f726c64`.
4. Switch to "WAT" tab -> WAT text appears within ~2s. `i8x16.swizzle` shown
   in bright amber bold; surrounding `i32x4.add`, `v128.const`, etc. in
   subtle blue. `local.get 0` left uncolored.
5. Switch to "AVX2" tab -> x86 disasm of `main.Encode` visible. `vpshufb`
   highlighted bright amber bold; surrounding `vmovdqu`, `vpaddd`, `vpor`, etc.
   subtle blue. Labels (`<main.Encode>:`), address columns, raw bytes, and
   `mov`/`add`/`cmp`/`jmp` all uncolored.
6. Uncheck the SIMD checkbox in the header -> AVX2 pane refreshes; no
   `vpshufb`; mostly plain scalar `mov`/`shr`/`or`/`and`; tier-2 amber
   highlighting absent.
7. Re-check SIMD. Pick "Base64 decode (Mula-Lemire)", switch to AVX2 tab ->
   `vpmaddubsw`, `vpmaddwd`, `vpshufb`, `vpermd` all visible in bright amber.
8. Pick "Mandelbrot (per-lane break)", click Run -> ASCII fractal output
   rendered in the pane within a few seconds.
9. Pick "Vectorized table lookup" with SIMD on, switch to WAT tab -> the
   TinyGo build error surfaces in the pane. The underlying cause is that
   TinyGo's build pipeline invokes wasm-opt internally and wasm-opt rejects
   the relaxed-SIMD opcode emitted by TinyGo ("invalid code after SIMD
   prefix: 256"); TinyGo surfaces that on its build error stream. The pane
   shows it cleanly as monospace text -- no UI crash, no highlighting
   misapplied to the error string.
10. In the editor, delete a closing brace `}` near the end of the file and
    switch to any tab -> the TinyGo compile error text appears in the pane
    in plain monospace, no UI crash. Restoring the brace and switching tabs
    again brings normal output back (or "Compiling...", then output).

Pass criteria: all 10 steps produce the expected visual outcome on
Firefox 128+ and Chrome 130+. The Run tab is wired (stdin shimmed to return
zero bytes; examples fall back to their built-in demo input).
