# tamesu - multi-module monorepo Makefile.
# Each module is built/tested independently with the workspace disabled, matching CI.

# All Go modules in the repo, discovered dynamically: every directory with a
# go.mod, excluding the docs site, node_modules, and testdata. New modules are
# picked up automatically, so this never needs updating.
MODULES := $(shell find . -name go.mod \
	-not -path './site/*' \
	-not -path './node_modules/*' \
	-not -path '*/testdata/*' \
	-exec dirname {} \; | sort)

GO ?= go
export GOWORK = off

.PHONY: all build test race vet lint lint-integration fmt tidy work-sync check-deps check-claims schema schema-check proto notices test-llmfake vet-llmfake lint-llmfake integration integration-sip genkit-ui site-dev site-build site-linkcheck demo clean help

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

all: fmt vet check-deps check-claims schema-check test lint lint-integration test-llmfake ## What CI runs: fmt, vet, check-deps, check-claims, schema-check, test, lint and the llmfake suites

build: ## go build every module (the binary is cmd/tamesu/tamesu)
	@for m in $(MODULES); do echo "==> build $$m"; (cd $$m && $(GO) build ./...) || exit 1; done
	@(cd cmd/tamesu && $(GO) build -tags onnx -o tamesu .)

test: ## go test every module
	@for m in $(MODULES); do echo "==> test $$m"; (cd $$m && $(GO) test ./...) || exit 1; done

race: ## go test -race every module
	@for m in $(MODULES); do echo "==> test -race $$m"; (cd $$m && $(GO) test -race ./...) || exit 1; done

vet: ## go vet every module (default and integration build tags)
	@for m in $(MODULES); do echo "==> vet $$m"; (cd $$m && $(GO) vet ./... && $(GO) vet -tags integration ./...) || exit 1; done
	@echo "==> vet -tags onnx audio"; (cd audio && $(GO) vet -tags onnx ./...)

lint: ## golangci-lint every module (requires golangci-lint)
	@for m in $(MODULES); do echo "==> lint $$m"; (cd $$m && golangci-lint run) || exit 1; done

lint-integration: ## golangci-lint -tags integration for every module (the container-backed suites)
	@for m in $(MODULES); do echo "==> lint -tags integration $$m"; (cd $$m && golangci-lint run --build-tags integration) || exit 1; done

fmt: ## Check the tree is gofmt-clean (fails and lists files instead of rewriting them)
	@out=$$(gofmt -l $$(find . -name '*.go' -not -path './site/*' -not -path '*/node_modules/*')); \
	if [ -n "$$out" ]; then \
		echo "not gofmt-clean:"; echo "$$out"; exit 1; \
	fi

tidy: ## go mod tidy every module
	@for m in $(MODULES); do echo "==> tidy $$m"; (cd $$m && $(GO) mod tidy) || exit 1; done

work-sync: ## Regenerate the go.work workspace (then re-tidy modules)
	@GOWORK= $(GO) work sync
	@$(MAKE) tidy

check-deps: ## Fail if the root module pulls a heavy SDK, or a module other than llm imports a model provider SDK
	@bash .github/scripts/check_root_deps.sh .
	@bash .github/scripts/check_ai_deps.sh

check-claims: ## Fail if the README, docs or skill claim a command, flag or target the CLI does not have
	@bash .github/scripts/check_readme_claims.sh
	@bash .github/scripts/check_docs_claims.sh

SUITES_PAGE := site/src/pages/docs/suites.md

schema: ## Regenerate schema/*.schema.json and the suites reference page from the Go structs
	@$(GO) run ./schema/gen.go schema $(SUITES_PAGE)

schema-check: ## Fail if the committed schemas or the suites page differ from what `make schema` generates
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	$(GO) run ./schema/gen.go "$$tmp/schema" "$$tmp/suites.md" || exit 1; \
	stale=0; \
	for f in tamesu.schema.json tamesu-config.schema.json; do diff -u "schema/$$f" "$$tmp/schema/$$f" || stale=1; done; \
	diff -u "$(SUITES_PAGE)" "$$tmp/suites.md" || stale=1; \
	if [ "$$stale" -ne 0 ]; then echo "schema is stale: run make schema and commit the result"; exit 1; fi; \
	echo "schema-check: schema/ and $(SUITES_PAGE) are up to date"

# The end-to-end tests with the fake model provider (no network, no keys) build
# only with -tags llmfake, so a default build never contains the fake provider.
# Entries are "module dir:packages"; modules join the list as they gain llmfake tests.
LLMFAKE := .:./... llm:./... cmd/tamesu:./...

