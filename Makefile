GO ?= go
GO_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*')

.PHONY: check check-g0 fmt-check test test-race vet build dependencies

check: check-g0 build

check-g0: fmt-check dependencies test test-race vet

fmt-check:
	@test -z "$$(gofmt -l $(GO_FILES))" || { \
		echo "gofmt required:" >&2; \
		gofmt -l $(GO_FILES) >&2; \
		exit 1; \
	}

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

dependencies:
	$(GO) mod verify
	$(GO) list -m -mod=readonly all >/dev/null

build:
	$(GO) build ./cmd/rvw
