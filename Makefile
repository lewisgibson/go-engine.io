CPUS ?= $(shell (nproc --all || sysctl -n hw.ncpu) 2>/dev/null || echo 1)
MAKEFLAGS += --jobs=$(CPUS)

.PHONY: help
help: ## Display available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: hooks
hooks: ## Install git hooks
	@cp scripts/pre-commit .git/hooks/pre-commit
	@chmod +x .git/hooks/pre-commit
	@if [ -f scripts/pre-push ]; then cp scripts/pre-push .git/hooks/pre-push && chmod +x .git/hooks/pre-push; fi
	@echo "Git hooks installed."

.PHONY: lint
lint: ## Lint files
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	@golangci-lint run ./...

.PHONY: lint-fix
lint-fix: ## Lint and auto-fix files
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	@golangci-lint run --fix ./...

.PHONY: format
format: ## Format files
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
	@golangci-lint fmt ./...

.PHONY: fakes
fakes: ## Generate fakes for testing
	@go install go.uber.org/mock/mockgen@v0.6.0
	@go generate ./...

.PHONY: build
build: ## Build all packages
	@go build -trimpath ./...

.PHONY: build-examples
build-examples: ## Build the framework example programs (separate module)
	@cd examples && go build -trimpath ./...

.PHONY: test
test: ## Run unit tests without coverage or race detector
	@go test -trimpath $$(go list -tags=!interop -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)

.PHONY: unit-test
unit-test: ## Run unit tests with the race detector, coverage, and a JUnit report
	@command -v gotestsum >/dev/null 2>&1 || go install gotest.tools/gotestsum@v1.13.0
	@mkdir -p coverage
	@gotestsum --junitfile coverage/unit.xml --format pkgname -- \
		-trimpath -race -count=1 -covermode=atomic \
		-coverprofile=coverage/unit.cov \
		$$(go list -tags=!interop -f '{{if or .TestGoFiles .XTestGoFiles}}{{.ImportPath}}{{end}}' ./...)

.PHONY: interop
interop: ## Run the JS engine.io interoperability tests (requires Node.js)
	@npm --prefix test/interop ci
	@go test -trimpath -tags interop -race ./test/interop/...

.PHONY: vendor
vendor: ## Tidy and re-vendor dependencies
	@go mod tidy
	@go mod vendor
	@go mod verify

.PHONY: vulncheck
vulncheck: ## Scan for known vulnerabilities
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@v1.3.0
	@govulncheck ./...

.PHONY: coverage
coverage: unit-test ## Generate an HTML coverage report
	@go tool cover -html=coverage/unit.cov -o coverage/coverage.html
