GO ?= go
GO_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*')
RVW := bin/rvw

.PHONY: check check-g0 fmt-check test test-e2e test-e2e-update test-race vet build dependencies

check: check-g0 build test-e2e

check-g0: fmt-check dependencies test test-race vet

fmt-check:
	@test -z "$$(gofmt -l $(GO_FILES))" || { \
		echo "gofmt required:" >&2; \
		gofmt -l $(GO_FILES) >&2; \
		exit 1; \
	}

test:
	$(GO) test ./...

test-e2e:
	$(GO) test -count=1 -tags=e2e ./e2e

test-e2e-update:
	$(GO) test -count=1 -tags=e2e ./e2e -update

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

dependencies:
	$(GO) mod verify
	$(GO) list -m -mod=readonly all >/dev/null

build: $(RVW)

$(RVW): $(GO_FILES) go.mod go.sum
	@mkdir -p $(@D)
	$(GO) build -o $@ ./cmd/rvw
