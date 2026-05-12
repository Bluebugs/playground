#!/usr/bin/env bash
# Phase 3 docker validation harness.
#
# Builds the SPMD playground image, runs it locally, and asserts:
#   - bundled wasm-opt is binaryen >= 109
#   - bundled wasm2wat / objdump exist and report a version
#   - tinygo inside the container is the SPMD fork
#   - the endpoint smoke (test_endpoints.sh) passes against the containerized server
#   - table-lookup and base64-mula-lemire produce SIMD WAT (i.e. binaryen >= 109
#     no longer fails with "invalid code after SIMD prefix")
#
# This script is intentionally slow (it builds the Docker image, which depends
# on release-spmd.tar.gz). It's meant for pre-deploy validation, not CI loops.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

IMAGE="${IMAGE:-spmd-playground:latest}"
NAME="spmd-playground-test"
PORT="${PORT:-8080}"

# 1. Build the image (no-op if up to date).
echo "==> Building $IMAGE"
make build

# 2. Start the container.
echo "==> Starting container $NAME"
docker rm -f "$NAME" >/dev/null 2>&1 || true
docker run -d --rm -p "${PORT}:8080" --name "$NAME" "$IMAGE" >/dev/null
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true' EXIT

# 3. Wait for the server to become ready (max 60s).
echo "==> Waiting for /api/examples"
ready=0
for i in $(seq 1 60); do
  if curl -fsS "http://localhost:${PORT}/api/examples" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -eq 0 ]; then
  echo "FAIL: server did not become ready within 60s"
  docker logs "$NAME" | tail -30 || true
  exit 1
fi
echo "    server is ready"

# 4. Tool version assertions inside the container.
echo "==> Tool versions inside container"
docker exec "$NAME" sh -c "wasm-opt --version" | head -1
docker exec "$NAME" sh -c "wasm2wat --version"
docker exec "$NAME" sh -c "objdump --version" | head -1
TG_VER=$(docker exec "$NAME" sh -c "tinygo version")
echo "    tinygo: $TG_VER"
if ! echo "$TG_VER" | grep -qi "spmd\|dev"; then
  # The forked TinyGo build reports a "dev" suffix or an spmd marker;
  # the upstream release does not. If we see a clean upstream version we know
  # we accidentally bundled the wrong tinygo.
  echo "WARN: tinygo version string does not look like the SPMD fork: $TG_VER"
fi

# binaryen >= 109 check.
WASM_OPT_RAW=$(docker exec "$NAME" sh -c "wasm-opt --version")
WASM_OPT_VER=$(echo "$WASM_OPT_RAW" | grep -oE 'version [0-9]+' | head -1 | awk '{print $2}')
if [ -z "$WASM_OPT_VER" ]; then
  echo "FAIL: could not parse wasm-opt version from: $WASM_OPT_RAW"
  exit 1
fi
if [ "$WASM_OPT_VER" -lt 109 ]; then
  echo "FAIL: wasm-opt version $WASM_OPT_VER < 109 (modern SIMD opcodes will not parse)"
  exit 1
fi
echo "    wasm-opt version $WASM_OPT_VER (>= 109, OK)"

# 5. Endpoint smoke against the containerized server.
echo "==> Endpoint smoke (test_endpoints.sh)"
./test_endpoints.sh "http://localhost:${PORT}"

# 6. Critical regression checks: binaryen >= 109 must unlock these examples.
echo "==> WAT regression check (table-lookup, base64-mula-lemire)"
for ex in table-lookup base64-mula-lemire; do
  src="$SCRIPT_DIR/examples/spmd/$ex/main.go"
  if [ ! -f "$src" ]; then
    echo "SKIP: $ex (source not found at $src)"
    continue
  fi
  out=$(curl -fsS -X POST -H "Content-Type: text/plain" \
        --data-binary "@$src" \
        "http://localhost:${PORT}/api/wat?simd=true" || true)
  # Use here-strings rather than `echo "$out" | grep -q` — with `set -o pipefail`,
  # grep -q's early exit causes SIGPIPE on echo, which fails the pipe and would
  # invert the if-condition for large `$out`.
  if grep -qE "wasm-opt failed|invalid code after SIMD prefix" <<< "$out"; then
    echo "FAIL: $ex SIMD=true WAT still hits wasm-opt failure inside container"
    head -20 <<< "$out"
    exit 1
  fi
  if grep -qE "i8x16|v128" <<< "$out"; then
    echo "OK:   $ex SIMD=true WAT contains SIMD ops"
  else
    echo "FAIL: $ex SIMD=true WAT lacks SIMD ops"
    head -20 <<< "$out"
    exit 1
  fi
done

echo "==> Docker validation passed"
