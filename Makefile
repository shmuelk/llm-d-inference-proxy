# The Go and Python based tools are defined in Makefile.tools.mk.
include Makefile.tools.mk

SHELL := /usr/bin/env bash

# Defaults
TARGETOS ?= $(shell command -v go >/dev/null 2>&1 && go env GOOS || uname -s | tr '[:upper:]' '[:lower:]')
TARGETARCH ?= $(shell command -v go >/dev/null 2>&1 && go env GOARCH || uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/; s/armv7l/arm/')
PROJECT_NAME ?= llm-d-inference-proxy
SIDECAR_IMAGE_NAME ?= llm-d-routing-sidecar
VLLM_SIMULATOR_IMAGE_NAME ?= llm-d-inference-sim
IMAGE_REGISTRY ?= ghcr.io/llm-d
IMAGE_TAG_BASE ?= $(IMAGE_REGISTRY)/$(PROJECT_NAME)
PROXY_TAG ?= dev
export PROXY_TAG
export PROXY_IMAGE ?= $(IMAGE_TAG_BASE):$(PROXY_TAG)
SIDECAR_TAG ?= dev
export SIDECAR_TAG
SIDECAR_IMAGE_TAG_BASE ?= $(IMAGE_REGISTRY)/$(SIDECAR_IMAGE_NAME)
export SIDECAR_IMAGE ?= $(SIDECAR_IMAGE_TAG_BASE):$(SIDECAR_TAG)
NAMESPACE ?= hc4ai-operator
VLLM_SIMULATOR_TAG ?= dev
export VLLM_SIMULATOR_TAG
VLLM_SIMULATOR_TAG_BASE ?= $(IMAGE_REGISTRY)/$(VLLM_SIMULATOR_IMAGE_NAME)
export VLLM_SIMULATOR_IMAGE ?= $(VLLM_SIMULATOR_TAG_BASE):$(VLLM_SIMULATOR_TAG)

# Map go arch to typos arch
ifeq ($(TARGETARCH),amd64)
TYPOS_TARGET_ARCH = x86_64
else ifeq ($(TARGETARCH),arm64)
TYPOS_TARGET_ARCH = aarch64
else
TYPOS_TARGET_ARCH = $(TARGETARCH)
endif

ifeq ($(TARGETOS),darwin)
TAR_OPTS = --strip-components 1
TYPOS_ARCH = $(TYPOS_TARGET_ARCH)-apple-darwin
else
TAR_OPTS = --wildcards '*/typos'
TYPOS_ARCH = $(TYPOS_TARGET_ARCH)-unknown-linux-musl
endif

CONTAINER_RUNTIME := $(shell { command -v docker >/dev/null 2>&1 && echo docker; } || { command -v podman >/dev/null 2>&1 && echo podman; } || echo "")
export CONTAINER_RUNTIME
BUILDER := $(shell command -v buildah >/dev/null 2>&1 && echo buildah || echo $(CONTAINER_RUNTIME))
PLATFORMS ?= linux/amd64 # linux/arm64 # linux/s390x,linux/ppc64le

GIT_COMMIT_SHA ?= "$(shell git rev-parse HEAD 2>/dev/null)"
BUILD_REF ?= $(shell git describe --abbrev=0 2>/dev/null)

# go source files
SRC = $(shell find . -type f -name '*.go')

LDFLAGS ?= -extldflags '-L$(shell pwd)/lib'

# Internal variables for generic targets
proxy_IMAGE = $(PROXY_IMAGE)
proxy_NAME = proxy
proxy_LDFLAGS = -ldflags="$(LDFLAGS)"
proxy_TEST_FILES = go list ./... | grep -v /test/

.PHONY: help
help: ## Print help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: clean
clean:
	go clean -testcache -cache
	rm -f $(TOKENIZER_LIB)
	rm -rf build/
	rmdir lib

.PHONY: format
format: ## Format Go source files
	@printf "\033[33;1m==== Running gofmt ====\033[0m\n"
	@gofmt -l -w $(SRC)

.PHONY: test
test: test-unit test-e2e ## Run unit tests and e2e tests

.PHONY: test-unit
test-unit: test-unit-proxy

.PHONY: test-unit-%
test-unit-%: check-dependencies ## Run unit tests
	@printf "\033[33;1m==== Running Unit Tests ====\033[0m\n"
	go test $($*_LDFLAGS) -v $$($($*_TEST_FILES) | tr '\n' ' ')

.PHONY: test-integration
test-integration: download-tokenizer check-dependencies ## Run integration tests
	@printf "\033[33;1m==== Running Integration Tests ====\033[0m\n"
	go test -ldflags="$(LDFLAGS)" -v -tags=integration_tests ./test/integration/

.PHONY: test-e2e
test-e2e: image-build image-pull ## Run end-to-end tests against a new kind cluster
	@printf "\033[33;1m==== Running End to End Tests ====\033[0m\n"
	./test/scripts/run_e2e.sh

.PHONY: post-deploy-test
post-deploy-test: ## Run post deployment tests
	echo Success!
	@echo "Post-deployment tests passed."

.PHONY: lint
lint: check-golangci-lint check-typos ## Run lint
	@printf "\033[33;1m==== Running linting ====\033[0m\n"
	golangci-lint run
	$(TYPOS)

##@ Build

.PHONY: build
build: build-proxy ## Build the project

.PHONY: build-%
build-%: check-go download-tokenizer ## Build the project
	@printf "\033[33;1m==== Building ====\033[0m\n"
	go build $($*_LDFLAGS) -o bin/$($*_NAME) cmd/$($*_NAME)/main.go

##@ Container Build/Push

.PHONY:	image-build
image-build: image-build-proxy ## Build Docker image

.PHONY: image-build-%
image-build-%: check-container-tool ## Build Docker image ## Build Docker image using $(CONTAINER_RUNTIME)
	@printf "\033[33;1m==== Building Docker image $($*_IMAGE) ====\033[0m\n"
	$(CONTAINER_RUNTIME) build \
		--platform linux/$(TARGETARCH) \
 		--build-arg TARGETOS=linux \
		--build-arg TARGETARCH=$(TARGETARCH) \
		--build-arg COMMIT_SHA=${GIT_COMMIT_SHA} \
		--build-arg BUILD_REF=${BUILD_REF} \
 		-t $($*_IMAGE) -f Dockerfile.$* .

.PHONY: image-push
image-push: image-push-proxy ## Push container images to registry

.PHONY: image-push-%
image-push-%: check-container-tool ## Push container image to registry
	@printf "\033[33;1m==== Pushing Container image $($*_IMAGE) ====\033[0m\n"
	$(CONTAINER_RUNTIME) push $($*_IMAGE)

.PHONY: image-pull
image-pull: check-container-tool ## Pull all related images using $(CONTAINER_RUNTIME)
	@printf "\033[33;1m==== Pulling Container images ====\033[0m\n"
	./scripts/pull_images.sh

##@ Install/Uninstall Targets

### RBAC Targets (using kustomize and envsubst)

##@ Environment
.PHONY: env
env: ## Print environment variables
	@echo "IMAGE_TAG_BASE=$(IMAGE_TAG_BASE)"
	@echo "PROXY_IMAGE=$(PROXY_IMAGE)"
	@echo "CONTAINER_RUNTIME=$(CONTAINER_RUNTIME)"

.PHONY: check-typos
check-typos: $(TYPOS) ## Check for spelling errors using typos (exits with error if found)
	@echo "🔍 Checking for spelling errors with typos..."
	@TYPOS_OUTPUT=$$($(TYPOS) --format brief 2>&1); \
	if [ $$? -eq 0 ]; then \
		echo "✅ No spelling errors found!"; \
		echo "🎉 Spelling check completed successfully!"; \
	else \
		echo "❌ Spelling errors found!"; \
		echo "🔧 You can try 'make fix-typos' to automatically fix the spelling errors and run 'make check-typos' again"; \
		echo "$$TYPOS_OUTPUT"; \
		exit 1; \
	fi
	
##@ Tools

.PHONY: check-tools
check-tools: \
  check-go \
  check-ginkgo \
  check-golangci-lint \
  check-kustomize \
  check-envsubst \
  check-container-tool \
  check-kubectl \
  check-buildah
	@echo "✅ All required tools are installed."

.PHONY: check-go
check-go:
	@command -v go >/dev/null 2>&1 || { \
	  echo "❌ Go is not installed. Install it from https://golang.org/dl/"; exit 1; }

.PHONY: check-ginkgo
check-ginkgo:
	@command -v ginkgo >/dev/null 2>&1 || { \
	  echo "❌ ginkgo is not installed. Install with: go install github.com/onsi/ginkgo/v2/ginkgo@latest"; exit 1; }

.PHONY: check-golangci-lint
check-golangci-lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
	  echo "❌ golangci-lint is not installed. Install from https://golangci-lint.run/usage/install/"; exit 1; }

