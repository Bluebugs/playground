package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	maxCacheSize = 10 * 1000 * 1000 // 10MB
)

type compilerJob struct {
	Source       []byte         // source code of program to compile
	SourceHash   string         // sha256 of source (in hex form)
	Filename     string         // cache file path
	Compiler     string         // compiler to use for this job
	Target       string         // target board name, or "wasm"
	Format       string         // output format: "wasm", "hex", "wat", "asm-avx2", etc.
	SIMD         bool           // whether to pass -simd=true or -simd=false to tinygo
	Symbols      []string       // function symbols to extract for asm-avx2; empty = whole .text
	ResultFile   chan string     // filename on completion
	ResultErrors chan []byte     // errors on completion
	Context      context.Context
}

// Started in the background, to limit the number of concurrent compiles.
func backgroundCompiler(ch chan compilerJob) {
	n := 0
	for job := range ch {
		n++
		err := job.Run()
		if err != nil {
			buf := &bytes.Buffer{}
			buf.WriteString(err.Error())
			job.ResultErrors <- buf.Bytes()
		}
		if n%100 == 1 {
			cleanupCompileCache()
		}
	}
}

// Run a single compiler job. It tries to load from the cache and kills the job
// (or even refuses to start) if this job was cancelled through the context.
func (job compilerJob) Run() error {
	outfileName := filepath.Base(job.Filename)

	// Attempt to load the file from the cache.
	_, err := os.Stat(job.Filename)
	if err == nil {
		// Cache hit!
		job.ResultFile <- job.Filename
		return nil
	}

	// Perhaps the job should not even be started.
	// Do a non-blocking read from the channel.
	select {
	case <-job.Context.Done():
		// Cancelled.
		return errors.New("aborted")
	default:
		// Not cancelled.
	}

	tmpfile := filepath.Join(cacheDir, "build-"+job.Compiler+"-"+job.Target+"-"+randomString(16)+".tmp."+job.Format)
	defer os.Remove(tmpfile)

	if bucket != nil {
		r, err := bucket.Object(outfileName).NewReader(job.Context)
		if err == nil {
			// File is already cached in the cloud.
			defer r.Close()

			// Copy the file (that is already cached in the cloud but not locally)
			// to the local cache.
			f, err := os.Create(tmpfile)
			if err != nil {
				return err
			}
			defer f.Close()
			if _, err := io.Copy(f, r); err != nil {
				return err
			}

			if err := os.Rename(tmpfile, job.Filename); err != nil {
				return err
			}

			// Done. Return the file that is now cached locally.
			job.ResultFile <- job.Filename
			return nil
		}
	}

	// Cache miss, compile now.
	// But first write the Go source code to a file so it can be read by the
	// compiler.
	tmpdir, err := os.MkdirTemp("", "tinygo-playground-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpdir)
	for _, fn := range []string{"go.mod", "go.sum"} {
		data, err := os.ReadFile("tinygo-template/" + fn)
		if err != nil {
			// go.sum may not exist (minimal go.mod with no deps).
			if fn == "go.sum" {
				continue
			}
			return err
		}
		err = os.WriteFile(tmpdir+"/"+fn, data, 0o666)
		if err != nil {
			return err
		}
	}
	infile, err := os.Create(tmpdir + "/main.go")
	if err != nil {
		return err
	}
	if _, err := infile.Write([]byte("//line main.go:1:1\n")); err != nil {
		return err
	}
	if _, err := infile.Write(job.Source); err != nil {
		return err
	}

	simdFlag := "-simd=false"
	if job.SIMD {
		simdFlag = "-simd=true"
	}

	var cmd *exec.Cmd
	env := []string{"GOPROXY=off"} // don't download dependencies
	switch job.Compiler {
	case "go":
		cmd = exec.Command("go", "build", "-json", "-trimpath", "-ldflags", "-s -w", "-o", tmpfile, infile.Name())
		env = append(env, "GOOS=wasip1", "GOARCH=wasm")
	case "tinygo":
		switch job.Format {
		case "wasm", "wasi":
			// Run code in the browser / WASI.
			tag := strings.Replace(job.Target, "-", "_", -1) // '-' not allowed in tags, use '_' instead
			cmd = exec.Command("tinygo", "build", "-json", "-o", tmpfile, "-target", job.Format, "-tags", tag, "-no-debug", simdFlag, infile.Name())
		case "wat":
			// Produce WebAssembly text format by building wasm then running wasm2wat.
			// The intermediate wasm lands in a sibling tmp file.
			wasmTmp := tmpfile + ".wasm"
			defer os.Remove(wasmTmp)
			tag := strings.ReplaceAll(job.Target, "-", "_")
			buildCmd := exec.CommandContext(job.Context, "tinygo", "build", "-json", "-o", wasmTmp, "-target", "wasi", "-tags", tag, "-no-debug", simdFlag, infile.Name())
			buildCmd.Dir = filepath.Dir(infile.Name())
			buildBuf := &bytes.Buffer{}
			buildCmd.Stdout = buildBuf
			buildCmd.Stderr = buildBuf
			buildCmd.Env = append(os.Environ(), env...)
			if err := buildCmd.Run(); err != nil {
				if buildBuf.Len() == 0 {
					buildBuf.WriteString(err.Error())
				}
				job.ResultErrors <- stripFilename(buildBuf.Bytes(), infile.Name())
				return nil
			}
			// Convert wasm to wat.
			// --enable-all covers relaxed-simd and other proposals TinyGo may emit
			// (i8x16.relaxed_swizzle, etc.) on this fork. Without it, wasm2wat
			// rejects the post-Relaxed-SIMD opcodes (0xFD 0x100+).
			watCmd := exec.CommandContext(job.Context, "wasm2wat", "--enable-all", wasmTmp, "-o", tmpfile)
			watBuf := &bytes.Buffer{}
			watCmd.Stderr = watBuf
			if err := watCmd.Run(); err != nil {
				if watBuf.Len() == 0 {
					watBuf.WriteString(err.Error())
				}
				job.ResultErrors <- watBuf.Bytes()
				return nil
			}
			// When specific symbols are requested, filter the WAT to just those
			// functions plus a small header — mirrors the per-symbol output of
			// `objdump --disassemble=<sym>` on the asm-avx2 path.
			if len(job.Symbols) > 0 {
				if err := filterWATBySymbols(tmpfile, job.Symbols); err != nil {
					job.ResultErrors <- []byte("wat symbol filter: " + err.Error())
					return nil
				}
			}
			if err := os.Rename(tmpfile, job.Filename); err != nil {
				job.ResultErrors <- []byte(err.Error())
				return nil
			}
			job.ResultFile <- job.Filename
			if cacheType == cacheTypeGCS {
				uploadToGCS(job)
			}
			return nil
		case "asm-avx2":
			// Build a native ELF with AVX2 features, then disassemble with objdump.
			elfTmp := tmpfile + ".elf"
			defer os.Remove(elfTmp)
			// Reference: test/e2e/spmd-benchmark-x86.sh uses these exact features.
			// +fma enables FMA3 (vfmadd*ps) so lanes.FMA lowers to a single fused op.
			buildCmd := exec.CommandContext(job.Context, "tinygo", "build", "-json", "-o", elfTmp,
				"-llvm-features=+ssse3,+sse4.2,+avx2,+fma", simdFlag, infile.Name())
			buildCmd.Dir = filepath.Dir(infile.Name())
			buildBuf := &bytes.Buffer{}
			buildCmd.Stdout = buildBuf
			buildCmd.Stderr = buildBuf
			buildCmd.Env = append(os.Environ(), env...)
			if err := buildCmd.Run(); err != nil {
				if buildBuf.Len() == 0 {
					buildBuf.WriteString(err.Error())
				}
				job.ResultErrors <- stripFilename(buildBuf.Bytes(), infile.Name())
				return nil
			}
			// Disassemble: per-symbol or whole .text.
			asmBuf := &bytes.Buffer{}
			if len(job.Symbols) == 0 {
				objCmd := exec.CommandContext(job.Context, "objdump", "-d", "-M", "intel", "--no-show-raw-insn", elfTmp)
				objCmd.Stdout = asmBuf
				objCmd.Stderr = asmBuf
				if err := objCmd.Run(); err != nil {
					job.ResultErrors <- asmBuf.Bytes()
					return nil
				}
			} else {
				for i, sym := range job.Symbols {
					if i > 0 {
						asmBuf.WriteString("\n")
					}
					asmBuf.WriteString("// ===== " + sym + " =====\n")
					objCmd := exec.CommandContext(job.Context, "objdump", "-d", "-M", "intel", "--no-show-raw-insn",
						"--disassemble="+sym, elfTmp)
					symBuf := &bytes.Buffer{}
					objCmd.Stdout = symBuf
					objCmd.Stderr = symBuf
					if err := objCmd.Run(); err != nil {
						// If a symbol is missing, include the error as a comment rather than failing.
						asmBuf.WriteString("// (symbol not found: " + err.Error() + ")\n")
					} else {
						asmBuf.Write(symBuf.Bytes())
					}
				}
			}
			// Write combined disassembly to cache file.
			if err := os.WriteFile(tmpfile, asmBuf.Bytes(), 0o666); err != nil {
				return err
			}
			if err := os.Rename(tmpfile, job.Filename); err != nil {
				job.ResultErrors <- []byte(err.Error())
				return nil
			}
			job.ResultFile <- job.Filename
			if cacheType == cacheTypeGCS {
				uploadToGCS(job)
			}
			return nil
		default:
			// build firmware
			cmd = exec.Command("tinygo", "build", "-json", "-o", tmpfile, "-target", job.Target, infile.Name())
		}
	}
	buf := &bytes.Buffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf
	cmd.Dir = filepath.Dir(infile.Name()) // avoid long relative paths in error messages
	cmd.Env = append(os.Environ(), env...)
	finishedChan := make(chan struct{})
	func() {
		defer close(finishedChan)
		err := cmd.Run()
		if err != nil {
			if buf.Len() == 0 {
				buf.WriteString(err.Error())
			}
			job.ResultErrors <- stripFilename(buf.Bytes(), infile.Name())
			return
		}
		if err := os.Rename(tmpfile, job.Filename); err != nil {
			// unlikely
			buf.WriteString(err.Error())
			job.ResultErrors <- buf.Bytes()
			return
		}

		// Now copy the file over to cloud storage to cache across all
		// instances.
		if cacheType == cacheTypeGCS {
			uploadToGCS(job)
		}

		// Done. Return the local file immediately.
		job.ResultFile <- job.Filename
	}()
	select {
	case <-finishedChan:
		// Job was completed before a cancellation.
	case <-job.Context.Done():
		// Job should be killed: it's useless now.
		cmd.Process.Kill()
	}
	return nil
}

// uploadToGCS uploads the compiled result to Google Cloud Storage.
// Errors are logged but not fatal — the local cache still has the file.
func uploadToGCS(job compilerJob) {
	outfileName := filepath.Base(job.Filename)
	obj := bucket.Object(outfileName)
	w := obj.NewWriter(job.Context)
	r, err := os.Open(job.Filename)
	if err != nil {
		log.Println(err.Error())
		return
	}
	defer r.Close()
	if _, err := io.Copy(w, r); err != nil {
		log.Println(err.Error())
		return
	}
	if err := w.Close(); err != nil {
		log.Println(err.Error())
		return
	}
}

// cleanupCompileCache is called regularly to clean up old compile results from
// the cache if the cache has grown too big.
func cleanupCompileCache() {
	totalSize := int64(0)
	files, err := ioutil.ReadDir(cacheDir)
	if err != nil {
		log.Println("could not read cache dir: ", err)
		return
	}
	for _, fi := range files {
		totalSize += fi.Size()
	}
	if totalSize > maxCacheSize {
		// Sort by modification time.
		sort.Slice(files, func(i, j int) bool {
			if files[i].ModTime().UnixNano() != files[j].ModTime().UnixNano() {
				return files[i].ModTime().UnixNano() < files[j].ModTime().UnixNano()
			}
			return files[i].Name() < files[j].Name()
		})

		// Remove all the oldest files.
		for totalSize > maxCacheSize {
			file := files[0]
			totalSize -= file.Size()
			err := os.Remove(filepath.Join(cacheDir, file.Name()))
			if err != nil {
				log.Println("failed to remove cache file:", err)
			}
			files = files[1:]
		}
	}
}

var seededRand *rand.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))

