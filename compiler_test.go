package main

import (
	"os"
	"strings"
	"testing"
)

// syntheticWAT is a minimal WAT text used by filterWATBySymbols tests. It
// contains a directly-requested func ($main.Foo), a func that is NOT requested
// but serves as the gowrapper fallback, and an unrelated func.
const syntheticWAT = `(module
  (func $main.Foo (param i32) (result i32)
    local.get 0
    i32.const 1
    i32.add)
  (func $runtime.run$1$gowrapper (param i32)
    ;; inlined body including f32x4.relaxed_madd
    f32x4.relaxed_madd
    drop)
  (func $other.func (param i32)
    drop)
)`

// syntheticWATMissing is a WAT where the requested symbol is absent but the
// gowrapper is present.
const syntheticWATMissing = `(module
  (func $runtime.run$1$gowrapper (param i32)
    ;; inlined: main.saxpy body with f32x4.relaxed_madd
    f32x4.relaxed_madd
    drop)
  (func $other.func (param i32)
    drop)
)`

// syntheticWATNoFallback has neither the requested symbol nor any gowrapper.
const syntheticWATNoFallback = `(module
  (func $other.func (param i32)
    drop)
)`

func writeTempWAT(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp("", "test-*.wat")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	f.Close()
	t.Cleanup(func() { os.Remove(f.Name()) })
	return f.Name()
}

func readWAT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read filtered WAT: %v", err)
	}
	return string(b)
}