.PHONY: check-kustomize
check-kustomize:
	@command -v kustomize >/dev/null 2>&1 || { \
	  echo "❌ kustomize is not installed. Install it from https://kubectl.docs.kubernetes.io/installation/kustomize/"; exit 1; }

.PHONY: check-envsubst
check-envsubst:
	@command -v envsubst >/dev/null 2>&1 || { \
	  echo "❌ envsubst is not installed. It is part of gettext."; \
	  echo "🔧 Try: sudo apt install gettext OR brew install gettext"; exit 1; }

.PHONY: check-container-tool
check-container-tool:
	@if [ -z "$(CONTAINER_RUNTIME)" ]; then \
		echo "❌ Error: No container tool detected. Please install docker or podman."; \
		exit 1; \
	else \
		echo "✅ Container tool '$(CONTAINER_RUNTIME)' found."; \
	fi
	  

.PHONY: check-kubectl
check-kubectl:
	@command -v kubectl >/dev/null 2>&1 || { \
	  echo "❌ kubectl is not installed. Install it from https://kubernetes.io/docs/tasks/tools/"; exit 1; }

.PHONY: check-builder
check-builder:
	@if [ -z "$(BUILDER)" ]; then \
		echo "❌ No container builder tool (buildah, docker, or podman) found."; \
		exit 1; \
	else \
		echo "✅ Using builder: $(BUILDER)"; \
	fi

