#!/usr/bin/env bash
# Run once before the talk: a full rehearsal, so nothing on stage needs the network or a rebuild.
source "$(dirname "$0")/lib.sh"
cd "$(dirname "$0")/.."

printf '\n%s🎤 Stage check%s %s— full rehearsal before the talk%s\n' "$BOLD" "$RESET" "$DIM" "$RESET"

step "1/4 Environment"
bash hack/doctor.sh >/dev/null 2>&1 || { bash hack/doctor.sh; exit 1; }
ok "doctor is green"

step "2/4 Operator"
make --no-print-directory -s deploy >/dev/null 2>&1 || { make --no-print-directory deploy; exit 1; }
ok "built and deployed ($TAG)"

step "3/4 Rehearsal (every scenario, asserted)"
bash hack/e2e.sh >/dev/null 2>&1 || { bash hack/e2e.sh; exit 1; }
ok "all five scenarios land where the talk says they do"

step "4/4 Clean stage"
"${KUBECTL[@]}" -n agents-demo delete agentruns --all --wait=true >/dev/null 2>&1 || true
ok "namespace agents-demo is empty"

cols=$(tput cols 2>/dev/null || echo 0); rows=$(tput lines 2>/dev/null || echo 0)
printf '\n%s✔ READY FOR STAGE%s\n\n' "$GREEN$BOLD" "$RESET"
printf '  On stage, run only:  %smake demo%s   (opens in seconds: no rebuild, no downloads)\n\n' "$BOLD" "$RESET"
printf '  Checklist\n'
if [[ ! -t 1 || "$cols" -eq 0 ]]; then printf '  %s□%s terminal at least 120×40 (bigger font = fewer columns)\n' "$DIM" "$RESET"
elif [[ "$cols" -ge 120 && "$rows" -ge 40 ]]; then ok "terminal is ${cols}×${rows}"
else warn "terminal is ${cols}×${rows} — make it at least 120×40 (bigger font = fewer columns)"; fi
printf '  %s□%s Do Not Disturb on (no notifications on the projector)\n' "$DIM" "$RESET"
printf '  %s□%s Docker Desktop stays open (the cluster lives there)\n' "$DIM" "$RESET"
printf '  %s□%s don'"'"'t edit the code after this check (a change triggers a ~1 min rebuild)\n\n' "$DIM" "$RESET"
