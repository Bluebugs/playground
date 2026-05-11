IMAGE ?= spmd-playground:latest
CONTAINER ?= spmd-playground

# Paths to the parent repo's forked toolchains.
REPO_ROOT := $(abspath $(CURDIR)/..)
FORKED_GO := $(REPO_ROOT)/go
FORKED_TINYGO := $(REPO_ROOT)/tinygo

all: build

# ---------------------------------------------------------------------------
# release-spmd.tar.gz: bundle the forked Go (GOROOT) + forked TinyGo redistributable.
#
# We rely on the parent repo's `make build` for the in-place TinyGo dev build
# (used by test_local.sh), and on TinyGo's own `make build/release` to produce
# the self-contained redistributable layout under tinygo/build/release/tinygo
# (bin/ + lib/ + pkg/ + src/ + targets/).
#
# Total tarball is ~700-900 MB compressed. The container needs the full Go
# GOROOT because TinyGo's `go env` invokes it for stdlib analysis at build
# time. We strip `.git`, `test/`, and `api/` which are not needed at runtime.
# ---------------------------------------------------------------------------

release-spmd.tar.gz:
	@echo ">>> Ensuring forked Go + TinyGo are built ($(REPO_ROOT))"
	$(MAKE) -C $(REPO_ROOT) build
	@echo ">>> Building TinyGo redistributable release"
	$(MAKE) -C $(FORKED_TINYGO) USE_SYSTEM_BINARYEN=1 build/release
	@echo ">>> Staging /tmp/spmd-toolchain"
	rm -rf /tmp/spmd-toolchain
	mkdir -p /tmp/spmd-toolchain
	cp -a $(FORKED_TINYGO)/build/release/tinygo /tmp/spmd-toolchain/tinygo
	cp -a $(FORKED_GO) /tmp/spmd-toolchain/go
	rm -rf /tmp/spmd-toolchain/go/.git \
	       /tmp/spmd-toolchain/go/test \
	       /tmp/spmd-toolchain/go/api \
	       /tmp/spmd-toolchain/go/doc \
	       /tmp/spmd-toolchain/go/test_constrained_lanes.o \
	       /tmp/spmd-toolchain/go/test_lanes_simple.o
	@echo ">>> Compressing release-spmd.tar.gz"
	tar -C /tmp/spmd-toolchain -czf $(CURDIR)/release-spmd.tar.gz tinygo go
	rm -rf /tmp/spmd-toolchain
	@ls -lh $(CURDIR)/release-spmd.tar.gz

.PHONY: build
build: release-spmd.tar.gz
	docker build -t $(IMAGE) .

.PHONY: run
run: build stop
	docker run --rm -p 8080:8080 -t --name=$(CONTAINER) $(IMAGE)

.PHONY: stop
stop:
	docker rm -f $(CONTAINER) || true

.PHONY: test-docker
test-docker:
	./test_docker.sh

.PHONY: push-docker
push-docker:
	docker push $(IMAGE)

.PHONY: push-gcloud
push-gcloud: release-spmd.tar.gz
ifndef GCP_PROJECT
	$(error GCP_PROJECT is not set. Invoke as `make push-gcloud GCP_PROJECT=your-project-id`)
endif
	gcloud builds submit --tag us-central1-docker.pkg.dev/$(GCP_PROJECT)/cloud-run-source-deploy/spmd-playground

# ---------------------------------------------------------------------------
# Frontend editor bundle (CodeMirror). Run `npm install` first.
# ---------------------------------------------------------------------------

resources/editor.bundle.js: editor/editor.js editor/tango.js package.json package-lock.json Makefile
	npx rollup editor/editor.js -f es -o resources/editor.bundle.js -p @rollup/plugin-node-resolve

resources/editor.bundle.min.js: resources/editor.bundle.js
	npx terser --compress --mangle --output=resources/editor.bundle.min.js resources/editor.bundle.js
