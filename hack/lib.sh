#!/usr/bin/env bash
# Shared helpers: pretty output and the local-context guard.
set -euo pipefail

if [[ -t 1 ]]; then
  BLUE=$'\033[38;5;33m' GREEN=$'\033[38;5;35m' YELLOW=$'\033[38;5;214m' RED=$'\033[38;5;203m'
  DIM=$'\033[2m' BOLD=$'\033[1m' RESET=$'\033[0m'
else
  BLUE='' GREEN='' YELLOW='' RED='' DIM='' BOLD='' RESET=''
fi

KUBE_CONTEXT="${KUBE_CONTEXT:-docker-desktop}"
KUBECTL=(kubectl --context "$KUBE_CONTEXT")

step() { printf '\n%s▸ %s%s\n' "$BOLD$BLUE" "$*" "$RESET"; }
ok()   { printf '  %s✔%s %s\n' "$GREEN" "$RESET" "$*"; }
warn() { printf '  %s!%s %s\n' "$YELLOW" "$RESET" "$*"; }
fail() { printf '  %s✘%s %s\n' "$RED" "$RESET" "$*"; }
hint() { printf '    %s↳ %s%s\n' "$DIM" "$*" "$RESET"; }
die()  { fail "$*"; exit 1; }

is_local_context() {
  case "$1" in
    docker-desktop|minikube|orbstack|rancher-desktop|colima*|kind-*|k3d-*) return 0 ;;
    *) return 1 ;;
  esac
}

guard_context() {
  if ! is_local_context "$KUBE_CONTEXT" && [[ "${ALLOW_REMOTE:-0}" != "1" ]]; then
    die "context '$KUBE_CONTEXT' is not a local cluster — refusing to touch it (set ALLOW_REMOTE=1 to override)"
  fi
  if ! kubectl config get-contexts -o name 2>/dev/null | grep -qx -- "$KUBE_CONTEXT"; then
    fail "kubeconfig has no context named '$KUBE_CONTEXT'"
    hint "Docker Desktop → Settings → Kubernetes → Enable Kubernetes → Apply & restart"
    hint "using another local cluster? run: make <target> KUBE_CONTEXT=kind-mycluster"
    exit 1
  fi
}
