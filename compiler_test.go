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
