# SPMD Playground container.
#
# Bundles the forked Go (GOROOT) + forked TinyGo + binaryen >=109 + wabt + binutils.
# The forked toolchain is delivered via release-spmd.tar.gz (built by `make release-spmd.tar.gz`
# from the parent repo). Inside the image it extracts to /app/go and /app/tinygo.
#
# Platform constraint: this image is pinned to linux/amd64 because the binaryen
# and wabt release tarballs we download are x86_64-linux only, and the bundled
# forked toolchain in release-spmd.tar.gz is built for amd64. Do not change the
# --platform pins without also providing matching toolchain artifacts.

# ---------- Stage 1: build the server binary ----------

FROM --platform=linux/amd64 golang:1.22-bookworm AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN go build -o main .

# ---------- Stage 2: runtime image ----------

FROM --platform=linux/amd64 debian:bookworm-slim AS runtime

# Versions of the WebAssembly tooling we bundle.
# binaryen >= 109 is required so that wasm-opt understands the modern SIMD opcodes
# emitted by the SPMD compiler (relaxed-simd, i8x16.swizzle, vpmaddubsw lowerings).
# Older binaryen builds (e.g. the bookworm package) reject them with
# "invalid code after SIMD prefix" and break /api/wat for table-lookup and
# base64-mula-lemire.
ARG BINARYEN_VERSION=version_119
ARG WABT_VERSION=1.0.36

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        wget \
        binutils \
    && rm -rf /var/lib/apt/lists/*

# Install binaryen (>=109) -- wasm-opt needs to support modern SIMD opcodes.
RUN wget -q "https://github.com/WebAssembly/binaryen/releases/download/${BINARYEN_VERSION}/binaryen-${BINARYEN_VERSION}-x86_64-linux.tar.gz" \
        -O /tmp/binaryen.tar.gz && \
    mkdir -p /usr/local/binaryen && \
    tar -xzf /tmp/binaryen.tar.gz -C /usr/local/binaryen --strip-components=1 && \
    ln -sf /usr/local/binaryen/bin/wasm-opt /usr/local/bin/wasm-opt && \
    rm /tmp/binaryen.tar.gz

# Install wabt (wasm2wat) -- newer than what bookworm ships, so we recognize current opcodes.
RUN wget -q "https://github.com/WebAssembly/wabt/releases/download/${WABT_VERSION}/wabt-${WABT_VERSION}-ubuntu-20.04.tar.gz" \
        -O /tmp/wabt.tar.gz && \
    mkdir -p /usr/local/wabt && \
    tar -xzf /tmp/wabt.tar.gz -C /usr/local/wabt --strip-components=1 && \
    ln -sf /usr/local/wabt/bin/wasm2wat /usr/local/bin/wasm2wat && \
    rm /tmp/wabt.tar.gz

# Non-root user. We create the account early but defer the chown until after
# all file-bringing steps (ADD/COPY) have populated /app, otherwise the
# extracted toolchain and copied assets would be owned by root.
RUN adduser --disabled-login --system --home /app appuser

# Extract the forked toolchain. release-spmd.tar.gz contains two top-level dirs:
#   go/      forked Go GOROOT (SPMD frontend)
#   tinygo/  forked TinyGo (SPMD backend, from build/release/tinygo)
ADD release-spmd.tar.gz /app/

# Server binary.
COPY --from=build /build/main /app/main

# Frontend assets.
COPY index.html dashboard.css dashboard.js highlight-wat.js highlight-x86.js /app/frontend/
COPY resources /app/frontend/resources
COPY worker /app/frontend/worker
COPY examples /app/frontend/examples
COPY tinygo-template /app/tinygo-template

# Make sure tinygo-template's go.mod deps are populated.
WORKDIR /app/tinygo-template
RUN GOEXPERIMENT=spmd /app/go/bin/go mod download || true

# The forked Go must come first on PATH so TinyGo's `go env` finds the SPMD-aware compiler.
ENV PATH="/app/go/bin:/app/tinygo/bin:${PATH}"
ENV GOEXPERIMENT=spmd
ENV GOROOT=/app/go

# Now that every file-bringing step has run, hand /app (toolchain + assets +
# binary + cache dir) over to the non-root user.
RUN mkdir -p /app/.cache && chown -R appuser /app

USER appuser
WORKDIR /app

# Warm the cache: validates the toolchain works inside the container.
# table-lookup is included because it exercises i8x16.relaxed_swizzle / vpshufb
# lowerings that require binaryen >= 109. A misconfigured binaryen will fail
# the build here rather than on the first user request.
RUN tinygo build -target=wasi -simd=true  -o /tmp/foo.wasm /app/frontend/examples/spmd/simple-sum/main.go && \
    tinygo build -target=wasi -simd=true  -o /tmp/foo.wasm /app/frontend/examples/spmd/table-lookup/main.go && \
    tinygo build -target=wasi -simd=false -o /tmp/foo.wasm /app/frontend/examples/spmd/simple-sum/main.go && \
    rm /tmp/foo.wasm

EXPOSE 8080
CMD ["./main", "-dir=/app/frontend", "-cache-type=local"]
