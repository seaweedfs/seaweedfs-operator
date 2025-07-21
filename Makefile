# Current Operator version
VERSION ?= v0.0.1
# Default bundle image tag
BUNDLE_IMG ?= controller-bundle:$(VERSION)
# Options for 'bundle-build'
ifneq ($(origin CHANNELS), undefined)
BUNDLE_CHANNELS := --channels=$(CHANNELS)
endif
ifneq ($(origin DEFAULT_CHANNEL), undefined)
BUNDLE_DEFAULT_CHANNEL := --default-channel=$(DEFAULT_CHANNEL)
endif
BUNDLE_METADATA_OPTS ?= $(BUNDLE_CHANNELS) $(BUNDLE_DEFAULT_CHANNEL)

# Image URL to use all building/pushing image targets
IMG ?= ghcr.io/seaweedfs/seaweedfs-operator:$(VERSION)

# Default target operating system.
TARGETOS ?= linux

# Default target architecture.
TARGETARCH ?= $(shell go env GOARCH)

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

all: manager

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	@echo "Check for kubernetes version $(K8S_VERSION_TRIMMED_V) in $(ENVTEST)"
	@$(ENVTEST) list | grep -q $(K8S_VERSION_TRIMMED_V)
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(K8S_VERSION_TRIMMED_V) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up.
test-e2e:
	go test ./test/e2e/ -v -ginkgo.v

# Build manager binary
manager: generate fmt vet
	go build -ldflags="-s -w" -o bin/manager main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: generate fmt vet manifests
	go run ./main.go

debug: generate fmt vet manifests
	go build -ldflags="-s -w" -gcflags="all=-N -l" ./main.go
	ENABLE_WEBHOOKS=false dlv --listen=:2345 --headless=true --api-version=2 --accept-multiclient exec main

# Generate manifests e.g. CRD, RBAC etc.
manifests: controller-gen
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

.PHONY: mod-tidy
mod-tidy: ## Run go mod tidy against code.
	go mod tidy

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter & yamllint
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: nilaway-lint
nilaway-lint: nilaway
	$(NILAWAY_LINT) -include-pkgs=github.com/seaweedfs/seweedfs-operator/api,github.com/seaweedfs/seweedfs-operator/internal,github.com/seaweedfs/seweedfs-operator/test ./...

# Generate code
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

# Build the docker image
.PHONY: docker-build
docker-build:
	echo ${IMG}
	${CONTAINER_TOOL} build . -t ${IMG} \
	  --platform ${TARGETOS}/${TARGETARCH} \
	  --build-arg TARGETARCH=${TARGETARCH} \
	  --build-arg TARGETOS=${TARGETOS}

# Push the docker image
docker-push:
	${CONTAINER_TOOL} push ${IMG}

# Generate bundle manifests and metadata, then validate generated files.
bundle: manifests
	operator-sdk generate kustomize manifests -q
	kustomize build config/manifests | operator-sdk generate bundle -q --overwrite --version $(VERSION) $(BUNDLE_METADATA_OPTS)
	operator-sdk bundle validate ./bundle

# Build the bundle image.
bundle-build:
	docker build -f bundle.Dockerfile -t $(BUNDLE_IMG) .

##@ Deployment

# K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
# renovate: datasource=github-tags depName=kubernetes/kubernetes
K8S_VERSION ?= v1.30.0
K8S_VERSION_TRIMMED_V = $(subst v,,$(K8S_VERSION))

KIND_CLUSTER_NAME ?= seaweedfs-operator-kind
NAMESPACE ?= seaweedfs-operator-system
KIND_IMAGE ?= kindest/node:$(K8S_VERSION)

# renovate: datasource=github-tags depName=prometheus-operator/prometheus-operator
PROMETHEUS_OPERATOR_VERSION ?= v0.74.0
# renovate: datasource=github-tags depName=jetstack/cert-manager
CERT_MANAGER_VERSION ?= v1.15.0

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete -n $(NAMESPACE) --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller:latest=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) -n $(NAMESPACE) apply -f -
	$(KUBECTL) wait deployment.apps/seaweedfs-operator-controller-manager --for condition=Available --namespace $(NAMESPACE) --timeout 5m

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete -n $(NAMESPACE) --ignore-not-found=$(ignore-not-found) -f -

.PHONY: redeploy
redeploy: deploy ## Redeploy controller with new docker image.
	$(KUBECTL) rollout restart -n $(NAMESPACE) deploy/seaweedfs-operator-controller-manager

.PHONY: kind-load
kind-load: docker-build kind ## Build and upload docker image to the local Kind cluster.
	$(KIND) load docker-image ${IMG} --name $(KIND_CLUSTER_NAME)

.PHONY: kind-create
kind-create: kind yq ## Create kubernetes cluster using Kind.
	@if ! $(KIND) get clusters | grep -q $(KIND_CLUSTER_NAME); then \
		$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --image $(KIND_IMAGE); \
	fi
	@if ! $(CONTAINER_TOOL) ps --format json | $(YQ) -P -pj e 'select(.Names == "$(KIND_CLUSTER_NAME)-control-plane") | .Image' | grep -q $(KIND_IMAGE); then \
  		$(KIND) delete cluster --name $(KIND_CLUSTER_NAME); \
		$(KIND) create cluster --name $(KIND_CLUSTER_NAME) --image $(KIND_IMAGE); \
	fi

.PHONY: kind-delete
kind-delete: kind ## Create kubernetes cluster using Kind.
	@if $(KIND) get clusters | grep -q $(KIND_CLUSTER_NAME); then \
		$(KIND) delete cluster --name $(KIND_CLUSTER_NAME); \
	fi

