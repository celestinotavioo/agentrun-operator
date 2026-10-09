package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

type harness struct {
	t   *testing.T
	c   client.Client
	r   *AgentRunReconciler
	rec *events.FakeRecorder
	now time.Time
	key client.ObjectKey
}

func newHarness(t *testing.T, mutate func(*agentsv1.AgentRun)) *harness {
	t.Helper()
	s := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(s))
	utilruntime.Must(agentsv1.AddToScheme(s))
	run := &agentsv1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "triage", Namespace: "demo", UID: "uid-1"},
		Spec: agentsv1.AgentRunSpec{
			Provider:       agentsv1.ProviderMock,
			Model:          "gemini-pro-latest",
			FallbackModels: []string{"gemini-flash-latest"},
			Prompt:         "triage issue 4211",
			Tools:          []agentsv1.MCPTool{{Name: "github"}},
			Budget:         agentsv1.Budget{MaxTokens: 200_000, MaxCostUSD: "0.50"},
			Timeout:        metav1.Duration{Duration: 10 * time.Minute},
			RetryPolicy:    agentsv1.RetryPolicy{MaxAttempts: 3, QualityThreshold: 0.7, BackoffSeconds: 3},
		},
	}
	if mutate != nil {
		mutate(run)
	}
	c := fake.NewClientBuilder().WithScheme(s).
		WithStatusSubresource(&agentsv1.AgentRun{}, &batchv1.Job{}).
		WithObjects(run).Build()
	h := &harness{t: t, c: c, rec: events.NewFakeRecorder(100), now: time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC), key: client.ObjectKeyFromObject(run)}
	h.r = &AgentRunReconciler{
		Client: c, APIReader: c, Scheme: s, Recorder: h.rec,
		Jobs: JobOptions{AgentImage: "agentrun-agent:test", ImagePullPolicy: corev1.PullNever, MockSpeed: "1"},
		Now:  func() time.Time { return h.now },
	}
	return h
}

func (h *harness) reconcile() ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: h.key})
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (h *harness) run() *agentsv1.AgentRun {
	h.t.Helper()
	var r agentsv1.AgentRun
	if err := h.c.Get(context.Background(), h.key, &r); err != nil {
		h.t.Fatalf("get run: %v", err)
	}
	return &r
}

func (h *harness) job(attempt int32) *batchv1.Job {
	h.t.Helper()
	var j batchv1.Job
	if err := h.c.Get(context.Background(), client.ObjectKey{Namespace: "demo", Name: JobName(h.run(), attempt)}, &j); err != nil {
		h.t.Fatalf("get job for attempt %d: %v", attempt, err)
	}
	return &j
}

// finishJob plays the kubelet + Job controller: the agent wrote res and exited.
func (h *harness) finishJob(attempt int32, res protocol.Result) {
	h.t.Helper()
	ctx := context.Background()
	job := h.job(attempt)
	cond := batchv1.JobComplete
	exit := int32(0)
	if res.Outcome != protocol.OutcomeOK {
		cond, exit = batchv1.JobFailed, 1
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: cond, Status: corev1.ConditionTrue})
	if err := h.c.Status().Update(ctx, job); err != nil {
		h.t.Fatal(err)
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: "demo", Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  "agent",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: exit, Message: string(res.Encode())}},
		}}},
	}
	if err := h.c.Create(ctx, pod); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) expectPhase(want agentsv1.AgentRunPhase) *agentsv1.AgentRun {
	h.t.Helper()
	r := h.run()
	if r.Status.Phase != want {
		h.t.Fatalf("phase = %s (%s), want %s", r.Status.Phase, r.Status.Message, want)
	}
	return r
}

func (h *harness) expectEvent(reason string) {
	h.t.Helper()
	for {
		select {
		case e := <-h.rec.Events:
			if strings.Contains(e, reason) {
				return
			}
		default:
			h.t.Fatalf("no %q event emitted", reason)
		}
	}
}

func ok(model string, q float64) protocol.Result {
	return protocol.Result{Outcome: protocol.OutcomeOK, Model: model, InputTokens: 3000, OutputTokens: 1000, CostMicroUSD: 13_760, Quality: q, Answer: "done"}
}

