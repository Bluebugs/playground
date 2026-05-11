// Command playground runs a TinyGo compiler as an API that can be used from a
// web application.
package main

// This file implements the HTTP frontend.

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
)

const (
	cacheTypeLocal = iota + 1 // Cache to a local directory
	cacheTypeGCS              // Google Cloud Storage
)

var (
	// The channel to submit compile jobs to.
	compilerChan chan compilerJob

	// The cache directory where cached wasm files are stored.
	cacheDir string

	// The cache type: local or Google Cloud Storage.
	cacheType int

	bucket *storage.BucketHandle

	firebaseCredentials string

	// servingDir is the directory the static file server and manifest loader read from.
	servingDir string
)

func main() {
	// Create a build cache directory.
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatalln("could not find temporary directory:", err)
	}
	cacheDir = filepath.Join(userCacheDir, "tinygo-playground")
	err = os.MkdirAll(cacheDir, 0777)
	if err != nil {
		log.Fatalln("could not create temporary directory:", err)
	}

	dir := flag.String("dir", ".", "which directory to serve from")
	cacheTypeFlag := flag.String("cache-type", "local", "cache type (local, gcs)")
	bucketNameFlag := flag.String("bucket-name", "", "Google Cloud Storage bucket name")
	flag.StringVar(&firebaseCredentials, "firebase-credentials", "", "path to JSON file with Firebase credentials")
	flag.Parse()
	servingDir = *dir

	switch *cacheTypeFlag {
	case "local":
		cacheType = cacheTypeLocal
	case "gcs":
		cacheType = cacheTypeGCS
		ctx := context.Background()
		client, err := storage.NewClient(ctx)
		if err != nil {
			log.Fatalln("could not create Google Cloud Storage client:", err)
		}
		bucket = client.Bucket(*bucketNameFlag)
	default:
		log.Fatalln("unrecognized cache type:", *cacheTypeFlag)
	}

	// Start the compiler goroutine in the background, that will serialize all
	// compile jobs.
	compilerChan = make(chan compilerJob)
	go backgroundCompiler(compilerChan)

	// Run the web server.
	http.HandleFunc("/api/compile", handleCompile)
	http.HandleFunc("/api/wat", handleWAT)
	http.HandleFunc("/api/asm", handleASM)
	http.HandleFunc("/api/examples", handleExamples)
	http.HandleFunc("/api/share", handleShare)
	http.HandleFunc("/api/stats", getStats)
	http.Handle("/", addHeaders(http.FileServer(http.Dir(*dir))))
	log.Print("Serving " + *dir + " on http://localhost:8080")
	http.ListenAndServe(":8080", nil)
}

func addHeaders(fs http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Add headers that enable greater accuracy of performance.now() in
		// Firefox.
		w.Header().Add("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Add("Cross-Origin-Embedder-Policy", "require-corp")

		fs.ServeHTTP(w, r)
	}
}

// addAPIHeaders sets the CORS and isolation headers required by all API endpoints.
func addAPIHeaders(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
}

// maxSourceBytes caps each request body to keep a malicious POST from OOMing the server.
const maxSourceBytes = 512 * 1024

// readSource extracts source code from a POST request: text/plain body or 'code' form field.
func readSource(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxSourceBytes)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		return io.ReadAll(r.Body)
	}
	return []byte(r.FormValue("code")), nil
}

// runCompileJob performs the full cache-lookup → submit → wait cycle for a
// compile job and returns the result file path or the error output bytes.
// Exactly one of (filename, errOutput) is non-empty on return; err signals an
// internal server error (not a compile error).
func runCompileJob(ctx context.Context, source []byte, compiler, target, format string, simd bool, symbols []string) (filename string, errOutput []byte, err error) {
	sourceHashRaw := sha256.Sum256(source)
	sourceHash := hex.EncodeToString(sourceHashRaw[:])

	filename = cacheFilename(compiler, target, sourceHash, format, simd, symbols)

	// Fast path: already cached locally.
	if _, statErr := os.Stat(filename); statErr == nil {
		return filename, nil, nil
	}

	job := compilerJob{
		Source:       source,
		SourceHash:   sourceHash,
		Filename:     filename,
		Compiler:     compiler,
		Target:       target,
		Format:       format,
		SIMD:         simd,
		Symbols:      symbols,
		Context:      ctx,
		ResultFile:   make(chan string),
		ResultErrors: make(chan []byte),
	}
	compilerChan <- job
	select {
	case fn := <-job.ResultFile:
		return fn, nil, nil
	case buf := <-job.ResultErrors:
		return "", buf, nil
	}
}

