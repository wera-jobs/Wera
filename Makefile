.PHONY: build test cover lint vuln migrate fetch score pipeline serve worker bench deep clean

# All configuration comes from the environment or a local .env file,
# which the binary loads itself. Nothing secret belongs in this Makefile.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X wera/internal/buildinfo.Version=$(VERSION) -X wera/internal/buildinfo.Commit=$(COMMIT)

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/wera ./cmd/wera

# Database tests need WERA_TEST_DATABASE_URL (a copy such as wera_dev,
# never the live database). Packages run one at a time because they share
# that database.
test:
	scripts/test.sh

cover:
	scripts/test.sh
	go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...

migrate:
	go run ./cmd/wera migrate

fetch:
	go run ./cmd/wera fetch

score:
	go run ./cmd/wera score

pipeline:
	go run ./cmd/wera pipeline

serve:
	go run ./cmd/wera serve

worker:
	go run ./cmd/wera worker

bench:
	go run ./cmd/wera bench

deep:
	go run ./cmd/wera deep

clean:
	rm -rf bin