func randomString(length int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		b[i] = chars[seededRand.Intn(len(chars))]
	}
	return string(b)
}

func stripFilename(buf []byte, filename string) []byte {
	prefix := []byte("# " + filename + "\n")
	if bytes.HasPrefix(buf, prefix) {
		buf = buf[len(prefix):]
	}
	return buf
}

// cacheFilename builds the local cache file path for a compile job.
// The SIMD flag and sorted symbols list are included so independent variants
// cache independently and never collide on the same source.
func cacheFilename(compiler, target, sourceHash, format string, simd bool, symbols []string) string {
	simdSuffix := "simdfalse"
	if simd {
		simdSuffix = "simdtrue"
	}
	symSuffix := ""
	if len(symbols) > 0 {
		sorted := append([]string(nil), symbols...)
		sort.Strings(sorted)
		h := sha256.Sum256([]byte(strings.Join(sorted, ",")))
		symSuffix = "-" + hex.EncodeToString(h[:16])
	}
	return filepath.Join(cacheDir, "build-"+compiler+"-"+target+"-"+sourceHash+"-"+simdSuffix+symSuffix+"."+format)
}

// filterWATBySymbols rewrites a WAT file in place, keeping only top-level
// `(func $<sym> ...)` blocks whose name matches one of `symbols`, plus a small
// header. Indices into the original module are preserved by name (wasm2wat
// resolves them when the WASM name section is present, which TinyGo emits).
//
// Paren-balanced from the opening `(func ...` to the matching close paren.
// `;; comment` lines and inline `(;...;)` comments do not contain unbalanced
// parens in wabt output, so naive paren counting is sufficient.
//
// Fallback: when wasm-opt inlines a small single-call-site function (e.g.
// main.saxpy disappears into $runtime.run$1$gowrapper), the missing symbol is
// noted and the gowrapper block is emitted with a clear annotation instead of
// a bare "symbol not found" line.
func filterWATBySymbols(watFile string, symbols []string) error {
	data, err := os.ReadFile(watFile)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		want["$"+s] = true
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, ";; Filtered to symbols: %s\n", strings.Join(symbols, ", "))
	out.WriteString(";; Drop the ?symbols= query param to view the whole module.\n\n")

	lines := strings.Split(string(data), "\n")

	// captureFuncBlock scans lines for a top-level `(func $target ...)` block
	// and writes it (paren-balanced) into dst. Returns true if found.
	captureFuncBlock := func(dst *bytes.Buffer, target string) bool {
		capturing := false
		depth := 0
		found := false
		for _, line := range lines {
			if !capturing {
				trimmed := strings.TrimLeft(line, " \t")
				if strings.HasPrefix(trimmed, "(func $") {
					rest := trimmed[len("(func "):]
					end := strings.IndexAny(rest, " \t(")
					if end < 0 {
						end = len(rest)
					}
					name := rest[:end]
					if name == target {
						capturing = true
						found = true
						depth = strings.Count(line, "(") - strings.Count(line, ")")
						dst.WriteString(line)
						dst.WriteByte('\n')
						if depth <= 0 {
							capturing = false
							dst.WriteByte('\n')
						}
					}
				}
				continue
			}
			depth += strings.Count(line, "(") - strings.Count(line, ")")
			dst.WriteString(line)
			dst.WriteByte('\n')
			if depth <= 0 {
				capturing = false
				dst.WriteByte('\n')
			}
		}
		return found
	}

	// Pass 1: capture all requested symbols that are present in the WAT.
	matched := make(map[string]bool, len(symbols))
	for sym := range want {
		var block bytes.Buffer
		if captureFuncBlock(&block, sym) {
			matched[sym] = true
			out.Write(block.Bytes())
		}
	}

	// Pass 2: for symbols not found (inlined away by wasm-opt), attempt a
	// fallback to the function that contains the inlined code. TinyGo WASI
	// programs inline main.main and small single-call-site functions into
	// $runtime.run$1$gowrapper; $main and $main.main do not appear in the
	// final WAT after wasm-opt. Try candidates in priority order.
	var missing []string
	for sym := range want {
		if !matched[sym] {
			missing = append(missing, sym)
		}
	}

	if len(missing) > 0 {
		// Build the fallback candidate list. Static priority: $main.main first,
		// then the canonical gowrapper name. Additionally, scan the WAT for any
		// $runtime.run$N$gowrapper variant in case the index differs.
		gowrapperRe := regexp.MustCompile(`^\$runtime\.run\$[0-9]+\$gowrapper$`)
		seen := map[string]bool{"$main.main": true, "$runtime.run$1$gowrapper": true}
		fallbackCandidates := []string{"$main.main", "$runtime.run$1$gowrapper"}
		for _, line := range lines {
			trimmed := strings.TrimLeft(line, " \t")
			if !strings.HasPrefix(trimmed, "(func $") {
				continue
			}
			rest := trimmed[len("(func "):]
			end := strings.IndexAny(rest, " \t(")
			if end < 0 {
				end = len(rest)
			}
			name := rest[:end]
			if gowrapperRe.MatchString(name) && !seen[name] {
				seen[name] = true
				fallbackCandidates = append(fallbackCandidates, name)
			}
		}

		// Pick the first candidate that (a) exists in the WAT and (b) was not
		// already emitted as one of the matched requested symbols.
		fallbackName := ""
		for _, candidate := range fallbackCandidates {
			if matched[candidate] {
				// Already emitted as a matched symbol; try the next candidate.
				continue
			}
			var probe bytes.Buffer
			if captureFuncBlock(&probe, candidate) {
				fallbackName = candidate
				break
			}
		}

		sort.Strings(missing) // deterministic order in annotation
		if fallbackName != "" {
			// Emit a clear annotation then the fallback block so the user knows
			// which symbol was requested, why it is absent, and what they are
			// actually looking at.
			fmt.Fprintf(&out,
				";; (symbols inlined by wasm-opt: %s — showing %s, which contains the inlined code)\n",
				strings.Join(missing, ", "), fallbackName)
			captureFuncBlock(&out, fallbackName)
		} else {
			// No fallback available; keep the original last-resort behavior.
			for _, sym := range missing {
				fmt.Fprintf(&out, ";; (symbol not found: %s)\n", sym)
			}
		}
	}

	return os.WriteFile(watFile, out.Bytes(), 0o666)
}
