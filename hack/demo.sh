#!/usr/bin/env bash
# The on-stage entry point: banner, compact checks, deploy only if needed, then the show.
source "$(dirname "$0")/lib.sh"
cd "$(dirname "$0")/.."

NS="${NS:-agents-demo}"
TAG="${TAG:?run through make demo}"
LOG="$(mktemp "${TMPDIR:-/tmp}/agentrun-demo.XXXXXX")"

spin() {
  local pid=$1 msg=$2 i=0 cols
  local frames=("◐" "◓" "◑" "◒")
  cols=$(tput cols 2>/dev/null || echo 80)
  # A line wider than the terminal wraps and \r can no longer redraw it.
  msg="${msg:0:$((cols - 6))}"
  while kill -0 "$pid" 2>/dev/null; do
    printf '\r\033[K  %s%s%s %s' "$BLUE" "${frames[i % 4]}" "$RESET" "$msg"
    i=$((i + 1)); sleep 0.12
  done
  printf '\r\033[K'
  wait "$pid"
}

banner() {
  local b=$'\033[38;5;33m' r=$'\033[38;5;203m' y=$'\033[38;5;214m' g=$'\033[38;5;35m'
  [[ -t 1 ]] || { b='' r='' y='' g=''; }
  clear 2>/dev/null || true
  printf '\n  %s●%s %s●%s %s●%s %s●%s\n' "$b" "$RESET" "$r" "$RESET" "$y" "$RESET" "$g" "$RESET"
  printf '  %s⎈ AgentRun Operator%s\n' "$BOLD" "$RESET"
  printf '  %sKubernetes Operators for AI Agents · orchestrating LLMs like we orchestrate Pods%s\n' "$DIM" "$RESET"
  printf '  %sOtavio Celestino · Google Developer Expert, Go%s\n\n' "$DIM" "$RESET"
}

# Everything runs inside main so bash parses the whole file before executing any of it.
main() {
  banner

  # 1. cluster
  guard_context
  if ! "${KUBECTL[@]}" get --raw /readyz >/dev/null 2>&1; then
    fail "the cluster '$KUBE_CONTEXT' is not answering"
    bash hack/doctor.sh || true
    exit 1
  fi
  version=$("${KUBECTL[@]}" version -o json 2>/dev/null | sed -n 's/.*"gitVersion": "\(v[^"]*\)".*/\1/p' | tail -1)
  ok "cluster   $KUBE_CONTEXT · Kubernetes $version"

  # 2. operator (fast path when the deployed image already matches the source)
  if [[ "${SKIP_DEPLOY:-0}" == "1" ]]; then
    ok "operator  managed outside this script"
  else
    deployed=$("${KUBECTL[@]}" -n agentrun-system get deploy agentrun-controller -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)
    ready=$("${KUBECTL[@]}" -n agentrun-system get deploy agentrun-controller -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)
    if [[ "$deployed" == "agentrun-controller:$TAG" && "${ready:-0}" -ge 1 ]]; then
      ok "operator  up to date ($TAG)"
    else
      make --no-print-directory -s deploy >"$LOG" 2>&1 &
      if spin $! "building + deploying the operator (~1 min)"; then
        ok "operator  deployed ($TAG)"
      else
        fail "deploy failed — last lines:"; tail -15 "$LOG" | sed 's/^/      /'; exit 1
      fi
    fi
  fi

  # 3. clean stage
  "${KUBECTL[@]}" apply -f config/samples/00-namespace.yaml >/dev/null
  "${KUBECTL[@]}" -n "$NS" delete agentruns --all --wait=true >/dev/null 2>&1 &
  spin $! "clearing the stage" || true
  ok "stage     namespace $NS is clean"

  printf '\n  %s▶ starting the show%s %s(space = next act · p = pause · r = report card · q = quit)%s\n' "$BOLD$GREEN" "$RESET" "$DIM" "$RESET"
  sleep 1.2
  exec bin/agentctl --context "$KUBE_CONTEXT" -n "$NS" show
}

main "$@"
