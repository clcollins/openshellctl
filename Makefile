unexport GOFLAGS

GOOS ?= linux
GOARCH ?= amd64
GOPATH := $(shell go env GOPATH | awk -F: '{print $$1}')
HOME ?= $(shell echo ~)

# Build metadata injected via -ldflags.
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
OPENSHELL_PIN := $(shell awk '{print $$1}' hack/openshell-pin)

MODULE  := github.com/openshift-online/openshellctl
LDFLAGS := -s -w \
           -X main.version=$(VERSION) \
           -X main.commit=$(COMMIT) \
           -X main.openshellPin=$(OPENSHELL_PIN) \
           -extldflags=-zrelro \
           -extldflags=-znow

GOLANGCI_LINT_VERSION = v2.12.2

IMAGE           ?= openshellctl
IMAGE_TAG       ?= $(VERSION)
CONTAINER_ENGINE ?= podman

# E2E UX test image — validates the "three command" getting-started path in a
# Fedora user environment. See test/Containerfile.e2e and test/e2e-ux-test.sh.
E2E_IMAGE            ?= localhost/openshellctl-e2e
E2E_FEDORA_VERSION   ?= 44
# Pinned to the latest prerelease. Run `make e2e-bump` to update.
E2E_OPENSHELLCTL_VERSION  ?= 0.1.5-rc.2
E2E_OPENSHELLCTL_CHECKSUM ?= 221b18b37bd5be922b20869189555f007302db1c0e0dc7480665910ed506ab24
E2E_IMAGE_TAG             ?= $(E2E_OPENSHELLCTL_VERSION)
# Vault KV coordinates baked into the image (overridable at build time).
E2E_VAULT_KV_MOUNT     ?= osd-sre
E2E_VAULT_KV_PATH      ?= rosa-agent
E2E_VAULT_FIELD_PREFIX ?= hypershell
# VAULT_ADDR is required for make e2e (vault login -method=oidc runs inside the container).
# E2E_VAULT_CALLBACK_PORT: host port published to container port 8250 for the OIDC callback.
# Vault sends the browser to localhost:<callback-port>/oidc/callback after authentication.
# The Vault OIDC role's allowed_redirect_uris must include http://localhost:<port>/oidc/callback.
VAULT_ADDR              ?=
E2E_VAULT_CALLBACK_PORT ?= 8250
E2E_CHECKSUMS_URL = https://github.com/openshift-online/openshellctl/releases/download/v$(E2E_OPENSHELLCTL_VERSION)/openshellctl_$(E2E_OPENSHELLCTL_VERSION)_checksums.txt

# Ensure go modules are enabled:
export GO111MODULE = on
export GOPROXY = https://proxy.golang.org

# Disable CGO so that we always generate static binaries:
export CGO_ENABLED = 0