test-llmfake: ## go test -tags llmfake for the fake-provider suites
	@for p in $(LLMFAKE); do d=$${p%%:*}; pkgs=$${p#*:}; echo "==> test -tags llmfake $$d $$pkgs"; \
		(cd $$d && $(GO) test -timeout 30m -tags llmfake $$pkgs) || exit 1; done

vet-llmfake: ## go vet -tags llmfake for the fake-provider suites
	@for p in $(LLMFAKE); do d=$${p%%:*}; pkgs=$${p#*:}; echo "==> vet -tags llmfake $$d $$pkgs"; \
		(cd $$d && $(GO) vet -tags llmfake $$pkgs) || exit 1; done

lint-llmfake: ## golangci-lint -tags llmfake for the fake-provider suites
	@for p in $(LLMFAKE); do d=$${p%%:*}; pkgs=$${p#*:}; echo "==> lint -tags llmfake $$d $$pkgs"; \
		(cd $$d && golangci-lint run --build-tags llmfake $$pkgs) || exit 1; done

integration: ## Run the example suites end to end with the fake model provider (scripts/integration.sh): rest, livekit text and audio
	@bash scripts/integration.sh

notices: ## Regenerate THIRD_PARTY_LICENSES.csv (go-licenses on the CLI module)
	@command -v go-licenses >/dev/null || { echo "go-licenses is not installed: go install github.com/google/go-licenses@latest"; exit 1; }
	@(cd cmd/tamesu && GOWORK=off go-licenses report . --ignore github.com/xavidop/tamesu 2>/dev/null | sort) > THIRD_PARTY_LICENSES.csv
	@echo "THIRD_PARTY_LICENSES.csv: $$(wc -l < THIRD_PARTY_LICENSES.csv | tr -d ' ') modules"

proto: ## Regenerate the Go code of proto/ and the vendored Pipecat frames (protoc, protoc-gen-go and protoc-gen-go-grpc on the PATH)
	@command -v protoc >/dev/null || { echo "protoc is not installed"; exit 1; }
	@PATH="$$PATH:$$($(GO) env GOPATH)/bin" protoc -I proto \
		--go_out=targets/grpc/turnv1 --go_opt=paths=source_relative \
		--go-grpc_out=targets/grpc/turnv1 --go-grpc_opt=paths=source_relative \
		proto/tamesu/turn/v1/turn.proto
	@mv targets/grpc/turnv1/tamesu/turn/v1/*.go targets/grpc/turnv1/ && rm -r targets/grpc/turnv1/tamesu
	@PATH="$$PATH:$$($(GO) env GOPATH)/bin" protoc -I targets/pipecat/frames \
		--go_out=targets/pipecat/frames --go_opt=paths=source_relative targets/pipecat/frames/frames.proto

integration-sip: ## Run the phone example through Docker (livekit-server, livekit/sip, Asterisk): tamesu sip setup, outbound and inbound calls
	@bash scripts/integration-sip.sh

genkit-ui: ## Run an example suite under `genkit start` to inspect traces in the Genkit Developer UI
	@command -v genkit >/dev/null || { echo "genkit CLI is not installed: npm i -g genkit-cli"; exit 1; }
	@if [ ! -d llm ]; then echo "genkit-ui: the llm module does not exist yet"; exit 1; fi
	@(cd cmd/tamesu && $(GO) build -o ../../bin/tamesu .)
	@genkit start -- ./bin/tamesu run examples/rest-agent/suite.yaml --reporter console

site-dev: ## Run the docs site dev server (Node 22 via nvm)
	@bash -lc 'source "$$HOME/.nvm/nvm.sh" && nvm use 22 && cd site && npm install && npm run dev'

site-build: ## Build the docs site (Node 22 via nvm)
	@bash -lc 'source "$$HOME/.nvm/nvm.sh" && nvm use 22 && cd site && npm install && npm run build'

site-linkcheck: ## Build the docs site and check for broken internal links (Node 22 via nvm)
	@bash -lc 'source "$$HOME/.nvm/nvm.sh" && nvm use 22 && cd site && npm install && npm run build && npm run linkcheck'

demo: ## Record site/public/demo.gif with vhs (needs vhs, ttyd and ffmpeg)
	@command -v vhs >/dev/null || { echo "vhs is not installed: brew install vhs (or see https://github.com/charmbracelet/vhs#installation)"; exit 1; }
	@mkdir -p bin site/public
	@(cd cmd/tamesu && $(GO) build -o ../../bin/tamesu .)
	@bash scripts/demo-seed.sh ./bin/tamesu /tmp/tamesu-demo
	@vhs demo.tape

clean: ## Remove build artifacts
	@rm -rf dist bin completions cmd/tamesu/tamesu site/dist site/.astro .tamesu
