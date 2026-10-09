# Workshop — Kubernetes Operators for AI Agents

Hands-on labs that follow the talk. Each lab takes 10–20 minutes and builds on the previous one.
Run everything from the repository root. When in doubt: `make doctor`.

| Lab | You will… | Slides |
|---|---|---|
| 0 | get a cluster and the tools ready | — |
| 1 | install the CRD and read the API | 9–12 |
| 2 | run the controller on your laptop and watch it reconcile | 8, 15–17 |
| 3 | break things on purpose: retries, fallbacks, failure | 13, 20 |
| 4 | spend money and hit the kill switch | 19 |
| 5 | prove the sandbox instead of trusting it | 18 |
| 6 | change the policy with a test first | 16–17, 23 |
| 7 | (optional) talk to a real model | — |
| 8 | discuss: worth it or over-engineering? | 22–24 |

---

## Lab 0 — Setup

1. Docker Desktop → Settings → Kubernetes → **Enable Kubernetes** → **Apply**. Wait for the green Kubernetes icon.
2. Clone the repo and run:

```bash
make doctor
```

Every line should be green except the "Operator" section (we install it next). Doctor prints a fix for anything else.

> Your shell may point at a real cluster. That's fine: every `make` target pins `--context docker-desktop` and refuses remote contexts.

## Lab 1 — The API is the product

```bash
make install                                  # just the CRD, no controller yet
kubectl --context docker-desktop explain agentrun.spec
kubectl --context docker-desktop explain agentrun.spec.retryPolicy
```

Now create a run **without a controller**:

```bash
kubectl --context docker-desktop apply -f config/samples/00-namespace.yaml
kubectl --context docker-desktop apply -f config/samples/01-triage-happy.yaml
make get
```

**Observe:** the object is stored and validated, but nothing happens — `PHASE` is empty. A CRD is only a contract; somebody has to *reconcile* it.

**Try:** set `qualityThreshold: 1.5` or `maxCostUSD: "cheap"` in a copy of the sample and apply it. Who rejects it, and where is that rule written? (Hint: the `+kubebuilder:validation` markers in [`api/v1alpha1/agentrun_types.go`](../api/v1alpha1/agentrun_types.go).)

## Lab 2 — Watch the loop

Open two terminals.

```bash
# terminal 1 — the controller, on your laptop
make run

# terminal 2 — the dashboard
make watch
```

The run you created in Lab 1 is picked up immediately: `Pending → Running → Succeeded`. Press `1` in the dashboard to launch another happy run.

**Read:** [`Reconcile`](../internal/controller/agentrun_controller.go) — find the three branches: *initialise*, *start an attempt*, *observe an attempt*. Each call moves the run exactly one step.

**Try:** stop the controller (Ctrl-C) while a run is `Running`, wait for its Job to finish, start the controller again. Does the run still finish correctly? Why? (Level-triggered: the controller reads the *current* state, it does not need to have seen the event.)

## Lab 3 — Break things

In the dashboard press `2` (flaky), `3` (hallucinate) and `5` (doomed). Select a run with ↑↓ and read its ledger.

```bash
make events                         # every decision, explained
make describe N=<run-name>          # the attempt ledger
make agent-logs N=<run-name>        # what the agent did inside each attempt
```

**Questions**

- `flaky` retried the *same* model; `hallucinate` switched model. What made the difference? Find it in [`Decide`](../internal/controller/decide.go).
- Why does a `TransientError` wait before the next attempt, but a fallback starts immediately?
- `doomed` stops after 3 attempts although `maxAttempts` is 5. Why?

## Lab 4 — Money

```bash
make launch S=budget-buster
make metrics
```

**Observe:** the agent stops *itself* (it knows its remaining budget from `AGENTRUN_REMAINING_COST_MICRO_USD`) and the controller marks the run `BudgetExceeded`. Look at the final cost: it overshoots the budget slightly. Why can't the controller prevent that completely? What would you add on the provider side?

**Try:** apply a copy of `04-budget-buster.yaml` with `maxCostUSD: "1.00"`. Does the outcome change? Now set `maxTokens: 20000` instead. Which limit trips first?

## Lab 5 — Prove the sandbox

```bash
make verify-sandbox
```

It starts a probe pod shaped like an attempt Job and *tests* each layer: non-root, read-only root filesystem, no service-account token, egress blocked by NetworkPolicy (it measures reachability before and after applying the policy).

**Try:**

- Remove `readOnlyRootFilesystem` from the probe in [`hack/verify-sandbox.sh`](../hack/verify-sandbox.sh) and run it again.
- Run as root (`runAsUser: 0`). Who refuses the pod? (Look at the namespace labels in `config/samples/00-namespace.yaml`.)
- Discuss: which of these layers would stop a prompt-injected agent from exfiltrating secrets? Which wouldn't?

## Lab 6 — Change the policy, test first

Product asks: *"a low-quality answer should get one more try on the same model before we fall back — sometimes it's just a bad sample."*

1. Add a field to `RetryPolicy` in [`api/v1alpha1/agentrun_types.go`](../api/v1alpha1/agentrun_types.go):

   ```go
   // RetryLowQualityOnce retries the same model once before falling back on a low score.
   // +optional
   RetryLowQualityOnce bool `json:"retryLowQualityOnce,omitempty"`
   ```

2. `make manifests` — regenerates the CRD and deepcopy.
3. Write the failing test first in [`decide_test.go`](../internal/controller/decide_test.go): a `LowQuality` outcome on the first attempt of a model should now return `ActRetry`; the second one should return `ActFallback`.
   *Hint:* the ledger (`status.Attempts`) tells you how many attempts the current model already had.
4. `make test` until green, then `make deploy` and `make launch S=hallucinate` to see it live.

**Stretch goals**

- A `agents.devfest.dev/cancel: "true"` annotation that moves a run to `Failed` and deletes its running Job.
- A per-namespace daily budget (hint: a second CRD, or a ResourceQuota-like object the controller sums against).

## Lab 7 — A real model (optional)

```bash
make gemini-key KEY=<key from https://aistudio.google.com/apikey>
make gemini
make watch
```

Run it three times. Same spec — compare tokens, cost and quality. That's the non-determinism the reconcile loop was never designed for.

## Lab 8 — Discussion

Use the quadrant on slide 24. For each, decide: script + cron, Job + policies, queue + workers, or an operator?

- a hackathon Slack bot
- a nightly compliance report agent for one team
- support-ticket triage for 5 teams sharing one LLM budget
- an internal platform where any team can submit agent runs

What would make you *remove* this operator later?