.PHONY: kind-prepare
kind-prepare: kind-create
	# Install prometheus operator
	$(KUBECTL) apply --server-side -f "https://github.com/prometheus-operator/prometheus-operator/releases/download/$(PROMETHEUS_OPERATOR_VERSION)/bundle.yaml"
	$(KUBECTL) wait deployment.apps/prometheus-operator --for condition=Available --namespace default --timeout 5m
	# Install cert-manager operator
	$(KUBECTL) apply --server-side -f "https://github.com/jetstack/cert-manager/releases/download/$(CERT_MANAGER_VERSION)/cert-manager.yaml"
	$(KUBECTL) wait deployment.apps/cert-manager-webhook --for condition=Available --namespace cert-manager --timeout 5m

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

HELM_PLUGINS ?= $(LOCALBIN)/helm-plugins
export HELM_PLUGINS
$(HELM_PLUGINS):
	mkdir -p $(HELM_PLUGINS)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
NILAWAY_LINT ?= $(LOCALBIN)/nilaway
KIND ?= $(LOCALBIN)/kind
HELM ?= $(LOCALBIN)/helm
HELM_DOCS ?= $(LOCALBIN)/helm-docs
YQ = $(LOCALBIN)/yq
CRD_REF_DOCS ?= $(LOCALBIN)/crd-ref-docs

## Tool Versions
# renovate: datasource=github-tags depName=kubernetes-sigs/kustomize
KUSTOMIZE_VERSION ?= v5.3.0
# renovate: datasource=github-tags depName=kubernetes-sigs/controller-tools
CONTROLLER_TOOLS_VERSION ?= v0.15.0
ENVTEST_VERSION ?= latest
# renovate: datasource=github-tags depName=golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= v1.59.1
# renovate: datasource=github-tags depName=kubernetes-sigs/kind
KIND_VERSION ?= v0.23.0
# renovate: datasource=github-tags depName=helm/helm
HELM_VERSION ?= v3.15.2
# renovate: datasource=github-tags depName=losisin/helm-values-schema-json
HELM_SCHEMA_VERSION ?= v1.4.1
# renovate: datasource=github-tags depName=norwoodj/helm-docs
HELM_DOCS_VERSION ?= v1.13.1
# renovate: datasource=github-tags depName=mikefarah/yq
YQ_VERSION ?= v4.44.1

## Tool install scripts
KUSTOMIZE_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/kubernetes-sigs/kustomize/master/hack/install_kustomize.sh"
HELM_INSTALL_SCRIPT ?= "https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3"

.PHONY: kustomize
kustomize: $(LOCALBIN)
	@if test -x $(KUSTOMIZE) && ! $(KUSTOMIZE) version | grep -q $(KUSTOMIZE_VERSION); then \
		rm -f $(KUSTOMIZE); \
	fi
	@test -x $(KUSTOMIZE) || { curl -Ss $(KUSTOMIZE_INSTALL_SCRIPT) | bash -s -- $(subst v,,$(KUSTOMIZE_VERSION)) $(LOCALBIN); }

.PHONY: controller-gen
controller-gen: $(LOCALBIN)
	@test -x $(CONTROLLER_GEN) && $(CONTROLLER_GEN) --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	CGO_ENABLED=0 GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: envtest
envtest: $(LOCALBIN)
	@test -x $(ENVTEST) || GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

.PHONY: crd-ref-docs
crd-ref-docs: $(LOCALBIN)
	@test -x $(CRD_REF_DOCS) || GOBIN=$(LOCALBIN) go install github.com/elastic/crd-ref-docs@latest

.PHONY: golangci-lint
golangci-lint: $(LOCALBIN)
	@test -x $(GOLANGCI_LINT) && $(GOLANGCI_LINT) version | grep -q $(GOLANGCI_LINT_VERSION) || \
	GOBIN=$(LOCALBIN) go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

.PHONY: nilaway
nilaway: $(LOCALBIN)
	@test -x $(NILAWAY_LINT) || GOBIN=$(LOCALBIN) go install go.uber.org/nilaway/cmd/nilaway@latest

kind: $(LOCALBIN)
	@test -x $(KIND) && $(KIND) version | grep -q $(KIND_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/kind@$(KIND_VERSION)

.PHONY: helm
helm: $(LOCALBIN)
	@if test -x $(HELM) && ! $(HELM) version | grep -q $(HELM_VERSION); then \
		rm -f $(HELM); \
	fi
	@test -x $(HELM) || { curl -Ss $(HELM_INSTALL_SCRIPT) | sed "s|/usr/local/bin|$(LOCALBIN)|" | PATH="$(LOCALBIN):$(PATH)" bash -s -- --no-sudo --version $(HELM_VERSION); }

.PHONY: helm-schema
helm-schema: helm $(HELM_PLUGINS)
	@if ! $(HELM) plugin list | grep schema | grep -q $(subst v,,$(HELM_SCHEMA_VERSION)); then \
		if $(HELM) plugin list | grep -q schema ; then \
			$(HELM) plugin uninstall schema; \
		fi; \
		$(HELM) plugin install https://github.com/losisin/helm-values-schema-json --version=$(HELM_SCHEMA_VERSION); \
	fi

.PHONY: helm-docs
helm-docs: $(LOCALBIN)
	@test -x $(HELM_DOCS) && $(HELM_DOCS) version | grep -q $(HELM_DOCS_VERSION) || \
	GOBIN=$(LOCALBIN) go install github.com/norwoodj/helm-docs/cmd/helm-docs@$(HELM_DOCS_VERSION)

.PHONY: yq
yq: $(LOCALBIN)
	@test -x $(YQ) && $(YQ) version | grep -q $(YQ_VERSION) || \
	GOBIN=$(LOCALBIN) go install github.com/mikefarah/yq/v4@$(YQ_VERSION)