# Pass OPENSHELL_PIN to goreleaser via env (used in .goreleaser.yaml ldflags).
export OPENSHELL_PIN

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-24s\033[0m %s\n", $$1, $$2}'

.PHONY: all
all: verify test lint build ## Run verify, test, lint, build

.PHONY: build
build: ## Build the binary via goreleaser (snapshot, local arch)
	@echo "Building via goreleaser..."
	goreleaser build --snapshot --clean --single-target

.PHONY: install
install: ## Install to $(GOPATH)/bin via go build
	@echo "Installing to $(GOPATH)/bin/openshellctl..."
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(GOPATH)/bin/openshellctl ./cmd/openshellctl

.PHONY: install-local
install-local: build ## Install locally to ~/.local/bin
	@echo "Installing to $(HOME)/.local/bin/openshellctl..."
	@mkdir -p $(HOME)/.local/bin
	cp dist/*/openshellctl $(HOME)/.local/bin/openshellctl

.PHONY: test
test: ## Run unit tests
	@echo "Running tests..."
	go test ./... -count=1 $(TESTOPTS)

.PHONY: test-race
test-race: ## Run tests with race detector
	@echo "Running tests with race detector..."
	CGO_ENABLED=1 go test -race ./... -count=1 $(TESTOPTS)

.PHONY: coverage
coverage: ## Generate test coverage report
	@echo "Generating test coverage report..."
	go test ./... -coverprofile=coverage.out -covermode=atomic
	@echo "Coverage summary:"
	go tool cover -func=coverage.out
	@rm -f coverage.out

PKG_COVERAGE_THRESHOLD ?= 85
CLI_COVERAGE_THRESHOLD ?= 70

.PHONY: test-coverage-threshold
test-coverage-threshold: ## Enforce minimum coverage thresholds (pkg/ >= 85%, internal/cli >= 70%)
	@echo "Checking coverage thresholds..."
	@go test ./... -coverprofile=coverage.out -covermode=atomic > /dev/null 2>&1
	@FAIL=false; \
	pkg_cov=$$(go tool cover -func=coverage.out | grep '^$(MODULE)/pkg/' | awk '{sum+=$$3; n++} END {if(n>0) printf "%.0f", sum/n; else print "0"}'); \
	cli_cov=$$(go tool cover -func=coverage.out | grep '^$(MODULE)/internal/cli/' | awk '{sum+=$$3; n++} END {if(n>0) printf "%.0f", sum/n; else print "0"}'); \
	if [ $$(echo "$$pkg_cov < $(PKG_COVERAGE_THRESHOLD)" | bc) -eq 1 ]; then \
		echo "  FAIL: pkg/ coverage $${pkg_cov}% < $(PKG_COVERAGE_THRESHOLD)%"; \
		FAIL=true; \
	else \
		echo "  PASS: pkg/ coverage $${pkg_cov}% >= $(PKG_COVERAGE_THRESHOLD)%"; \
	fi; \
	if [ $$(echo "$$cli_cov < $(CLI_COVERAGE_THRESHOLD)" | bc) -eq 1 ]; then \
		echo "  WARN: internal/cli/ coverage $${cli_cov}% < $(CLI_COVERAGE_THRESHOLD)% (aspirational)"; \
	else \
		echo "  PASS: internal/cli/ coverage $${cli_cov}% >= $(CLI_COVERAGE_THRESHOLD)%"; \
	fi; \
	rm -f coverage.out; \
	if [ "$$FAIL" = "true" ]; then exit 1; fi

.PHONY: lint
lint: getlint ## Run golangci-lint
	@echo "Running golangci-lint..."
	$(GOPATH)/bin/golangci-lint --version
	$(GOPATH)/bin/golangci-lint run --timeout 5m

.PHONY: getlint
getlint: ## Install golangci-lint if not already installed
	@$(GOPATH)/bin/golangci-lint >/dev/null 2>&1 || (echo "Installing golangci-lint..." && go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION))

.PHONY: vet
vet: ## Run go vet
	@echo "Running go vet..."
	go vet ./...

.PHONY: fmt
fmt: ## Format the code
	@echo "Formatting the code..."
	gofmt -s -l -w cmd internal pkg

.PHONY: fmt-check
fmt-check: ## Check code formatting (exits non-zero if unformatted)
	@echo "Checking code formatting..."
	@test -z "$$(gofmt -s -l cmd internal pkg)" || (echo "Unformatted files:"; gofmt -s -l cmd internal pkg; exit 1)

.PHONY: tidy
tidy: ## Tidy up go modules
	@echo "Tidying up go modules..."
	go mod tidy

.PHONY: generate
generate: ## Run go generate
	go generate ./...

.PHONY: verify
verify: verify-pin verify-tidy verify-generate verify-checksum-template ## Run all verification checks

.PHONY: verify-pin
verify-pin: ## Verify the OpenShell pin is consistent
	./hack/verify-pin.sh

.PHONY: verify-tidy
verify-tidy: ## Verify go.mod and go.sum are tidy
	go mod tidy
	git diff --exit-code -- go.mod go.sum

.PHONY: verify-generate
verify-generate: generate ## Verify generated code is up to date
	git diff --exit-code

.PHONY: verify-checksum-template
verify-checksum-template: ## Verify .goreleaser.yaml checksum template matches selfupdate.ChecksumAssetName
	./hack/verify-checksum-template.sh

.PHONY: test-vuln
test-vuln: ## Check for known vulnerabilities in dependencies
	@echo "Checking for known vulnerabilities..."
	@which govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

.PHONY: test-all
test-all: fmt-check vet lint test test-race verify ## Run all checks
	@echo "All checks passed."

.PHONY: image
image: ## Build the container image
	$(CONTAINER_ENGINE) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg OPENSHELL_PIN=$(OPENSHELL_PIN) \
		-f build/Containerfile -t $(IMAGE):$(IMAGE_TAG) .

.PHONY: ensure-goreleaser
ensure-goreleaser: ## Ensure goreleaser is installed
	@which goreleaser >/dev/null 2>&1 || (echo "goreleaser not found; install from https://goreleaser.com/install/"; exit 1)

.PHONY: release
release: ensure-goreleaser ## Create a release using goreleaser
	@echo "Creating a release..."
	goreleaser release --clean --parallelism 1

# ---------------------------------------------------------------------------
# E2E UX tests
# ---------------------------------------------------------------------------

.PHONY: e2e-image
e2e-image: ## Build the E2E UX test image (Fedora + vault + pinned openshellctl RC)
	$(CONTAINER_ENGINE) build \
		--build-arg FEDORA_VERSION=$(E2E_FEDORA_VERSION) \
		--build-arg OPENSHELLCTL_VERSION=$(E2E_OPENSHELLCTL_VERSION) \
		--build-arg OPENSHELLCTL_CHECKSUM=$(E2E_OPENSHELLCTL_CHECKSUM) \
		--build-arg OPENSHELL_VAULT_KV_MOUNT=$(E2E_VAULT_KV_MOUNT) \
		--build-arg OPENSHELL_VAULT_KV_PATH=$(E2E_VAULT_KV_PATH) \
		--build-arg OPENSHELL_VAULT_FIELD_PREFIX=$(E2E_VAULT_FIELD_PREFIX) \
		-f test/Containerfile.e2e -t $(E2E_IMAGE):$(E2E_IMAGE_TAG) .

.PHONY: e2e
e2e: e2e-image ## E2E UX test: vault login -method=oidc inside the container, then openshellctl sandbox list (needs VAULT_ADDR)
	@test -n "$(VAULT_ADDR)" || { \
		echo ""; \
		echo "VAULT_ADDR is required."; \
		echo "  export VAULT_ADDR=https://vault.example.com"; \
		echo "  make e2e"; \
		echo "or:"; \
		echo "  make e2e VAULT_ADDR=https://vault.example.com"; \
		echo ""; \
		exit 2; }
	@$(CONTAINER_ENGINE) run --rm \
		-p 127.0.0.1:$(E2E_VAULT_CALLBACK_PORT):8250 \
		-e VAULT_ADDR \
		-e VAULT_CALLBACK_PORT=$(E2E_VAULT_CALLBACK_PORT) \
		$(E2E_IMAGE):$(E2E_IMAGE_TAG)

.PHONY: e2e-shell
e2e-shell: e2e-image ## Interactive shell in the E2E test image
	$(CONTAINER_ENGINE) run --rm -it --entrypoint /bin/bash $(E2E_IMAGE):$(E2E_IMAGE_TAG)

.PHONY: e2e-checksum
e2e-checksum: ## Print the sha256 for E2E_OPENSHELLCTL_VERSION's Linux x86_64 archive
	@curl -fsSL $(E2E_CHECKSUMS_URL) | awk '/openshellctl_Linux_x86_64.tar.gz/ {print $$1}'

.PHONY: e2e-bump
e2e-bump: ## Update E2E_OPENSHELLCTL_VERSION and its checksum (make e2e-bump E2E_OPENSHELLCTL_VERSION=x.y.z-rc.N)
	@sum=$$(curl -fsSL $(E2E_CHECKSUMS_URL) | awk '/openshellctl_Linux_x86_64.tar.gz/ {print $$1}'); \
	test -n "$$sum" || { echo "no checksum found at $(E2E_CHECKSUMS_URL)"; exit 1; }; \
	sed -i \
	    -e 's/^E2E_OPENSHELLCTL_VERSION  ?= .*/E2E_OPENSHELLCTL_VERSION  ?= $(E2E_OPENSHELLCTL_VERSION)/' \
	    -e "s/^E2E_OPENSHELLCTL_CHECKSUM ?= .*/E2E_OPENSHELLCTL_CHECKSUM ?= $$sum/" \
	    Makefile; \
	echo "pinned $(E2E_OPENSHELLCTL_VERSION) $$sum"

.PHONY: clean
clean: ## Clean up build artifacts
	@echo "Cleaning up build artifacts..."
	rm -rf bin dist coverage.out