func TestHappyPath(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	r := h.expectPhase(agentsv1.PhasePending)
	if r.Status.StartedAt == nil || r.Status.CostUSD != "$0.0000" {
		t.Fatalf("status not initialised: %+v", r.Status)
	}

	h.reconcile()
	h.expectPhase(agentsv1.PhaseRunning)
	job := h.job(1)
	pod := job.Spec.Template.Spec
	if *job.Spec.BackoffLimit != 0 {
		t.Error("Kubernetes must never retry an LLM call on its own (backoffLimit must be 0)")
	}
	if !*pod.Containers[0].SecurityContext.ReadOnlyRootFilesystem || *pod.AutomountServiceAccountToken {
		t.Error("attempt pods must be sandboxed")
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].Name != "triage" {
		t.Error("job must be owned by the run for garbage collection")
	}
	var np networkingv1.NetworkPolicy
	if err := h.c.Get(context.Background(), client.ObjectKey{Namespace: "demo", Name: "triage-sandbox"}, &np); err != nil {
		t.Fatalf("network policy: %v", err)
	}
	if len(np.Spec.Egress) != 0 {
		t.Error("mock runs must have no egress at all")
	}

	if res := h.reconcile(); res.RequeueAfter == 0 {
		t.Error("a running attempt should be polled")
	}
	h.expectPhase(agentsv1.PhaseRunning)

	h.finishJob(1, ok("gemini-pro-latest", 0.91))
	h.reconcile()
	r = h.expectPhase(agentsv1.PhaseSucceeded)
	if r.Status.TokensUsed != 4000 || r.Status.CostUSD != "$0.0138" || r.Status.Answer != "done" {
		t.Fatalf("usage not recorded: %+v", r.Status)
	}
	if a := r.Status.Attempts[0]; a.Outcome != agentsv1.OutcomeSucceeded || a.Quality != "0.91" || a.FinishedAt == nil {
		t.Fatalf("ledger entry wrong: %+v", a)
	}
	h.expectEvent("Succeeded")

	// Terminal runs are left alone.
	h.reconcile()
	if len(h.run().Status.Attempts) != 1 {
		t.Fatal("terminal run must not start new attempts")
	}
}

func TestTransientErrorRetriesSameModelAfterBackoff(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	h.reconcile()
	h.finishJob(1, protocol.Result{Outcome: protocol.OutcomeTransient, Reason: "429 RESOURCE_EXHAUSTED", Model: "gemini-pro-latest", InputTokens: 1200})
	res := h.reconcile()
	r := h.expectPhase(agentsv1.PhaseRetrying)
	if res.RequeueAfter != 3*time.Second {
		t.Fatalf("requeue after %s, want 3s backoff", res.RequeueAfter)
	}
	h.expectEvent("Retrying")

	// Before the backoff elapses nothing happens.
	h.now = h.now.Add(time.Second)
	h.reconcile()
	h.expectPhase(agentsv1.PhaseRetrying)

	h.now = h.now.Add(3 * time.Second)
	h.reconcile()
	r = h.expectPhase(agentsv1.PhaseRunning)
	if r.Status.Attempt != 2 || r.Status.ActiveModel != "gemini-pro-latest" {
		t.Fatalf("retry should reuse the model: %+v", r.Status)
	}
	h.finishJob(2, ok("gemini-pro-latest", 0.8))
	h.reconcile()
	r = h.expectPhase(agentsv1.PhaseSucceeded)
	if r.Status.TokensUsed != 1200+4000 {
		t.Fatalf("tokens of failed attempts must be counted too, got %d", r.Status.TokensUsed)
	}
}

func TestLowQualityFallsBackToNextModel(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	h.reconcile()
	h.finishJob(1, ok("gemini-pro-latest", 0.31))
	h.reconcile()
	r := h.expectPhase(agentsv1.PhaseFallingBack)
	if r.Status.ActiveModel != "gemini-flash-latest" || r.Status.ModelIndex != 1 {
		t.Fatalf("expected fallback to flash: %+v", r.Status)
	}
	if r.Status.Attempts[0].Outcome != agentsv1.OutcomeLowQuality {
		t.Fatalf("attempt 1 should be LowQuality, got %s", r.Status.Attempts[0].Outcome)
	}
	h.expectEvent("FallingBack")

	h.reconcile()
	r = h.expectPhase(agentsv1.PhaseRunning)
	if model := h.job(2).Spec.Template.Spec.Containers[0].Env; !hasEnv(model, protocol.EnvModel, "gemini-flash-latest") {
		t.Fatal("attempt 2 job must run the fallback model")
	}
	h.finishJob(2, ok("gemini-flash-latest", 0.84))
	h.reconcile()
	h.expectPhase(agentsv1.PhaseSucceeded)
}

