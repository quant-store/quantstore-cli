GO ?= go
# POSIX convenience entrypoint. Windows uses build.ps1.
VERSION ?= dev
BUILD_DATE ?= unknown
LDFLAGS := -s -w \
	-X github.com/quant-store/quantstore-cli/core/internal/version.Version=$(VERSION) \
	-X github.com/quant-store/quantstore-cli/core/internal/version.BuildDate=$(BUILD_DATE)

.PHONY: build test vet tidy clean

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -buildvcs=false -trimpath -ldflags '$(LDFLAGS)' -o bin/qs ./cmd/qs

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf bin
