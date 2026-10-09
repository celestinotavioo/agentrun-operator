#!/usr/bin/env bash
# Proves each sandbox layer empirically, from inside a pod shaped exactly like an attempt Job.
source "$(dirname "$0")/lib.sh"
guard_context
NS=agents-demo
POD=sandbox-probe

"${KUBECTL[@]}" apply -f config/samples/00-namespace.yaml >/dev/null
"${KUBECTL[@]}" -n "$NS" delete "pod/$POD" "networkpolicy/$POD-sandbox" --ignore-not-found --wait=true >/dev/null 2>&1

RUNTIME_CLASS=""
if "${KUBECTL[@]}" get runtimeclass gvisor >/dev/null 2>&1; then RUNTIME_CLASS="runtimeClassName: gvisor"; fi

step "Creating a probe pod with the same sandbox as an attempt Job"
cat <<YAML | "${KUBECTL[@]}" -n "$NS" apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: $POD
  labels: {agents.devfest.dev/run: $POD}
spec:
  restartPolicy: Never
  $RUNTIME_CLASS
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: probe
      image: mirror.gcr.io/library/busybox:1.36
      command: ["sleep", "120"]
      securityContext:
        allowPrivilegeEscalation: false
        readOnlyRootFilesystem: true
        capabilities: {drop: ["ALL"]}
YAML
"${KUBECTL[@]}" -n "$NS" wait --for=condition=Ready pod/"$POD" --timeout=90s >/dev/null || die "probe pod did not start"
ok "admitted by Pod Security 'restricted' in namespace $NS"

probe() { "${KUBECTL[@]}" -n "$NS" exec "$POD" -- sh -c "$1" >/dev/null 2>&1; }

step "Results"
if [[ "$("${KUBECTL[@]}" -n "$NS" exec "$POD" -- id -u)" == "65532" ]]; then ok "runs as non-root (uid 65532)"; else fail "runs as root"; fi
if probe 'touch /pwned'; then fail "root filesystem is writable"; else ok "root filesystem is read-only"; fi
if probe 'test -e /var/run/secrets/kubernetes.io/serviceaccount/token'; then fail "service account token is mounted"; else ok "no service account token to steal"; fi
TARGET="http://agentrun-metrics.agentrun-system.svc.cluster.local:8080/metrics"
if ! probe "wget -T 3 -qO- $TARGET"; then
  warn "egress check inconclusive: the probe cannot reach the controller even without a policy"
  hint "is the controller deployed? make deploy"
else
  cat <<YAML | "${KUBECTL[@]}" -n "$NS" apply -f - >/dev/null
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: $POD-sandbox}
spec:
  podSelector: {matchLabels: {agents.devfest.dev/run: $POD}}
  policyTypes: [Ingress, Egress]
YAML
  sleep 5
  if probe "wget -T 3 -qO- $TARGET"; then
    warn "egress is NOT blocked — this cluster's CNI ignores NetworkPolicy"
    hint "Docker Desktop's kindnet does not enforce NetworkPolicy; GKE Dataplane V2, Cilium and Calico do."
    hint "the policy object is still created per run and would be enforced there — trust, but verify."
  else
    ok "egress blocked by NetworkPolicy (reachable before, unreachable after)"
  fi
fi
if [[ -n "$RUNTIME_CLASS" ]]; then
  if "${KUBECTL[@]}" -n "$NS" exec "$POD" -- dmesg 2>/dev/null | grep -qi gvisor; then
    ok "running inside gVisor: the kernel the agent sees is a user-space sandbox, not the node's"
  else
    fail "RuntimeClass gvisor exists but the probe is not running inside gVisor"
  fi
else
  warn "no gvisor RuntimeClass on this cluster (expected locally; make gke-up gives you one)"
fi

"${KUBECTL[@]}" -n "$NS" delete "pod/$POD" "networkpolicy/$POD-sandbox" --ignore-not-found --wait=false >/dev/null 2>&1 || true