##@ Alias checking
.PHONY: check-alias
check-alias: check-container-tool
	@echo "🔍 Checking alias functionality for container '$(PROJECT_NAME)-container'..."
	@if ! $(CONTAINER_RUNTIME) exec $(PROJECT_NAME)-container /app/$(PROJECT_NAME) --help >/dev/null 2>&1; then \
	  echo "⚠️  The container '$(PROJECT_NAME)-container' is running, but the alias might not work."; \
	  echo "🔧 Try: $(CONTAINER_RUNTIME) exec -it $(PROJECT_NAME)-container /app/$(PROJECT_NAME)"; \
	else \
	  echo "✅ Alias is likely to work: alias $(PROJECT_NAME)='$(CONTAINER_RUNTIME) exec -it $(PROJECT_NAME)-container /app/$(PROJECT_NAME)'"; \
	fi

.PHONY: print-namespace
print-namespace: ## Print the current namespace
	@echo "$(NAMESPACE)"

.PHONY: print-project-name
print-project-name: ## Print the current project name
	@echo "$(PROJECT_NAME)"

.PHONY: install-hooks
install-hooks: ## Install git hooks
	git config core.hooksPath hooks

##@ Dev Environments

KIND_CLUSTER_NAME ?= llm-d-inference-proxy
KIND_PROXY_HOST_PORT ?= 30080

.PHONY: env-dev-kind
env-dev-kind: ## Run under kind ($(KIND_CLUSTER_NAME))
	@if [ "$$PD_ENABLED" = "true" ] && [ "$$KV_CACHE_ENABLED" = "true" ]; then \
		echo "Error: Both PD_ENABLED and KV_CACHE_ENABLED are true. Skipping env-dev-kind."; \
		exit 1; \
	else \
		CLUSTER_NAME=$(KIND_CLUSTER_NAME) \
		PROXY_HOST_PORT=$(KIND_PROXY_HOST_PORT) \
		IMAGE_REGISTRY=$(IMAGE_REGISTRY) \
		PROXY_IMAGE=$(PROXY_IMAGE) \
		VLLM_SIMULATOR_IMAGE=${VLLM_SIMULATOR_IMAGE} \
		SIDECAR_IMAGE=${SIDECAR_IMAGE} \
		./scripts/kind-dev-env.sh; \
	fi

.PHONY: clean-env-dev-kind
clean-env-dev-kind:      ## Cleanup kind setup (delete cluster $(KIND_CLUSTER_NAME))
	@echo "INFO: cleaning up kind cluster $(KIND_CLUSTER_NAME)"
	kind delete cluster --name $(KIND_CLUSTER_NAME)

##@ Dependencies

.PHONY: check-dependencies
check-dependencies: ## Check if development dependencies are installed
	@echo "✅ All dependencies are installed."

.PHONY: install-dependencies
install-dependencies: ## Install development dependencies based on OS/ARCH
	@echo "Checking and installing development dependencies..."
