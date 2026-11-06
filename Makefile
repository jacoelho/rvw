GO ?= go
GO_FILES := $(shell find . -type f -name '*.go' -not -path './vendor/*' -not -path './.git/*')
BIN := $(CURDIR)/bin
GOLANGCI_LINT_VERSION := v2.15.0
RVW := $(BIN)/rvw
export GOBIN := $(BIN)

.PHONY: check fmt-check lint test test-race vet build dependencies web web-check

check: fmt-check dependencies web-check lint test test-race vet build

fmt-check:
	@test -z "$$(gofmt -l $(GO_FILES))" || { \
		echo "gofmt required:" >&2; \
		gofmt -l $(GO_FILES) >&2; \
		exit 1; \
	}

lint: $(BIN)/golangci-lint
	$(BIN)/golangci-lint run

$(BIN)/golangci-lint: go.mod .golangci.yml Makefile | $(BIN)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

dependencies:
	$(GO) mod verify
	$(GO) list -m -mod=readonly all >/dev/null

# web/dist is committed and embedded so `go install` needs no Node.
web: web/node_modules
	cd web && npm run build

web/node_modules: web/package.json web/package-lock.json
	cd web && npm ci
	@touch $@

# Fails when web/dist differs from a fresh build of web/src.
web-check: web
	@git diff --exit-code --stat -- web/dist || { echo "web/dist is stale: run make web and commit it" >&2; exit 1; }
	@test -z "$$(git ls-files --others --exclude-standard -- web/dist)" || { echo "web/dist has uncommitted files: run make web and commit it" >&2; exit 1; }

build: $(RVW)

$(RVW): $(GO_FILES) go.mod go.sum $(wildcard web/dist/*) | $(BIN)
	$(GO) build -o $@ ./cmd/rvw

$(BIN):
	mkdir -p $@
