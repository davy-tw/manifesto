# COMPONENT selects which binary the image contains: press, herald, darkroom or desk.
COMPONENT ?= press
# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# HERALD_IMG is the image for herald, the visitor site.
HERALD_IMG ?= herald:latest
# DARKROOM_IMG is the image for darkroom, the media store.
DARKROOM_IMG ?= darkroom:latest
# DESK_IMG is the image for desk, the authors' web UI.
DESK_IMG ?= desk:latest
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet setup-envtest kustomize helm ## Run tests (test/chart needs helm and kustomize).
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# TODO(user): To use a different vendor for e2e tests, modify the setup under 'tests/e2e'.
# The default setup assumes Kind is pre-installed and builds/loads the Manager Docker image locally.
# kubectl kuberc is disabled by default for test isolation; enable with:
# - KUBECTL_KUBERC=true
# CertManager is installed by default; skip with:
# - CERT_MANAGER_INSTALL_SKIP=true
KIND_CLUSTER ?= manifesto-test-e2e

.PHONY: setup-test-e2e
setup-test-e2e: ## Set up a Kind cluster for e2e tests if it does not exist
	@command -v $(KIND) >/dev/null 2>&1 || { \
		echo "Kind is not installed. Please install Kind manually."; \
		exit 1; \
	}
	@case "$$($(KIND) get clusters)" in \
		*"$(KIND_CLUSTER)"*) \
			echo "Kind cluster '$(KIND_CLUSTER)' already exists. Skipping creation." ;; \
		*) \
			echo "Creating Kind cluster '$(KIND_CLUSTER)'..."; \
			$(KIND) create cluster --name $(KIND_CLUSTER) ;; \
	esac

.PHONY: test-ui
test-ui: setup-envtest ## Drive desk in Chromium with Playwright (needs Node.js; run "npm ci" and "npx playwright install chromium" in internal/desk/uitest first).
	DESK_UI_TEST=1 KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test ./internal/desk -run TestUI -v

.PHONY: test-e2e
test-e2e: setup-test-e2e manifests generate fmt vet ## Run the e2e tests. Expected an isolated environment using Kind.
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 30m
	$(MAKE) cleanup-test-e2e

.PHONY: cleanup-test-e2e
cleanup-test-e2e: ## Tear down the Kind cluster used for e2e tests
	@$(KIND) delete cluster --name $(KIND_CLUSTER)

# The k3d variant runs the same suite on k3s with its bundled Traefik, the
# first production target (docs/k3s.md). It also enables press's gateway
# routing mode and checks that Traefik serves a Gazette end to end.
K3D_CLUSTER ?= manifesto-test-e2e-k3d
# K3S_IMAGE pins the k3s version the k3d cluster runs.
K3S_IMAGE ?= rancher/k3s:v1.37.2-k3s1

.PHONY: setup-test-e2e-k3d
setup-test-e2e-k3d: ## Set up a k3d (k3s + Traefik) cluster for e2e tests if it does not exist
	@command -v $(K3D) >/dev/null 2>&1 || { \
		echo "k3d is not installed. Please install k3d manually."; \
		exit 1; \
	}
	@if $(K3D) cluster get $(K3D_CLUSTER) >/dev/null 2>&1; then \
		echo "k3d cluster '$(K3D_CLUSTER)' already exists. Skipping creation."; \
	else \
		echo "Creating k3d cluster '$(K3D_CLUSTER)'..."; \
		$(K3D) cluster create $(K3D_CLUSTER) --image $(K3S_IMAGE) --wait --timeout 5m; \
	fi

