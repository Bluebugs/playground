#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

TINYGO_BIN="$REPO_ROOT/tinygo/build/tinygo"
GO_BIN="$REPO_ROOT/go/bin"

# 1. Verify the forked toolchain exists.
if [ ! -x "$TINYGO_BIN" ]; then
  echo "ERROR: $TINYGO_BIN not found." >&2
  echo "Run 'make build' from the repository root ($REPO_ROOT) first." >&2
  exit 1
fi

# 2. Start the playground server in the background.
cd "$SCRIPT_DIR"
PATH="$GO_BIN:$REPO_ROOT/tinygo/build:$PATH" \
  GOEXPERIMENT=spmd \
  go run . &
SERVER_PID=$!

# Ensure the server is killed on exit regardless of how this script terminates.
trap "kill $SERVER_PID 2>/dev/null || true" EXIT

# 3. Poll /api/examples until the server is ready (max 30s).
echo "Waiting for server to start..."
ready=0
for i in $(seq 1 30); do
  if curl -fsS "http://localhost:8080/api/examples" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done

if [ "$ready" -eq 0 ]; then
  echo "ERROR: server did not become ready within 30 seconds" >&2
  exit 1
fi
echo "Server is ready."

# 4. Run the endpoint smoke tests.
"$SCRIPT_DIR/test_endpoints.sh" "http://localhost:8080"
EXIT_CODE=$?

# trap will kill the server.
exit "$EXIT_CODE"
