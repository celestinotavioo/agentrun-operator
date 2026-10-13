SHELL := /bin/bash
.DEFAULT_GOAL := help

KUBE_CONTEXT     ?= docker-desktop
NS               ?= agents-demo
# Tags derive from the source hash: a code change always reaches the cluster (no stale ":dev" cache).
SRC_HASH         := $(shell find api cmd internal go.mod go.sum Dockerfile -type f 2>/dev/null | LC_ALL=C sort | xargs cat | shasum | cut -c1-10)
TAG              ?= dev-$(SRC_HASH)
CONTROLLER_IMAGE ?= agentrun-controller:$(TAG)
AGENT_IMAGE      ?= agentrun-agent:$(TAG)
MOCK_SPEED       ?= 1
CONTROLLER_GEN_VERSION ?= v0.22.0

KUBECTL        := kubectl --context $(KUBE_CONTEXT)
BIN            := $(CURDIR)/bin
CONTROLLER_GEN := $(BIN)/controller-gen
export KUBE_CONTEXT

GREEN := \033[38;5;35m
BLUE  := \033[38;5;33m
DIM   := \033[2m
BOLD  := \033[1m
RESET := \033[0m

.PHONY: help
help: ## Show this help
	@printf "\n  $(BOLD)⎈ AgentRun Operator$(RESET) $(DIM)— Kubernetes Operators for AI Agents · DevFest Malta 2026$(RESET)\n\n"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z0-9_-]+:.*?##/ { printf "    $(GREEN)make %-13s$(RESET) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\n  $(DIM)Before the talk: make stage-check · On stage: make demo$(RESET)\n\n"

.PHONY: demo
demo: agentctl ## The live demo: incident story in 5 acts, scoreboard, report card
	@TAG=$(TAG) bash hack/demo.sh

.PHONY: stage-check
stage-check: agentctl ## Run once before the talk: full rehearsal + checklist
	@TAG=$(TAG) bash hack/stage-check.sh

.PHONY: doctor
doctor: ## Check Docker, Kubernetes and the operator, with a fix for anything missing
	@bash hack/doctor.sh

.PHONY: clean
clean: guard ## Remove the operator, the CRD and every AgentRun from the cluster
	-@$(KUBECTL) delete namespace $(NS) --ignore-not-found --wait=true
	-@$(KUBECTL) delete -k config/default --ignore-not-found

# ---- internals (used by demo / stage-check / CI) -------------------------------------------

.PHONY: deploy
deploy: guard images install
	@$(KUBECTL) kustomize config/default \
		| sed -e 's|agentrun-controller:dev|$(CONTROLLER_IMAGE)|' -e 's|agentrun-agent:dev|$(AGENT_IMAGE)|' \
		| $(KUBECTL) apply -f - >/dev/null
	@$(KUBECTL) -n agentrun-system set env deploy/agentrun-controller MOCK_SPEED=$(MOCK_SPEED) >/dev/null
	@$(KUBECTL) -n agentrun-system scale deploy/agentrun-controller --replicas=1 >/dev/null
	@$(KUBECTL) -n agentrun-system rollout status deploy/agentrun-controller --timeout=120s

.PHONY: install
install: guard manifests
	@$(KUBECTL) apply -k config/crd >/dev/null
	@$(KUBECTL) wait --for=condition=Established crd/agentruns.agents.devfest.dev --timeout=30s >/dev/null

.PHONY: images
images:
	@docker build -q --build-arg BIN=manager -t $(CONTROLLER_IMAGE) . >/dev/null
	@docker build -q --build-arg BIN=agent -t $(AGENT_IMAGE) . >/dev/null

.PHONY: agentctl
agentctl:
	@go build -o $(BIN)/agentctl ./cmd/agentctl

.PHONY: test
test:
	go test -race -cover ./...

.PHONY: lint
lint:
	@test -z "$$(gofmt -l api cmd internal)" || { gofmt -l api cmd internal; echo "run: gofmt -w api cmd internal"; exit 1; }
	go vet ./...

.PHONY: manifests
manifests: $(CONTROLLER_GEN)
	@$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...
	@$(CONTROLLER_GEN) crd:allowDangerousTypes=true rbac:roleName=agentrun-manager-role paths=./... \
		output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

$(CONTROLLER_GEN):
	@GOBIN=$(BIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: guard
guard:
	@bash -c 'source hack/lib.sh && guard_context'
