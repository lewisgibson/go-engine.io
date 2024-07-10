SHA ?= $(shell git rev-parse HEAD)

CPUS ?= $(shell (nproc --all || sysctl -n hw.ncpu) 2>/dev/null || echo 1)
MAKEFLAGS += --jobs=$(CPUS)

.PHONY: help
help: ## Display available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

.PHONY: fakes
fakes: ## Generate fakes for testing
	@go install go.uber.org/mock/mockgen@latest
	@go generate ./...

.PHONY: build
build: ## Build the application
	@go build ./...

.PHONY: test
test: ## Run tests
	@go test ./...

.PHONY: lint
lint: ## Lint files
	@golangci-lint run ./...

.PHONY: coverage
coverage: coverage-html coverage-xml ## Generate and report coverage

.PHONY: coverage-profile
coverage-profile:
	@mkdir -p coverage
	@go test -cover -covermode=count -coverprofile=coverage/profile.cov ./...

.PHONY: coverage-html
coverage-html: coverage-profile
	@go tool cover -html=coverage/profile.cov -o coverage/coverage.html

.PHONY: coverage-xml
coverage-xml: coverage-profile
	@go install github.com/axw/gocov/gocov@latest
	@go install github.com/AlekSi/gocov-xml@latest
	@gocov convert coverage/profile.cov | gocov-xml > coverage/coverage.xml

