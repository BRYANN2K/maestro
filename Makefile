GO ?= go
GOPATH := $(shell $(GO) env GOPATH)
GOIMPORTS := $(GOPATH)/bin/goimports
STATICCHECK := $(GOPATH)/bin/staticcheck
GOVULNCHECK := $(GOPATH)/bin/govulncheck
GO_FILE_FIND := find . -type f -name '*.go'

.PHONY: build build-check test test-fast vet lint vuln fmt fmt-check tidy tidy-check verify check-licenses tools check-go check release-check npm-test npm-pack clean

runtime-install:
	bun install --cwd runtime --frozen-lockfile --ignore-scripts

runtime-build: runtime-install
	bun scripts/build-runtime.ts

runtime-test: runtime-build
	MAESTRO_RUNTIME="$(CURDIR)/bin/maestro-runtime" $(GO) test ./internal/agentcore ./internal/orchestrator -run "TestBundled|TestStipulate" -count=1
	bun test runtime/test

ui-install:
	bun install --cwd ui --frozen-lockfile --ignore-scripts

ui-build: ui-install
	bun scripts/build-ui.ts

ui-test: runtime-build ui-build
	bun run --cwd ui typecheck
	bun test ui/tests
	MAESTRO_RUNTIME="$(CURDIR)/bin/maestro-runtime" MAESTRO_UI="$(CURDIR)/bin/maestro-ui" $(GO) test ./internal/cockpit ./internal/workflow ./cmd/maestro -run "TestCockpit|TestApprovalRejects|TestOpenTUI" -race -count=1

build: runtime-build ui-build
	mkdir -p bin
	$(GO) build -o bin/maestro ./cmd/maestro

test:
	$(GO) test ./... -race -count=1

test-fast:
	$(GO) test ./... -count=1

vet:
	$(GO) vet ./...

lint:
	@test -x "$(STATICCHECK)" || { echo "staticcheck missing; run 'make tools'" >&2; exit 1; }
	$(STATICCHECK) ./...

vuln:
	@test -x "$(GOVULNCHECK)" || { echo "govulncheck missing; run 'make tools'" >&2; exit 1; }
	$(GOVULNCHECK) ./...

fmt:
	@$(GO_FILE_FIND) -print0 | xargs -0 gofmt -w
	@$(GO_FILE_FIND) -print0 | xargs -0 $(GOIMPORTS) -local github.com/bryann2k/maestro -w

fmt-check:
	@test -x "$(GOIMPORTS)" || { echo "goimports missing; run 'make tools'" >&2; exit 1; }
	@files="$$( $(GO_FILE_FIND) -print0 | xargs -0 gofmt -l )"; \
		test -z "$$files" || { echo "gofmt required:" >&2; echo "$$files" >&2; exit 1; }
	@files="$$( $(GO_FILE_FIND) -print0 | xargs -0 $(GOIMPORTS) -local github.com/bryann2k/maestro -l )"; \
		test -z "$$files" || { echo "goimports required:" >&2; echo "$$files" >&2; exit 1; }

tidy:
	$(GO) mod tidy

tidy-check:
	$(GO) mod tidy -diff

verify:
	$(GO) mod verify

check-licenses:
	$(GO) run ./scripts/check_licenses.go

build-check:
	@task_out="$$(mktemp -d)"; trap 'rm -rf "$$task_out"' EXIT; \
		$(GO) build -trimpath -o "$$task_out/maestro" ./cmd/maestro

tools:
	GOBIN=$(GOPATH)/bin $(GO) install golang.org/x/tools/cmd/goimports@v0.48.0
	GOBIN=$(GOPATH)/bin $(GO) install honnef.co/go/tools/cmd/staticcheck@2026.1
	GOBIN=$(GOPATH)/bin $(GO) install golang.org/x/vuln/cmd/govulncheck@v1.6.0

check-go: fmt-check tidy-check verify check-licenses vet lint test build-check

check: check-go npm-test runtime-test ui-test

release-check: check vuln npm-pack
	@echo "release-check: formatting, modules, licenses, vet, staticcheck, race, build, vulnerabilities, and npm package are green"

npm-test:
	cd npm && npm test

npm-pack:
	cd npm && npm pack --dry-run

clean:
	rm -rf bin coverage.*