.PHONY: test-e2e-k3d
test-e2e-k3d: setup-test-e2e-k3d manifests generate fmt vet ## Run the e2e tests on k3d (k3s with Traefik).
	E2E_PROVIDER=k3d K3D=$(K3D) K3D_CLUSTER=$(K3D_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v -timeout 30m
	$(MAKE) cleanup-test-e2e-k3d

.PHONY: cleanup-test-e2e-k3d
cleanup-test-e2e-k3d: ## Tear down the k3d cluster used for e2e tests
	@$(K3D) cluster delete $(K3D_CLUSTER)

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	"$(GOLANGCI_LINT)" run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	"$(GOLANGCI_LINT)" run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	"$(GOLANGCI_LINT)" config verify

##@ Build

.PHONY: build
build: manifests generate fmt vet mfctl ## Build every component and mfctl.
	go build -o bin/press ./cmd/press
	go build -o bin/herald ./cmd/herald
	go build -o bin/darkroom ./cmd/darkroom
	go build -o bin/desk ./cmd/desk

.PHONY: chart
chart: manifests kustomize ## Regenerate the Helm chart's CRDs and RBAC from config/.
	"$(KUSTOMIZE)" build config/default | go run ./hack/chartfiles charts/manifesto/files

.PHONY: chart-lint
chart-lint: chart helm ## Lint the Helm chart and fail if its generated files are stale.
	"$(HELM)" lint charts/manifesto --set desk.publicURL=https://desk.example.io
	@git diff --exit-code -- charts/manifesto/files || { echo "charts/manifesto/files is stale: run make chart and commit"; exit 1; }

MFCTL_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
.PHONY: mfctl
mfctl: ## Build the mfctl CLI into bin/mfctl (cross-compile with GOOS/GOARCH).
	CGO_ENABLED=0 go build -ldflags "-X go.pkgs.davy.tw/manifesto/internal/mfctl.Version=$(MFCTL_VERSION)" -o bin/mfctl ./cmd/mfctl

.PHONY: run
run: manifests generate fmt vet ## Run press (the operator) from your host.
	go run ./cmd/press

.PHONY: run-herald
run-herald: fmt vet ## Run herald (the visitor site) from your host.
	go run ./cmd/herald

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# Override BASE_IMAGE to build from another registry, e.g.
# make docker-build IMG=<img> BASE_IMAGE=docker.io/library/golang:1.27
.PHONY: docker-build
docker-build: ## Build docker image for COMPONENT (press, herald, darkroom or desk; default press).
	$(CONTAINER_TOOL) build $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) --build-arg COMPONENT=$(COMPONENT) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS are the platforms docker-buildx builds COMPONENT for. The
# Dockerfile cross-compiles on the build machine, so no emulation is needed.
# BUILDX_OUTPUT=--push pushes the image (the release does); the default only
# builds it, to check that every platform compiles.
PLATFORMS ?= linux/amd64,linux/arm64
BUILDX_OUTPUT ?=
# BUILDX_FLAGS are extra buildx flags, e.g. --label for the image source.
BUILDX_FLAGS ?=
.PHONY: docker-buildx
docker-buildx: ## Build COMPONENT's image for PLATFORMS (BUILDX_OUTPUT=--push to push it as IMG).
	- $(CONTAINER_TOOL) buildx create --name manifesto-builder
	$(CONTAINER_TOOL) buildx build --builder manifesto-builder $(BUILDX_OUTPUT) $(BUILDX_FLAGS) --platform=$(PLATFORMS) \
		$(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) --build-arg COMPONENT=$(COMPONENT) --tag ${IMG} .

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	cd config/herald && "$(KUSTOMIZE)" edit set image herald=${HERALD_IMG}
	cd config/darkroom && "$(KUSTOMIZE)" edit set image darkroom=${DARKROOM_IMG}
	cd config/desk && "$(KUSTOMIZE)" edit set image desk=${DESK_IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	cd config/herald && "$(KUSTOMIZE)" edit set image herald=${HERALD_IMG}
	cd config/darkroom && "$(KUSTOMIZE)" edit set image darkroom=${DARKROOM_IMG}
	cd config/desk && "$(KUSTOMIZE)" edit set image desk=${DESK_IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
KUBECTL ?= kubectl
KIND ?= kind
K3D ?= k3d
KUSTOMIZE ?= $(LOCALBIN)/kustomize
HELM ?= $(LOCALBIN)/helm
GORELEASER ?= $(LOCALBIN)/goreleaser
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.8.1
HELM_VERSION ?= v4.3.1
GORELEASER_VERSION ?= v2.20.0
CONTROLLER_TOOLS_VERSION ?= v0.22.0

#ENVTEST_VERSION is the controller-runtime version to use for setup-envtest, derived from go.mod
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually (controller-runtime replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v")

#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; \
  [ -n "$$v" ] || { echo "Set ENVTEST_K8S_VERSION manually (k8s.io/api replace has no tag)" >&2; exit 1; }; \
  printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')

GOLANGCI_LINT_VERSION ?= v2.15.0
.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: helm
helm: $(HELM) ## Download helm locally if necessary.
$(HELM): $(LOCALBIN)
	$(call go-install-tool,$(HELM),helm.sh/helm/v4/cmd/helm,$(HELM_VERSION))

.PHONY: goreleaser
goreleaser: $(GORELEASER) ## Download goreleaser locally if necessary.
$(GORELEASER): $(LOCALBIN)
	$(call go-install-tool,$(GORELEASER),github.com/goreleaser/goreleaser/v2,$(GORELEASER_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef

define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
