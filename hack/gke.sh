#!/usr/bin/env bash
# The whole GKE lifecycle: up · push · deploy · context · down.
# Usage: hack/gke.sh <command>   (normally through make gke-*)
source "$(dirname "$0")/lib.sh"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TF_DIR="$ROOT/deploy/gcp/terraform"
STATE_FILE="$ROOT/deploy/gcp/.project"
REGION="${REGION:-europe-west1}"
ZONE="${ZONE:-${REGION}-b}"
export PATH="/opt/homebrew/share/google-cloud-sdk/bin:/usr/local/share/google-cloud-sdk/bin:$PATH"
export USE_GKE_GCLOUD_AUTH_PLUGIN=True

tf() { terraform -chdir="$TF_DIR" "$@"; }
tf_out() { tf output -raw "$1" 2>/dev/null; }

project() {
  if [[ -n "${PROJECT:-}" ]]; then echo "$PROJECT"; return; fi
  [[ -f "$STATE_FILE" ]] && cat "$STATE_FILE" && return
  echo ""
}

require_tools() {
  local missing=0
  for t in gcloud terraform docker kubectl; do
    command -v "$t" >/dev/null || { fail "$t not found"; missing=1; }
  done
  command -v gke-gcloud-auth-plugin >/dev/null || {
    fail "gke-gcloud-auth-plugin not found"; hint "gcloud components install gke-gcloud-auth-plugin"; missing=1; }
  [[ $missing -eq 0 ]] || die "install the missing tools and retry"
  gcloud auth print-access-token >/dev/null 2>&1 || die "gcloud is not logged in — run: gcloud auth login"
  gcloud auth application-default print-access-token >/dev/null 2>&1 \
    || die "Terraform needs application-default credentials — run: gcloud auth application-default login"
  ok "gcloud $(gcloud version 2>/dev/null | head -1 | awk '{print $4}') · terraform $(terraform version -json 2>/dev/null | sed -n 's/.*"terraform_version": *"\([^"]*\)".*/\1/p') · logged in as $(gcloud config get-value account 2>/dev/null)"
}

ensure_project() {
  local p; p="$(project)"
  [[ -n "$p" ]] || die "which project? run: make gke-up PROJECT=<new-or-existing-project-id> [BILLING=<billing-account-id>]"
  if [[ "$p" == *payment* ]]; then die "refusing to use '$p' — use a dedicated demo project"; fi

  if ! gcloud projects describe "$p" >/dev/null 2>&1; then
    step "Creating project $p"
    gcloud projects create "$p" --name="agentrun demo" --labels=purpose=devfest-demo >/dev/null || die "could not create project $p (IDs are global; try another)"
    ok "project created"
  else
    ok "project $p exists"
  fi

  if [[ "$(gcloud billing projects describe "$p" --format='value(billingEnabled)' 2>/dev/null)" != "True" ]]; then
    if [[ -z "${BILLING:-}" ]]; then
      fail "billing is not enabled on $p"
      hint "pick an account below and run again with BILLING=<ACCOUNT_ID>"
      gcloud billing accounts list --filter=open=true --format='table(name.basename():label=ACCOUNT_ID,displayName)' 2>/dev/null | sed 's/^/      /'
      exit 1
    fi
    gcloud billing projects link "$p" --billing-account="$BILLING" >/dev/null || die "could not link billing account $BILLING"
    ok "billing linked ($BILLING)"
  else
    ok "billing enabled"
  fi
  echo "$p" > "$STATE_FILE"
  PROJECT="$p"
}

kube_context() { tf_out kube_context; }

credentials() {
  local ctx previous; ctx="$(kube_context)"
  [[ -n "$ctx" ]] || die "no cluster in the Terraform state — run: make gke-up"
  previous="$(kubectl config current-context 2>/dev/null || true)"
  gcloud container clusters get-credentials "$(tf_out cluster_name)" --zone "$(tf_out location)" --project "$(project)" >/dev/null 2>&1 \
    || die "could not fetch cluster credentials"
  # get-credentials switches your current context; put yours back.
  if [[ -n "$previous" && "$previous" != "$ctx" ]]; then kubectl config use-context "$previous" >/dev/null; fi
  ok "kube context $ctx (your current context is unchanged)"
}

push_images() {
  local registry tag; registry="$(tf_out registry)"; tag="$1"
  [[ -n "$registry" ]] || die "no registry in the Terraform state — run: make gke-up"
  step "Building linux/amd64 images and pushing to Artifact Registry"
  gcloud auth print-access-token | docker login -u oauth2accesstoken --password-stdin "https://${REGION}-docker.pkg.dev" >/dev/null 2>&1 \
    || die "docker login to ${REGION}-docker.pkg.dev failed"
  for bin in manager agent; do
    local name="agentrun-controller"; [[ $bin == agent ]] && name="agentrun-agent"
    docker buildx build --quiet --platform linux/amd64 --build-arg BIN="$bin" -t "$registry/$name:$tag" --push "$ROOT" >/dev/null \
      || die "building/pushing $name failed"
    ok "$registry/$name:$tag"
  done
}

deploy() {
  local tag="$1" registry ctx; registry="$(tf_out registry)"; ctx="$(kube_context)"
  KUBE_CONTEXT="$ctx"; KUBECTL=(kubectl --context "$ctx")
  step "Deploying the operator to $ctx"
  "${KUBECTL[@]}" apply -k "$ROOT/config/crd" >/dev/null
  "${KUBECTL[@]}" wait --for=condition=Established crd/agentruns.agents.devfest.dev --timeout=60s >/dev/null
  "${KUBECTL[@]}" kustomize "$ROOT/config/default" \
    | sed -e "s|agentrun-controller:dev|$registry/agentrun-controller:$tag|" -e "s|agentrun-agent:dev|$registry/agentrun-agent:$tag|" \
    | "${KUBECTL[@]}" apply -f - >/dev/null
  "${KUBECTL[@]}" -n agentrun-system set env deploy/agentrun-controller DEFAULT_RUNTIME_CLASS=gvisor MOCK_SPEED="${MOCK_SPEED:-1}" >/dev/null
  "${KUBECTL[@]}" -n agentrun-system rollout status deploy/agentrun-controller --timeout=300s >/dev/null \
    || die "controller did not become ready — inspect: kubectl --context $ctx -n agentrun-system get pods"
  ok "controller ready · every attempt runs in gVisor"
}

wait_for_gvisor() {
  local ctx; ctx="$(kube_context)"
  for _ in $(seq 1 60); do
    kubectl --context "$ctx" get runtimeclass gvisor >/dev/null 2>&1 && { ok "RuntimeClass gvisor available"; return; }
    sleep 5
  done
  die "RuntimeClass gvisor never appeared — is the gvisor node pool up?"
}

cmd="${1:-help}"; shift || true
case "$cmd" in
  up)
    step "Preflight"
    require_tools
    ensure_project
    printf '\n  %sCost: ~2 × %s nodes + a zonal GKE cluster ≈ US$ 0.15–0.25/hour. Tear down with: make gke-down%s\n' "$YELLOW" "e2-standard-2" "$RESET"

    step "Terraform (this takes ~8-12 minutes the first time)"
    cat > "$TF_DIR/terraform.tfvars" <<EOF
project_id = "$PROJECT"
region     = "$REGION"
zone       = "$ZONE"
EOF
    tf init -input=false -upgrade >/dev/null || die "terraform init failed"
    tf apply -input=false -auto-approve || die "terraform apply failed — fix the error above and run make gke-up again (it is safe to re-run)"
    ok "infrastructure ready"

    step "Cluster access"
    credentials
    wait_for_gvisor

    tag="${TAG:?TAG is set by the Makefile}"
    push_images "$tag"
    deploy "$tag"
    printf '\n%s✔ GKE is ready.%s Next: %smake gke-demo%s · %smake gke-e2e%s · %smake gke-verify-sandbox%s · when done %smake gke-down%s\n' \
      "$GREEN$BOLD" "$RESET" "$BOLD" "$RESET" "$BOLD" "$RESET" "$BOLD" "$RESET" "$BOLD" "$RESET"
    ;;
  deploy)
    require_tools
    credentials
    push_images "${TAG:?}"
    deploy "$TAG"
    ;;
  context)
    kube_context
    ;;
  status)
    p="$(project)"; ctx="$(kube_context)"
    if [[ -z "$ctx" ]]; then warn "no GKE demo cluster in the Terraform state"; exit 0; fi
    ok "project $p · cluster $(tf_out cluster_name) in $(tf_out location)"
    gcloud container node-pools list --cluster "$(tf_out cluster_name)" --zone "$(tf_out location)" --project "$p" \
      --format='table(name,config.machineType,autoscaling.maxNodeCount,config.sandboxConfig.type)' 2>/dev/null | sed 's/^/    /'
    kubectl --context "$ctx" get nodes -L sandbox.gke.io/runtime 2>/dev/null | sed 's/^/    /'
    ;;
  down)
    require_tools
    [[ -f "$TF_DIR/terraform.tfvars" ]] || { warn "nothing to destroy"; exit 0; }
    ctx="$(kube_context || true)"
    step "Destroying the GKE demo (project $(project))"
    tf destroy -input=false -auto-approve || die "terraform destroy failed — run make gke-down again"
    if [[ -n "$ctx" ]]; then
      kubectl config delete-context "$ctx" >/dev/null 2>&1 || true
      kubectl config delete-cluster "$ctx" >/dev/null 2>&1 || true
      kubectl config delete-user "$ctx" >/dev/null 2>&1 || true
    fi
    ok "all billable resources destroyed"
    if [[ "${DELETE_PROJECT:-0}" == "1" ]]; then
      gcloud projects delete "$(project)" --quiet && rm -f "$STATE_FILE" && ok "project deleted"
    else
      hint "the (now empty) project $(project) is kept; DELETE_PROJECT=1 make gke-down removes it too"
    fi
    ;;
  *)
    echo "usage: $0 up|deploy|context|status|down"; exit 2 ;;
esac
