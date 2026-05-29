VERSION ?= dev
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X vtunnel/internal/cli.version=$(VERSION) -X vtunnel/internal/cli.commit=$(COMMIT) -X vtunnel/internal/cli.date=$(DATE)

.PHONY: build test release clean

build:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/vtunnel ./cmd/vtunnel

test:
	go test ./...

release:
	./scripts/release.sh $(VERSION)

clean:
	rm -rf bin dist
