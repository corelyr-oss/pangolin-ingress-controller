# Image URL to use all building/pushing image targets
IMG ?= pangolin-ingress-controller:latest
# Kubernetes version for testing
KUBERNETES_VERSION ?= 1.28.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool binaries and versions
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
# controller-gen is built as its own module, so this version is independent of
# the k8s libraries in go.mod. It must be new enough to compile under the Go
# toolchain in go.mod: v0.13.0 pulls golang.org/x/tools v0.12.0, which does not
# build on Go 1.25.
CONTROLLER_TOOLS_VERSION ?= v0.17.3

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	test -s $(CONTROLLER_GEN) && $(CONTROLLER_GEN) --version | grep -q $(CONTROLLER_TOOLS_VERSION) || \
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

.PHONY: generate
generate: controller-gen ## Generate DeepCopy methods for API types.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: manifests
manifests: controller-gen ## Generate CRD manifests into deploy/crds and the Helm chart.
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:artifacts:config=deploy/crds
	cp deploy/crds/pangolin.ingress.k8s.io_pangolinendpoints.yaml chart/templates/crd-pangolinendpoint.yaml
	$(SHELL) hack/wrap-crd-template.sh chart/templates/crd-pangolinendpoint.yaml

.PHONY: test
test: generate fmt vet ## Run tests.
	go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: generate fmt vet ## Run a controller from your host.
	go run cmd/main.go

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	docker build -t ${IMG} .

.PHONY: docker-buildx-setup
docker-buildx-setup: ## Create and use a buildx builder instance.
	-docker buildx create --name pangolin-builder --use
	docker buildx inspect --bootstrap

.PHONY: docker-build-multiarch
docker-build-multiarch: docker-buildx-setup ## Build multi-arch docker image with the manager.
	docker buildx build --platform linux/amd64,linux/arm64 -t ${IMG} .

.PHONY: docker-build-push
docker-build-push: docker-buildx-setup ## Build and push multi-arch docker image with the manager.
	docker buildx build --platform linux/amd64,linux/arm64 -t ${IMG} --push .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	docker push ${IMG}

##@ Deployment

.PHONY: deploy
deploy: ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	kubectl apply -f deploy/

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	kubectl delete -f deploy/

.PHONY: install-crds
install-crds: ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	kubectl apply -f deploy/crds/

.PHONY: uninstall-crds
uninstall-crds: ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	kubectl delete -f deploy/crds/
