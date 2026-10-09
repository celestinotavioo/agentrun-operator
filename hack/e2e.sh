#!/usr/bin/env bash
# End-to-end: apply every scenario against the real cluster and assert where each one lands.
source "$(dirname "$0")/lib.sh"
guard_context

NS=agents-demo
TIMEOUT="${E2E_TIMEOUT:-300}"
RUNS="triage-issue-4211 flaky-api hallucination budget-buster doomed"
expect() {
  case "$1" in
    budget-buster) echo BudgetExceeded ;;
    doomed) echo Failed ;;
    *) echo Succeeded ;;
  esac
}

step "Preflight"
"${KUBECTL[@]}" get crd agentruns.agents.devfest.dev >/dev/null 2>&1 || die "CRD not installed — run: make deploy"
ready=$("${KUBECTL[@]}" -n agentrun-system get deploy agentrun-controller -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)
if [[ "${ready:-0}" -lt 1 ]] && ! pgrep -f "cmd/manager|/manager --context" >/dev/null; then
  die "no controller is running — run: make deploy   (or make run in another terminal)"
fi
ok "CRD installed and a controller is running"

step "Resetting scenario runs"
"${KUBECTL[@]}" apply -f config/samples/00-namespace.yaml >/dev/null
"${KUBECTL[@]}" -n "$NS" delete agentruns --all --wait=true >/dev/null 2>&1 || true
"${KUBECTL[@]}" apply -k config/samples >/dev/null
ok "applied 5 scenarios"

step "Waiting for terminal phases (timeout ${TIMEOUT}s)"
deadline=$((SECONDS + TIMEOUT))
while :; do
  pending=0
  for name in $RUNS; do
    phase=$("${KUBECTL[@]}" -n "$NS" get agentrun "$name" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    case "$phase" in Succeeded|Failed|BudgetExceeded) ;; *) pending=$((pending + 1)) ;; esac
  done
  [[ $pending -eq 0 ]] && break
  if [[ $SECONDS -ge $deadline ]]; then fail "timed out with $pending run(s) still active"; break; fi
  if [[ -t 1 ]]; then
    printf '\r  %s%d run(s) still active… %ss%s ' "$DIM" "$pending" "$((deadline - SECONDS))" "$RESET"
  elif (( SECONDS % 15 < 3 )); then
    echo "  $pending run(s) still active…"
  fi
  sleep 3
done
[[ -t 1 ]] && printf '\r%60s\r' ''

step "Results"
failures=0
for name in $RUNS; do
  read -r phase attempt model cost <<<"$("${KUBECTL[@]}" -n "$NS" get agentrun "$name" \
    -o jsonpath='{.status.phase} {.status.attempt} {.status.activeModel} {.status.costUSD}' 2>/dev/null || echo '? ? ? ?')"
  line=$(printf '%-18s %-15s attempts=%-2s %-26s %s' "$name" "$phase" "$attempt" "$model" "$cost")
  if [[ "$phase" == "$(expect "$name")" ]]; then ok "$line"; else fail "$line  (want $(expect "$name"))"; failures=$((failures + 1)); fi
done

hall_model=$("${KUBECTL[@]}" -n "$NS" get agentrun hallucination -o jsonpath='{.status.activeModel}' 2>/dev/null || true)
if [[ "$hall_model" == "gemini-flash-latest" ]]; then ok "hallucination fell back to gemini-flash-latest"; else fail "hallucination should end on gemini-flash-latest, got '$hall_model'"; failures=$((failures + 1)); fi
flaky_attempts=$("${KUBECTL[@]}" -n "$NS" get agentrun flaky-api -o jsonpath='{.status.attempt}' 2>/dev/null || true)
if [[ "$flaky_attempts" == "3" ]]; then ok "flaky-api retried twice on the same model"; else fail "flaky-api should take 3 attempts, took '$flaky_attempts'"; failures=$((failures + 1)); fi

echo
if [[ $failures -eq 0 ]]; then
  printf '%s✔ e2e passed%s\n' "$GREEN$BOLD" "$RESET"
else
  printf '%s✘ e2e failed (%d)%s — inspect with: make watch / make logs\n' "$RED$BOLD" "$failures" "$RESET"
  exit 1
fi
