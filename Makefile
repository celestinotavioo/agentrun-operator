SHELL := /bin/bash
.DEFAULT_GOAL := help

# ---- settings (override like: make deploy KUBE_CONTEXT=kind-dev) -------------------------
KUBE_CONTEXT     ?= docker-desktop
NS               ?= agents-demo
# Tags derive from the source hash: a code change always reaches the cluster (no stale ":dev" cache).
SRC_HASH         := $(shell find api cmd internal go.mod go.sum Dockerfile -type f 2>/dev/null | LC_ALL=C sort | xargs cat | shasum | cut -c1-10)
TAG              ?= dev-$(SRC_HASH)
CONTROLLER_IMAGE ?= agentrun-controller:$(TAG)
AGENT_IMAGE      ?= agentrun-agent:$(TAG)
MOCK_SPEED       ?= 1
CONTROLLER_GEN_VERSION ?= v0.22.0

KUBECTL := kubectl --context $(KUBE_CONTEXT)
BIN     := $(CURDIR)/bin
CONTROLLER_GEN := $(BIN)/controller-gen
AGENTCTL := $(BIN)/agentctl --context $(KUBE_CONTEXT) -n $(NS)
export KUBE_CONTEXT

GREEN := \033[38;5;35m
BLUE  := \033[38;5;33m
DIM   := \033[2m
BOLD  := \033[1m
RESET := \033[0m

##@ Start here

.PHONY: help
help: ## Show this help
	@printf "\n  $(BOLD)⎈ AgentRun Operator$(RESET) $(DIM)— Kubernetes Operators for AI Agents · DevFest Malta 2026$(RESET)\n"
	@printf "  $(DIM)context: $(KUBE_CONTEXT) · namespace: $(NS)$(RESET)\n"
	@awk 'BEGIN {FS = ":.*##"} \
		/^##@/ { printf "\n  $(BLUE)$(BOLD)%s$(RESET)\n", substr($$0, 5) } \
		/^[a-zA-Z0-9_-]+:.*?##/ { printf "    $(GREEN)make %-20s$(RESET) %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf "\n  $(DIM)First time? → make demo$(RESET)\n\n"

.PHONY: demo
demo: agentctl ## The talk: incident story in 5 acts, live scoreboard, report card (sets up whatever is missing)
	@TAG=$(TAG) bash hack/demo.sh

.PHONY: stage-check
stage-check: agentctl ## Run once before the talk: full rehearsal + checklist, so `make demo` is instant on stage
	@TAG=$(TAG) bash hack/stage-check.sh

.PHONY: doctor
doctor: ## Check Docker, Kubernetes and the operator, with hints to fix anything missing
	@bash hack/doctor.sh

##@ Run the demo

.PHONY: watch
watch: agentctl guard ## Live dashboard (keys 1-6 launch scenarios, q quits)
	@$(AGENTCTL) watch

.PHONY: samples
samples: guard ## Apply the five scenario AgentRuns (happy, flaky, hallucination, budget, doomed)
	@$(KUBECTL) apply -k config/samples
	@printf "$(GREEN)✔$(RESET) scenarios applied$(if $(IN_DEMO),, — watch them with: $(BOLD)make watch$(RESET))\n"

.PHONY: random
random: guard ## Create a non-deterministic "real life" run (run it a few times!)
	@$(KUBECTL) apply -f config/samples/00-namespace.yaml >/dev/null
	@$(KUBECTL) create -f config/samples/06-real-life.yaml

.PHONY: launch
launch: agentctl guard ## Launch one scenario: make launch S=flaky   (S=all launches every scenario)
	@test -n "$(S)" || { $(BIN)/agentctl scenarios; printf "\n  usage: make launch S=<scenario>\n"; exit 1; }
	@$(KUBECTL) apply -f config/samples/00-namespace.yaml >/dev/null
	@$(AGENTCTL) launch $(S)

.PHONY: get
get: guard ## kubectl get agentruns (the printer columns from the slides)
	@$(KUBECTL) -n $(NS) get agentruns

.PHONY: describe
describe: agentctl guard ## Attempt ledger of one run: make describe N=flaky-api
	@test -n "$(N)" || { echo "usage: make describe N=<agentrun>"; exit 1; }
	@$(AGENTCTL) describe $(N)

.PHONY: agent-logs
agent-logs: guard ## Logs of every attempt of a run: make agent-logs N=hallucination
	@test -n "$(N)" || { echo "usage: make agent-logs N=<agentrun>"; exit 1; }
	@for pod in $$($(KUBECTL) -n $(NS) get pods -l agents.devfest.dev/run=$(N) --sort-by=.metadata.creationTimestamp -o name); do \
		$(KUBECTL) -n $(NS) logs $$pod; done

.PHONY: events
events: guard ## Decisions the controller took, as Kubernetes Events
	@$(KUBECTL) -n $(NS) get events --field-selector involvedObject.kind=AgentRun --sort-by=.metadata.creationTimestamp

.PHONY: metrics
metrics: guard ## Scrape the controller's Prometheus metrics (agentrun_*)
	@$(KUBECTL) -n agentrun-system get --raw "/api/v1/namespaces/agentrun-system/services/agentrun-metrics:metrics/proxy/metrics" \
		| grep -E '^agentrun_' | grep -v '_bucket' || echo "no metrics yet — launch a run first"

.PHONY: verify-sandbox
verify-sandbox: guard ## Prove each sandbox layer from inside a probe pod (non-root, read-only, no token, egress)
	@bash hack/verify-sandbox.sh

.PHONY: logs
logs: guard ## Follow the controller logs
	@$(KUBECTL) -n agentrun-system logs deploy/agentrun-controller -f --tail=50

.PHONY: reset
reset: guard ## Delete every AgentRun in the demo namespace (keeps the operator)
	@$(KUBECTL) apply -f config/samples/00-namespace.yaml >/dev/null
	@$(KUBECTL) -n $(NS) delete agentruns --all --wait=true >/dev/null
	@printf "$(GREEN)✔$(RESET) clean slate in $(NS)\n"

##@ Real LLM (optional)

.PHONY: gemini-key
gemini-key: guard ## Store your Gemini API key in-cluster: make gemini-key KEY=...
	@test -n "$(KEY)" || { echo "usage: make gemini-key KEY=<key from https://aistudio.google.com/apikey>"; exit 1; }
	@$(KUBECTL) apply -f config/samples/00-namespace.yaml >/dev/null
	@$(KUBECTL) -n $(NS) create secret generic agentrun-gemini --from-literal=api-key="$(KEY)" \
		--dry-run=client -o yaml | $(KUBECTL) apply -f - >/dev/null
	@printf "$(GREEN)✔$(RESET) secret agentrun-gemini stored in $(NS) — now: make gemini\n"

.PHONY: gemini
gemini: guard ## Run a real AgentRun against the Gemini API
	@$(KUBECTL) -n $(NS) get secret agentrun-gemini >/dev/null 2>&1 || { echo "missing key — run: make gemini-key KEY=..."; exit 1; }
	@$(KUBECTL) create -f config/samples/gemini/agentrun-gemini.yaml

##@ Google Cloud (optional — GKE with gVisor + Dataplane V2)

GKE := PATH="/opt/homebrew/share/google-cloud-sdk/bin:$$PATH" TAG=$(TAG) bash hack/gke.sh
GKE_CTX = $(shell terraform -chdir=deploy/gcp/terraform output -raw kube_context 2>/dev/null)
GKE_ENV = PATH="/opt/homebrew/share/google-cloud-sdk/bin:$$PATH" USE_GKE_GCLOUD_AUTH_PLUGIN=True

.PHONY: gke-up
gke-up: ## Create everything on GCP and deploy: make gke-up PROJECT=<id> [BILLING=<account>]
	@$(GKE) up

.PHONY: gke-demo
gke-demo: gke-ctx agentctl ## The show on GKE: incident story, scoreboard, report card
	@$(GKE_ENV) SKIP_DEPLOY=1 KUBE_CONTEXT=$(GKE_CTX) TAG=$(TAG) bash hack/demo.sh

.PHONY: gke-watch
gke-watch: gke-ctx ## Dashboard against the GKE cluster
	@$(GKE_ENV) $(MAKE) --no-print-directory watch KUBE_CONTEXT=$(GKE_CTX)

.PHONY: gke-e2e
gke-e2e: gke-ctx ## Every scenario against GKE, asserting the outcome
	@$(GKE_ENV) $(MAKE) --no-print-directory e2e KUBE_CONTEXT=$(GKE_CTX)

.PHONY: gke-verify-sandbox
gke-verify-sandbox: gke-ctx ## Prove gVisor + NetworkPolicy from inside a probe pod on GKE
	@$(GKE_ENV) $(MAKE) --no-print-directory verify-sandbox KUBE_CONTEXT=$(GKE_CTX)

.PHONY: gke-deploy
gke-deploy: ## Rebuild, push and redeploy the operator on GKE
	@$(GKE) deploy

.PHONY: gke-status
gke-status: ## What is running on GCP right now
	@$(GKE) status

.PHONY: gke-down
gke-down: ## Destroy every billable GCP resource (DELETE_PROJECT=1 also deletes the project)
	@$(GKE) down

.PHONY: gke-ctx
gke-ctx:
	@test -n "$(GKE_CTX)" || { printf "no GKE demo cluster yet — run: make gke-up PROJECT=<id>\n"; exit 1; }

##@ Build & deploy

.PHONY: deploy
deploy: guard images install ## Build images and deploy the controller into the cluster
	@$(KUBECTL) kustomize config/default \
		| sed -e 's|agentrun-controller:dev|$(CONTROLLER_IMAGE)|' -e 's|agentrun-agent:dev|$(AGENT_IMAGE)|' \
		| $(KUBECTL) apply -f - >/dev/null
	@$(KUBECTL) -n agentrun-system set env deploy/agentrun-controller MOCK_SPEED=$(MOCK_SPEED) >/dev/null
	@$(KUBECTL) -n agentrun-system scale deploy/agentrun-controller --replicas=1 >/dev/null
	@$(KUBECTL) -n agentrun-system rollout status deploy/agentrun-controller --timeout=120s
	@printf "$(DIM)controller $(CONTROLLER_IMAGE) · agent $(AGENT_IMAGE)$(RESET)\n"
	@printf "$(GREEN)✔$(RESET) controller is running$(if $(IN_DEMO),, — next: $(BOLD)make samples$(RESET) and $(BOLD)make watch$(RESET))\n"

.PHONY: undeploy
undeploy: guard ## Remove the controller, CRD and every AgentRun
	-@$(KUBECTL) delete namespace $(NS) --ignore-not-found --wait=true
	-@$(KUBECTL) delete -k config/default --ignore-not-found

.PHONY: install
install: guard manifests ## Install the CRD only
	@$(KUBECTL) apply -k config/crd >/dev/null
	@$(KUBECTL) wait --for=condition=Established crd/agentruns.agents.devfest.dev --timeout=30s >/dev/null
	@printf "$(GREEN)✔$(RESET) CRD agentruns.agents.devfest.dev installed\n"

.PHONY: run
run: guard install images ## Run the controller on your machine (fast dev loop)
	@$(KUBECTL) -n agentrun-system scale deploy/agentrun-controller --replicas=0 >/dev/null 2>&1 && \
		printf "$(DIM)in-cluster controller scaled to 0 so the two don't fight — make deploy brings it back$(RESET)\n" || true
	go run ./cmd/manager --context $(KUBE_CONTEXT) --agent-image $(AGENT_IMAGE) --mock-speed $(MOCK_SPEED) --metrics-bind-address :8080 --health-probe-bind-address :8081

.PHONY: images
images: ## Build the controller and agent images
	@printf "$(BLUE)▸$(RESET) building $(CONTROLLER_IMAGE) and $(AGENT_IMAGE)\n"
	@docker build -q --build-arg BIN=manager -t $(CONTROLLER_IMAGE) . >/dev/null
	@docker build -q --build-arg BIN=agent -t $(AGENT_IMAGE) . >/dev/null
	@printf "$(GREEN)✔$(RESET) images built\n"

.PHONY: prune
prune: ## Remove old agentrun images (keeps the ones for the current source)
	@docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^agentrun-(controller|agent):' \
		| grep -v -e ':$(TAG)$$' | xargs -r docker rmi >/dev/null 2>&1 || true
	@printf "$(GREEN)✔$(RESET) old images removed\n"

.PHONY: build
build: agentctl ## Build the three binaries into ./bin
	go build -o $(BIN)/manager ./cmd/manager
	go build -o $(BIN)/agent ./cmd/agent

.PHONY: agentctl
agentctl:
	@go build -o $(BIN)/agentctl ./cmd/agentctl

##@ Quality

.PHONY: test
test: ## Unit + controller tests (no cluster needed)
	go test -race -cover ./...

.PHONY: e2e
e2e: guard ## Assert every scenario lands in the expected phase on the real cluster
	@MOCK_SPEED=$(MOCK_SPEED) bash hack/e2e.sh

.PHONY: lint
lint: ## gofmt + go vet
	@test -z "$$(gofmt -l api cmd internal)" || { gofmt -l api cmd internal; echo "run: make fmt"; exit 1; }
	go vet ./...

.PHONY: fmt
fmt: ## Format the code
	gofmt -w api cmd internal

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Regenerate CRD, RBAC and deepcopy from the Go types
	@$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...
	@$(CONTROLLER_GEN) crd:allowDangerousTypes=true rbac:roleName=agentrun-manager-role paths=./... \
		output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac

$(CONTROLLER_GEN):
	@GOBIN=$(BIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: guard
guard:
	@bash -c 'source hack/lib.sh && guard_context'