// TestFilterWATBySymbols_FoundSymbol verifies that when the requested symbol
// IS present in the WAT the existing behavior is preserved: the func block is
// captured and no fallback annotation is emitted.
func TestFilterWATBySymbols_FoundSymbol(t *testing.T) {
	path := writeTempWAT(t, syntheticWAT)

	if err := filterWATBySymbols(path, []string{"main.Foo"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	if !strings.Contains(got, "(func $main.Foo") {
		t.Errorf("expected $main.Foo block in output, got:\n%s", got)
	}
	if strings.Contains(got, "inlined by wasm-opt") {
		t.Errorf("unexpected fallback annotation when symbol IS found, got:\n%s", got)
	}
	if strings.Contains(got, "$runtime.run$1$gowrapper") {
		t.Errorf("unexpected gowrapper block when symbol IS found, got:\n%s", got)
	}
	if strings.Contains(got, "$other.func") {
		t.Errorf("unexpected $other.func in output (should be filtered), got:\n%s", got)
	}
}

// TestFilterWATBySymbols_MissingSymbolFallback verifies that when the requested
// symbol is NOT in the WAT (inlined away) the gowrapper fallback block is
// emitted with a clear annotation naming the missing symbol and the fallback.
func TestFilterWATBySymbols_MissingSymbolFallback(t *testing.T) {
	path := writeTempWAT(t, syntheticWATMissing)

	if err := filterWATBySymbols(path, []string{"main.saxpy"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	if !strings.Contains(got, "inlined by wasm-opt") {
		t.Errorf("expected fallback annotation, got:\n%s", got)
	}
	if !strings.Contains(got, "$main.saxpy") {
		t.Errorf("expected missing symbol name in annotation, got:\n%s", got)
	}
	if !strings.Contains(got, "$runtime.run$1$gowrapper") {
		t.Errorf("expected gowrapper fallback block, got:\n%s", got)
	}
	if !strings.Contains(got, "f32x4.relaxed_madd") {
		t.Errorf("expected gowrapper body content (f32x4.relaxed_madd), got:\n%s", got)
	}
	// The fallback must appear only once.
	count := strings.Count(got, "(func $runtime.run$1$gowrapper")
	if count != 1 {
		t.Errorf("gowrapper block appeared %d times, want 1, got:\n%s", count, got)
	}
}

// TestFilterWATBySymbols_NoFallbackAvailable verifies the last-resort behavior:
// when the requested symbol is missing AND no gowrapper/fallback exists, a
// plain ";; (symbol not found: ...)" line is emitted.
func TestFilterWATBySymbols_NoFallbackAvailable(t *testing.T) {
	path := writeTempWAT(t, syntheticWATNoFallback)

	if err := filterWATBySymbols(path, []string{"main.saxpy"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	if !strings.Contains(got, "symbol not found") {
		t.Errorf("expected 'symbol not found' line, got:\n%s", got)
	}
	if !strings.Contains(got, "$main.saxpy") {
		t.Errorf("expected missing symbol name in output, got:\n%s", got)
	}
	if strings.Contains(got, "inlined by wasm-opt") {
		t.Errorf("unexpected fallback annotation when no fallback exists, got:\n%s", got)
	}
}

// TestFilterWATBySymbols_FallbackEmittedOnce verifies that even when multiple
// requested symbols are all missing, the gowrapper fallback is emitted exactly
// once (not once per missing symbol).
func TestFilterWATBySymbols_FallbackEmittedOnce(t *testing.T) {
	path := writeTempWAT(t, syntheticWATMissing)

	if err := filterWATBySymbols(path, []string{"main.saxpy", "main.other"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	count := strings.Count(got, "(func $runtime.run$1$gowrapper")
	if count != 1 {
		t.Errorf("gowrapper block appeared %d times (want 1) when 2 symbols missing, got:\n%s", count, got)
	}
}

// syntheticWATMainAndGowrapper contains $main.main (directly matched) and
// $runtime.run$1$gowrapper (the fallback candidate) but NOT $main.saxpy.
// This is the scenario that exposed the break-vs-continue bug: when
// $main.main is a fallback candidate AND was already matched in Pass 1, a
// break exited the entire candidate loop before the gowrapper was tried.
const syntheticWATMainAndGowrapper = `(module
  (func $main.main (param i32) (result i32)
    local.get 0
    i32.const 42
    i32.add)
  (func $runtime.run$1$gowrapper (param i32)
    ;; inlined saxpy body
    f32x4.relaxed_madd
    drop)
  (func $other.func (param i32)
    drop)
)`

// TestFilterWATBySymbols_MatchedNameSharesFallbackCandidate tests the bug
// where $main.main appears in both the requested-symbol list AND the
// fallback-candidate list.  When $main.main is matched in Pass 1 and
// $main.saxpy is missing, the old `break` exited the candidate loop before
// reaching $runtime.run$1$gowrapper, so no fallback was emitted.  With
// `continue` the loop skips the already-emitted $main.main and correctly
// picks $runtime.run$1$gowrapper as the fallback.
func TestFilterWATBySymbols_MatchedNameSharesFallbackCandidate(t *testing.T) {
	path := writeTempWAT(t, syntheticWATMainAndGowrapper)

	if err := filterWATBySymbols(path, []string{"main.main", "main.saxpy"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	// $main.main was directly matched — its block must appear.
	if !strings.Contains(got, "(func $main.main") {
		t.Errorf("expected $main.main block in output, got:\n%s", got)
	}
	// $main.saxpy is missing so the fallback annotation must be present.
	if !strings.Contains(got, "inlined by wasm-opt") {
		t.Errorf("expected fallback annotation for missing $main.saxpy, got:\n%s", got)
	}
	// The annotation must name the missing symbol.
	if !strings.Contains(got, "$main.saxpy") {
		t.Errorf("expected missing symbol $main.saxpy named in annotation, got:\n%s", got)
	}
	// The gowrapper fallback block itself must be present.
	if !strings.Contains(got, "(func $runtime.run$1$gowrapper") {
		t.Errorf("expected $runtime.run$1$gowrapper fallback block, got:\n%s", got)
	}
	// The gowrapper body token must survive into the output.
	if !strings.Contains(got, "f32x4.relaxed_madd") {
		t.Errorf("expected gowrapper body token f32x4.relaxed_madd, got:\n%s", got)
	}
}

// syntheticWATPrefix has two main.* funcs (so a `main.*` wildcard expands to
// both) plus an unrelated func that must be filtered out.
const syntheticWATPrefix = `(module
  (func $main.Foo (param i32) (result i32)
    local.get 0
    i32.const 1
    i32.add)
  (func $main.Bar (param i32) (result i32)
    local.get 0
    i32.const 2
    i32.add)
  (func $other.func (param i32)
    drop)
)`

func TestSplitWildcardSymbols(t *testing.T) {
	exact, prefixes := splitWildcardSymbols([]string{"main.Foo", "main.*", "x.*", "main.Bar"})
	if strings.Join(exact, ",") != "main.Foo,main.Bar" {
		t.Errorf("exact = %v, want [main.Foo main.Bar]", exact)
	}
	if strings.Join(prefixes, ",") != "main.,x." {
		t.Errorf("prefixes = %v, want [main. x.]", prefixes)
	}
}

// TestFilterWATBySymbols_PrefixWildcard verifies `main.*` expands to every
// $main.* func and excludes unrelated funcs, with no fallback annotation.
func TestFilterWATBySymbols_PrefixWildcard(t *testing.T) {
	path := writeTempWAT(t, syntheticWATPrefix)

	if err := filterWATBySymbols(path, []string{"main.*"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	if !strings.Contains(got, "(func $main.Foo") || !strings.Contains(got, "(func $main.Bar") {
		t.Errorf("expected both $main.Foo and $main.Bar blocks, got:\n%s", got)
	}
	if strings.Contains(got, "$other.func") {
		t.Errorf("unexpected $other.func (should be filtered by prefix), got:\n%s", got)
	}
	if strings.Contains(got, "inlined by wasm-opt") || strings.Contains(got, "symbol not found") {
		t.Errorf("unexpected fallback annotation when prefix matched, got:\n%s", got)
	}
}

// TestFilterWATBySymbols_PrefixWildcardAllInlined verifies that when wasm-opt
// inlined every user function away (no $main.* survives — the real scratch /
// WASM case) the `main.*` wildcard is treated as missing and the gowrapper
// fallback fires, so the WASM tab is never blank.
func TestFilterWATBySymbols_PrefixWildcardAllInlined(t *testing.T) {
	path := writeTempWAT(t, syntheticWATMissing)

	if err := filterWATBySymbols(path, []string{"main.*"}); err != nil {
		t.Fatalf("filterWATBySymbols: %v", err)
	}
	got := readWAT(t, path)

	if !strings.Contains(got, "(func $runtime.run$1$gowrapper") {
		t.Errorf("expected gowrapper fallback when all main.* inlined, got:\n%s", got)
	}
	if !strings.Contains(got, "$main.*") {
		t.Errorf("expected missing wildcard $main.* named in annotation, got:\n%s", got)
	}
	if !strings.Contains(got, "f32x4.relaxed_madd") {
		t.Errorf("expected inlined body token to survive via fallback, got:\n%s", got)
	}
}

// syntheticObjdump mimics objdump -d output: a banner, a user main.* block,
// and a runtime block that a `main.*` wildcard must drop.
const syntheticObjdump = `
demo.elf:     file format elf64-x86-64


Disassembly of section .text:

00000000002292b0 <main.MyKernel>:
  2292b0:	push   rbp
  2292b1:	vpaddd ymm0,ymm0,ymm1
  229336:	ret

0000000000201000 <runtime.scheduler>:
  201000:	nop
  201001:	ret
`

func TestFilterObjdumpBySymbols_PrefixWildcard(t *testing.T) {
	out := string(filterObjdumpBySymbols([]byte(syntheticObjdump),
		[]string{"main.*"}, nil, []string{"main."}))

	if !strings.Contains(out, "<main.MyKernel>:") {
		t.Errorf("expected main.MyKernel block kept, got:\n%s", out)
	}
	if !strings.Contains(out, "vpaddd ymm0") {
		t.Errorf("expected main.MyKernel body kept, got:\n%s", out)
	}
	if strings.Contains(out, "runtime.scheduler") {
		t.Errorf("expected runtime.scheduler block dropped, got:\n%s", out)
	}
	if !strings.Contains(out, "Disassembly of section .text:") {
		t.Errorf("expected objdump banner preserved, got:\n%s", out)
	}
	if strings.Contains(out, "no symbols matched") {
		t.Errorf("unexpected no-match note when a block matched, got:\n%s", out)
	}
}

func TestFilterObjdumpBySymbols_NoMatch(t *testing.T) {
	noUser := `
demo.elf:     file format elf64-x86-64


Disassembly of section .text:

0000000000201000 <runtime.scheduler>:
  201000:	ret
`
	out := string(filterObjdumpBySymbols([]byte(noUser),
		[]string{"main.*"}, nil, []string{"main."}))

	if strings.Contains(out, "runtime.scheduler") {
		t.Errorf("expected non-matching block dropped, got:\n%s", out)
	}
	if !strings.Contains(out, "no symbols matched") {
		t.Errorf("expected no-match note so the pane is not blank, got:\n%s", out)
	}
}

// TestFilterObjdumpBySymbols_RuntimeRunMainFallback covers the real reported
// case: user code without //go:noinline is inlined entirely into
// runtime.runMain (the native-ELF analogue of $runtime.run$N$gowrapper), so
// no main.* symbol survives. The AVX2 pane must fall back to runtime.runMain
// — which holds the actual vector code — instead of the bare no-match note.
func TestFilterObjdumpBySymbols_RuntimeRunMainFallback(t *testing.T) {
	inlinedObjdump := `
arr.elf:     file format elf64-x86-64


Disassembly of section .text:

000000000021be90 <main>:
  21be90:	push   rax
  21beaa:	call   21beb3 <runtime.runMain>
  21beb2:	ret

000000000021beb3 <runtime.runMain>:
  21beb3:	push   rbp
  21bf01:	vpaddd ymm0,ymm0,ymm1
  21bf30:	ret

0000000000201000 <runtime.scheduler>:
  201000:	ret
`
	out := string(filterObjdumpBySymbols([]byte(inlinedObjdump),
		[]string{"main.*"}, nil, []string{"main."}))

	if !strings.Contains(out, "symbols inlined: main.*") {
		t.Errorf("expected inlined-symbols annotation, got:\n%s", out)
	}
	if !strings.Contains(out, "showing runtime.runMain") {
		t.Errorf("expected fallback to runtime.runMain named, got:\n%s", out)
	}
	if !strings.Contains(out, "<runtime.runMain>:") || !strings.Contains(out, "vpaddd ymm0") {
		t.Errorf("expected runtime.runMain block with vector code, got:\n%s", out)
	}
	if strings.Contains(out, "no symbols matched") {
		t.Errorf("should use the fallback, not the bare no-match note, got:\n%s", out)
	}
	// The tiny <main> entry stub and the scheduler must not leak in.
	if strings.Contains(out, "<runtime.scheduler>:") {
		t.Errorf("unexpected runtime.scheduler in fallback output, got:\n%s", out)
	}
}
