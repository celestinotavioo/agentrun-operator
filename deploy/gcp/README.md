# Run it on Google Cloud (GKE)

The same operator, on a real GKE cluster. Here two sandbox layers that a laptop cannot give you are real:

- **GKE Sandbox (gVisor).** Every agent attempt runs on a dedicated node pool inside gVisor, a user-space kernel. The controller sets `runtimeClassName: gvisor` on every Job.
- **Dataplane V2.** The per-run NetworkPolicy is enforced by eBPF (Cilium).

Three commands: up, play, down.

```bash
make gke-up PROJECT=agentrun-demo-$RANDOM BILLING=<your-billing-account-id>
make gke-demo
make gke-down
```

## Before you start

| you need | check |
|---|---|
| a Google Cloud account with billing | `gcloud billing accounts list` |
| `gcloud` + the GKE auth plugin | `gcloud components install gke-gcloud-auth-plugin` |
| logged in twice (CLI + Terraform) | `gcloud auth login` and `gcloud auth application-default login` |
| Terraform ≥ 1.6, Docker, kubectl, Go | `make doctor` covers Docker/kubectl/Go |

`PROJECT` can be a **new ID** (it is created for you; IDs are global, so add a suffix) or an **existing project** with billing enabled. Use a dedicated project: `make gke-down` destroys everything this stack created. Run `make gke-up` without `BILLING` and it lists your billing accounts.

## What `make gke-up` does (~12 minutes the first time)

1. Creates the project (if needed), links billing, and enables the Compute, GKE, Artifact Registry and IAM APIs.
2. Runs **Terraform** ([`terraform/`](terraform)), which creates:
   - a VPC and subnet;
   - a **zonal GKE Standard** cluster (Dataplane V2, Workload Identity, REGULAR channel);
   - a `system` node pool (1 × e2-standard-2) for the controller;
   - a `gvisor` node pool (1–2 × e2-standard-2) with GKE Sandbox, where the agents run;
   - an Artifact Registry repository;
   - a least-privilege service account for the nodes.
3. Fetches cluster credentials **without changing your current kube context**.
4. Builds the controller and agent images for `linux/amd64` and pushes them to Artifact Registry.
5. Deploys the operator with `DEFAULT_RUNTIME_CLASS=gvisor`.

`make gke-up` is safe to re-run: a second run takes ~30 seconds and changes nothing.

## Play

```bash
make gke-demo             # reset, launch the five scenarios, open the dashboard
make gke-e2e              # assert every scenario lands where it should
make gke-verify-sandbox   # prove non-root, read-only FS, no SA token, NetworkPolicy and gVisor
make gke-status           # node pools and nodes (look for RUNTIME=gvisor)
make gke-deploy           # after editing code: rebuild, push, redeploy
```

Every other target works against GKE too: pass the context printed by `make gke-up`, e.g. `make describe N=hallucination KUBE_CONTEXT=gke_<project>_europe-west1-b_agentrun-demo`.

To use a real model, run `make gemini-key KEY=… KUBE_CONTEXT=<gke context>` and then `make gemini KUBE_CONTEXT=<gke context>`.

## Cost and tear-down

Two e2-standard-2 nodes, their disks and the zonal cluster cost about **US$ 0.15–0.25 per hour**. The GKE free tier covers the management fee of one zonal cluster per billing account. The mock LLM costs nothing.

```bash
make gke-down                    # destroys every billable resource; keeps the empty project
DELETE_PROJECT=1 make gke-down   # also deletes the project
```

## Safety

The Makefile and `agentctl` refuse every kube context that is not local, with one exception: contexts ending in `_agentrun-demo`, the dedicated cluster this stack creates. A demo command can never reach another cluster in your kubeconfig. Terraform state stays local in `terraform/` (git-ignored).

## Troubleshooting

| symptom | fix |
|---|---|
| `Terraform needs application-default credentials` | `gcloud auth application-default login` |
| `billing is not enabled` | re-run with `BILLING=<ACCOUNT_ID>` from the printed list |
| `could not create project` | the ID is taken globally — pick another |
| `executable gke-gcloud-auth-plugin not found` | `gcloud components install gke-gcloud-auth-plugin` |
| anything failed half-way | run `make gke-up` again; it resumes where it stopped |
