#!/usr/bin/env bash
# Checks everything the demo needs and explains how to fix what is missing.
source "$(dirname "$0")/lib.sh"
set +e
problems=0
need() { problems=$((problems + 1)); }

step "Tools"
for tool in docker kubectl go; do
  if command -v "$tool" >/dev/null; then ok "$tool $(command -v "$tool")"; else fail "$tool not found"; need; fi
done
if command -v go >/dev/null; then
  gov=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
  major=${gov%%.*}; rest=${gov#*.}; minor=${rest%%.*}
  if [[ "$major" -gt 1 || ( "$major" -eq 1 && "$minor" -ge 26 ) ]]; then
    ok "go $gov"
  else
    fail "go $gov is too old (need 1.26+)"; hint "https://go.dev/dl/"; need
  fi
fi

step "Docker"
if docker info >/dev/null 2>&1; then
  ok "docker engine is running"
else
  fail "docker engine is not reachable"; hint "start Docker Desktop"; need
fi

step "Kubernetes context: $KUBE_CONTEXT"
current=$(kubectl config current-context 2>/dev/null)
if [[ -n "$current" && "$current" != "$KUBE_CONTEXT" ]]; then
  warn "your current context is '$current' — that's fine, every command here pins --context $KUBE_CONTEXT"
fi
if ! is_local_context "$KUBE_CONTEXT"; then
  fail "'$KUBE_CONTEXT' does not look like a local cluster"; hint "use KUBE_CONTEXT=docker-desktop (or ALLOW_REMOTE=1 if you really mean it)"; need
fi
if kubectl config get-contexts -o name 2>/dev/null | grep -qx -- "$KUBE_CONTEXT"; then
  ok "context exists in kubeconfig"
  if "${KUBECTL[@]}" get --raw /readyz >/dev/null 2>&1; then
    ok "API server is ready ($("${KUBECTL[@]}" version -o json 2>/dev/null | sed -n 's/.*"gitVersion": "\(v[^"]*\)".*/\1/p' | tail -1))"
    nodes=$("${KUBECTL[@]}" get nodes --no-headers 2>/dev/null | awk '$2=="Ready"' | wc -l | tr -d ' ')
    if [[ "$nodes" -gt 0 ]]; then ok "$nodes node(s) Ready"; else fail "no Ready nodes yet"; hint "wait a minute and retry"; need; fi
  else
    fail "API server not reachable"; hint "Docker Desktop → Kubernetes may still be starting"; need
  fi
else
  fail "context '$KUBE_CONTEXT' not found"
  hint "Docker Desktop → Settings → Kubernetes → Enable Kubernetes → Apply & restart"
  need
fi

if [[ -n "${IN_DEMO:-}" ]]; then
  [[ $problems -eq 0 ]] || { printf '\n%s✘ fix the items above, then run make demo again%s\n' "$RED$BOLD" "$RESET"; exit 1; }
  printf '\n%s✔ environment ready — building and deploying…%s\n' "$GREEN$BOLD" "$RESET"
  exit 0
fi

step "Operator"
if "${KUBECTL[@]}" get crd agentruns.agents.devfest.dev >/dev/null 2>&1; then
  ok "CRD agentruns.agents.devfest.dev installed"
else
  warn "CRD not installed yet"; hint "make demo installs it"
fi
if "${KUBECTL[@]}" -n agentrun-system get deploy agentrun-controller >/dev/null 2>&1; then
  ready=$("${KUBECTL[@]}" -n agentrun-system get deploy agentrun-controller -o jsonpath='{.status.readyReplicas}' 2>/dev/null)
  if [[ "${ready:-0}" -ge 1 ]]; then ok "controller running in-cluster"; else warn "controller deployed but not ready"; hint "kubectl -n agentrun-system logs deploy/agentrun-controller"; fi
else
  warn "controller not deployed"; hint "make demo deploys it"
fi

echo
if [[ $problems -eq 0 ]]; then
  printf '%s✔ all good — try: make demo%s\n' "$GREEN$BOLD" "$RESET"
else
  printf '%s✘ %d problem(s) found — fix the items above and run make doctor again%s\n' "$RED$BOLD" "$problems" "$RESET"
  exit 1
fi