func TestNoModelsLeftFails(t *testing.T) {
	h := newHarness(t, func(r *agentsv1.AgentRun) { r.Spec.FallbackModels = nil })
	h.reconcile()
	h.reconcile()
	h.finishJob(1, protocol.Result{Outcome: protocol.OutcomeFailed, Reason: "invalid tool call", Model: "gemini-pro-latest"})
	h.reconcile()
	r := h.expectPhase(agentsv1.PhaseFailed)
	if !strings.Contains(r.Status.Message, "no models left") {
		t.Fatalf("message = %q", r.Status.Message)
	}
}

func TestBudgetKillSwitch(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	h.reconcile()
	env := h.job(1).Spec.Template.Spec.Containers[0].Env
	if !hasEnv(env, protocol.EnvMaxCostMicro, "500000") {
		t.Fatal("the agent must know its remaining budget")
	}
	h.finishJob(1, protocol.Result{Outcome: protocol.OutcomeBudget, Reason: "budget exhausted", Model: "gemini-pro-latest", InputTokens: 30_000, OutputTokens: 48_000, CostMicroUSD: 517_500})
	h.reconcile()
	r := h.expectPhase(agentsv1.PhaseBudgetExceeded)
	if r.Status.CostUSD != "$0.5175" {
		t.Fatalf("cost = %s", r.Status.CostUSD)
	}
	h.expectEvent("BudgetExceeded")
}

func TestGlobalTimeoutKillsRunningJob(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	h.reconcile()
	h.now = h.now.Add(11 * time.Minute)
	h.reconcile()
	r := h.expectPhase(agentsv1.PhaseFailed)
	if !strings.Contains(r.Status.Message, "timed out") {
		t.Fatalf("message = %q", r.Status.Message)
	}
	var jobs batchv1.JobList
	_ = h.c.List(context.Background(), &jobs, client.InNamespace("demo"))
	if len(jobs.Items) != 0 {
		t.Fatal("the running attempt must be deleted when the run times out")
	}
}

func TestMissingReportIsAFailureNotACrash(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	h.reconcile()
	job := h.job(1)
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: batchv1.JobReasonDeadlineExceeded}}
	if err := h.c.Status().Update(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	r := h.expectPhase(agentsv1.PhaseRetrying)
	if r.Status.Attempts[0].Outcome != agentsv1.OutcomeTransient {
		t.Fatalf("deadline exceeded should be transient, got %s", r.Status.Attempts[0].Outcome)
	}
}

func hasEnv(env []corev1.EnvVar, name, value string) bool {
	for _, e := range env {
		if e.Name == name && e.Value == value {
			return true
		}
	}
	return false
}

func TestNeverAdoptsAJobFromAPreviousIncarnation(t *testing.T) {
	h := newHarness(t, nil)
	h.reconcile()
	// A Job with the expected name, owned by a different (deleted) run, is still around.
	stale := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name: JobName(h.run(), 1), Namespace: "demo",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: agentsv1.GroupVersion.String(), Kind: "AgentRun", Name: "triage", UID: "old-uid", Controller: ptrTrue(),
		}},
	}}
	if err := h.c.Create(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	res := h.reconcile()
	r := h.expectPhase(agentsv1.PhasePending)
	if r.Status.Attempt != 0 || res.RequeueAfter == 0 {
		t.Fatalf("must wait for the stale job to be collected, got attempt=%d requeue=%s", r.Status.Attempt, res.RequeueAfter)
	}
}

func ptrTrue() *bool { b := true; return &b }

func TestDefaultRuntimeClass(t *testing.T) {
	h := newHarness(t, nil)
	h.r.Jobs.DefaultRuntimeClass = "gvisor"
	h.reconcile()
	h.reconcile()
	if rc := h.job(1).Spec.Template.Spec.RuntimeClassName; rc == nil || *rc != "gvisor" {
		t.Fatalf("runtimeClassName = %v, want gvisor", rc)
	}

	own := "kata"
	h2 := newHarness(t, func(r *agentsv1.AgentRun) { r.Spec.Sandbox.RuntimeClassName = &own })
	h2.r.Jobs.DefaultRuntimeClass = "gvisor"
	h2.reconcile()
	h2.reconcile()
	if rc := h2.job(1).Spec.Template.Spec.RuntimeClassName; rc == nil || *rc != "kata" {
		t.Fatalf("the run's own runtimeClassName must win, got %v", rc)
	}
}
