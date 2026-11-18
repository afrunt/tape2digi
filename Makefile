SHELL := /bin/bash

APP      := tape2digi
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)
WAILS    ?= $(shell command -v wails || echo $(HOME)/go/bin/wails)
LLVM_MINGW ?= $(firstword $(wildcard $(HOME)/.local/toolchains/llvm-mingw-*))
TARGETS  := darwin-amd64 darwin-arm64 windows-amd64 windows-arm64 linux-amd64 linux-arm64
DIST_TARGETS := $(addprefix dist-,$(TARGETS))
HOST_OS  := $(shell uname -s)

.PHONY: dist clean doctor frontend $(addprefix dist-,$(TARGETS))

dist: $(addprefix dist-,$(TARGETS))
	@echo "Artifacts:" && ls -1 dist

frontend:
	cd frontend && npm install --no-audit --no-fund && npm run build

doctor:
	@fail=0; \
	check() { command -v "$$1" >/dev/null 2>&1 || { echo "missing: $$1 ($$2)"; fail=1; }; }; \
	check go "install Go 1.26+ (GOTOOLCHAIN=auto fetches the exact toolchain)"; \
	check node "install Node.js 20+"; \
	check npm "install Node.js 20+"; \
	check zip "install zip"; \
	check git "install git"; \
	[ -x "$(WAILS)" ] || { echo "missing: wails (go install github.com/wailsapp/wails/v2/cmd/wails@latest)"; fail=1; }; \
	[ "$(HOST_OS)" = Darwin ] || { echo "missing: macOS host (the dist build is designed to run on a Mac)"; fail=1; }; \
	check clang "install Xcode command line tools: xcode-select --install"; \
	[ -x "$(LLVM_MINGW)/bin/x86_64-w64-mingw32-clang" ] || { echo "missing: llvm-mingw in ~/.local/toolchains (https://github.com/mstorsjo/llvm-mingw/releases, ucrt macos-universal) or set LLVM_MINGW=<dir>"; fail=1; }; \
	check docker "install and start Docker Desktop"; \
	docker info >/dev/null 2>&1 || { echo "docker is not running: start Docker Desktop"; fail=1; }; \
	[ $$fail = 0 ] && echo "doctor: all prerequisites found"; exit $$fail

dist-darwin-amd64: ARCH := x86_64
dist-darwin-arm64: ARCH := arm64
$(filter dist-darwin-%,$(DIST_TARGETS)): dist-darwin-%: frontend
	@mkdir -p dist
	CGO_ENABLED=1 CC="clang -arch $(ARCH)" $(WAILS) build -clean -platform darwin/$* -ldflags "$(LDFLAGS)" -s
	rm -f dist/$(APP)-$(VERSION)-darwin-$*.zip
	ditto -c -k --keepParent build/bin/$(APP).app dist/$(APP)-$(VERSION)-darwin-$*.zip
	rm -rf dist/$(APP).app

dist-windows-amd64: TRIPLE := x86_64
dist-windows-arm64: TRIPLE := aarch64
$(filter dist-windows-%,$(DIST_TARGETS)): dist-windows-%: frontend
	@mkdir -p dist
	@test -x "$(LLVM_MINGW)/bin/$(TRIPLE)-w64-mingw32-clang" || { echo "llvm-mingw not found: run 'make doctor'"; exit 1; }
	CGO_ENABLED=1 CC="$(LLVM_MINGW)/bin/$(TRIPLE)-w64-mingw32-clang" $(WAILS) build -clean -platform windows/$* -ldflags "$(LDFLAGS)" -s
	rm -f dist/$(APP)-$(VERSION)-windows-$*.zip
	cd build/bin && zip -q ../../dist/$(APP)-$(VERSION)-windows-$*.zip $(APP).exe

$(filter dist-linux-%,$(DIST_TARGETS)): dist-linux-%: frontend
	@mkdir -p dist build/bin/linux-$*
	docker build --platform linux/$* -f build/docker/linux.Dockerfile -t $(APP)-build-$* build/docker
	docker run --rm --platform linux/$* -v "$(CURDIR)":/src -v $(APP)-gocache-$*:/root/.cache/go-build -v $(APP)-gomod-$*:/go/pkg/mod \
		-e CGO_ENABLED=1 $(APP)-build-$* \
		go build -buildvcs=false -tags desktop,production,webkit2_41 -ldflags "-s -w $(LDFLAGS)" -o build/bin/linux-$*/$(APP) .
	rm -f dist/$(APP)-$(VERSION)-linux-$*.tar.gz
	tar -C build/bin/linux-$* -czf dist/$(APP)-$(VERSION)-linux-$*.tar.gz $(APP)

clean:
	rm -rf dist build/bin frontend/dist