// handleCompile handles the /api/compile API endpoint. It first tries to serve
// from a cache and if that fails, compiles the submitted source code directly.
func handleCompile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "TinyGo-Page, TinyGo-Modified")

	var source []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "text/plain") {
		// Read the source from the POST request.
		var err error
		source, err = ioutil.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
	} else {
		// Read the source from a form parameter.
		source = []byte(r.FormValue("code"))
	}
	// Hash the source code, used for the build cache.
	sourceHashRaw := sha256.Sum256([]byte(source))
	sourceHash := hex.EncodeToString(sourceHashRaw[:])

	// Check 'format' parameter.
	format := r.FormValue("format")
	if format == "" {
		// backwards compatibility (the format should be specified)
		format = "wasm"
	}
	flashFirmware := false
	switch format {
	case "wasm", "wasi":
		// Run code in the browser.
	case "elf", "hex", "uf2":
		// Build a firmware that can be flashed directly to a development board.
		flashFirmware = true
	default:
		// Unrecognized format. Disallow to be sure (might introduce security
		// issues otherwise).
		w.Write([]byte("unrecognized format"))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Check 'compiler' parameter.
	compiler := r.FormValue("compiler")
	if compiler == "" {
		compiler = "tinygo" // legacy fallback
	}
	switch compiler {
	case "go", "tinygo":
	default:
		// Unrecognized compiler.
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("unrecognized compiler"))
		return
	}

	// Parse simd flag — optional, default false for backwards compatibility.
	simd := r.FormValue("simd") == "true"

	// Track this compile action (after we're done compiling).
	defer trackCompile(map[string]any{
		"page":          r.Header.Get("TinyGo-Page"),
		"compiler":      compiler,
		"target":        r.FormValue("target"),
		"flashFirmware": flashFirmware,
		"timestamp":     time.Now().UTC().Truncate(time.Hour * 24),
	}, r.Header.Get("TinyGo-Modified"))

	// Attempt to serve directly from the directory with cached files.
	// Use the SIMD-aware cache filename so on/off artifacts don't collide.
	filename := cacheFilename(compiler, r.FormValue("target"), sourceHash, format, simd, nil)
	fp, err := os.Open(filename)
	if err == nil {
		// File was already cached! Serve it directly.
		defer fp.Close()
		sendCompiledResult(w, fp, format)
		return
	}

	// Create a new compiler job, which will be executed in a single goroutine
	// (to avoid overloading the system).
	job := compilerJob{
		Source:       source,
		SourceHash:   sourceHash,
		Filename:     filename,
		Compiler:     compiler,
		Target:       r.FormValue("target"),
		Format:       format,
		SIMD:         simd,
		Context:      r.Context(),
		ResultFile:   make(chan string),
		ResultErrors: make(chan []byte),
	}
	// Send the job for execution.
	compilerChan <- job
	// See how well that went, when it finishes.
	select {
	case filename := <-job.ResultFile:
		// Succesful compilation.
		fp, err := os.Open(filename)
		if err != nil {
			log.Println("could not open compiled file:", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer fp.Close()
		sendCompiledResult(w, fp, format)
	case buf := <-job.ResultErrors:
		// Failed compilation.
		w.Write(buf)
	}
}

// handleWAT handles POST /api/wat?simd=true|false
// Compiles the source to WASM and converts to WebAssembly text format via wasm2wat.
// Returns text/plain body with the WAT content (no gzip — text is small and easier to debug).
func handleWAT(w http.ResponseWriter, r *http.Request) {
	addAPIHeaders(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	source, err := readSource(w, r)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	simd := r.FormValue("simd") == "true"

	filename, errOutput, err := runCompileJob(r.Context(), source, "tinygo", "", "wat", simd, nil)
	if err != nil {
		log.Println("internal error running wat job:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(errOutput) > 0 {
		w.Write(errOutput)
		return
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		log.Println("could not read wat file:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

// handleASM handles POST /api/asm?simd=true|false&symbols=main.Foo,main.Bar
// Compiles the source for native x86-64 with AVX2 features and returns the
// objdump disassembly as text/plain (no gzip).
func handleASM(w http.ResponseWriter, r *http.Request) {
	addAPIHeaders(w)
	if r.Method == http.MethodOptions {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	source, err := readSource(w, r)
	if err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}

	simd := r.FormValue("simd") == "true"

	// Parse comma-separated symbols from query param (capped to keep one request
	// from monopolising the compile goroutine with hundreds of objdump invocations).
	const maxSymbols = 16
	var symbols []string
	if s := r.FormValue("symbols"); s != "" {
		for _, sym := range strings.Split(s, ",") {
			sym = strings.TrimSpace(sym)
			if sym != "" {
				symbols = append(symbols, sym)
			}
			if len(symbols) >= maxSymbols {
				break
			}
		}
	}

	filename, errOutput, err := runCompileJob(r.Context(), source, "tinygo", "", "asm-avx2", simd, symbols)
	if err != nil {
		log.Println("internal error running asm job:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if len(errOutput) > 0 {
		w.Write(errOutput)
		return
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		log.Println("could not read asm file:", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

// handleExamples handles GET /api/examples.
// Reads examples/manifest.json from disk and returns it as application/json.
// No caching at the HTTP layer — the file is small and updates during development.
func handleExamples(w http.ResponseWriter, r *http.Request) {
	addAPIHeaders(w)
	w.Header().Set("Content-Type", "application/json")

	data, err := os.ReadFile(filepath.Join(servingDir, "examples", "manifest.json"))
	if err != nil {
		log.Println("could not read examples manifest:", err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"manifest not found"}`))
		return
	}

	// Validate JSON so we don't serve garbage.
	if !json.Valid(data) {
		log.Println("examples/manifest.json is not valid JSON")
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"manifest invalid"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

// sendCompiledResult streams a wasm file while gzipping it during transfer.
func sendCompiledResult(w http.ResponseWriter, fp *os.File, format string) {
	switch format {
	case "wasm", "wasi":
		w.Header().Set("Content-Type", "application/wasm")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename=firmware."+format)
	}
	w.Header().Set("Content-Encoding", "gzip")
	gw := gzip.NewWriter(w)
	_, err := io.Copy(gw, fp)
	if err != nil {
		log.Println("could not read compiled file:", err)
		return
	}
	gw.Close()
}
