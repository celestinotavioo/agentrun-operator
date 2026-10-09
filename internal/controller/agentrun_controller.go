// Package controller reconciles AgentRuns into sandboxed, budgeted, observable Jobs.
package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

const (
	CondReady         = "Ready"
	CondQualityPassed = "QualityPassed"

	pollInterval = 15 * time.Second
)

// AgentRunReconciler turns an AgentRun into one sandboxed Job per attempt.
type AgentRunReconciler struct {
	client.Client
	// APIReader reads Pods without caching every Pod in the cluster.
	APIReader client.Reader
	Scheme    *runtime.Scheme
	Recorder  recorder.EventRecorder
	Jobs      JobOptions
	Now       func() time.Time
}

// +kubebuilder:rbac:groups=agents.devfest.dev,resources=agentruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=agents.devfest.dev,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile moves a run one step toward a terminal phase.
func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var run agentsv1.AgentRun
	if err := r.Get(ctx, req.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if run.Status.Phase.IsTerminal() {
		return ctrl.Result{}, nil
	}

	now := r.now()
	if run.Status.Phase == "" {
		return r.initialize(ctx, &run, now)
	}
	if err := r.ensureNetworkPolicy(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}
	if left := r.timeLeft(&run, now); left <= 0 {
		return r.finish(ctx, &run, agentsv1.PhaseFailed, fmt.Sprintf("timed out after %s", run.Spec.Timeout.Duration), now)
	}

	switch run.Status.Phase {
	case agentsv1.PhasePending, agentsv1.PhaseRetrying, agentsv1.PhaseFallingBack:
		if next := run.Status.NextAttemptAt; next != nil && now.Before(next.Time) {
			return ctrl.Result{RequeueAfter: next.Sub(now)}, nil
		}
		return r.startAttempt(ctx, &run, now)
	case agentsv1.PhaseRunning:
		return r.observeAttempt(ctx, &run, now)
	}
	return ctrl.Result{}, nil
}

func (r *AgentRunReconciler) initialize(ctx context.Context, run *agentsv1.AgentRun, now time.Time) (ctrl.Result, error) {
	st := &run.Status
	st.Phase = agentsv1.PhasePending
	st.StartedAt = &metav1.Time{Time: now}
	st.ActiveModel = run.Spec.Model
	st.CostUSD = protocol.FormatUSD(0)
	st.ObservedGeneration = run.Generation
	st.Message = "accepted"
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: CondReady, Status: metav1.ConditionFalse, Reason: "Pending", Message: "waiting for the first attempt",
		ObservedGeneration: run.Generation,
	})
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	r.event(run, nil, corev1.EventTypeNormal, "Accepted", "Accept",
		"budget %s / %d tokens · %d attempts · models %v", maxCostLabel(&run.Spec), run.Spec.Budget.MaxTokens, maxAttempts(&run.Spec), run.Spec.Models())
	return ctrl.Result{}, nil
}

func (r *AgentRunReconciler) startAttempt(ctx context.Context, run *agentsv1.AgentRun, now time.Time) (ctrl.Result, error) {
	st := &run.Status
	attempt := st.Attempt + 1
	models := run.Spec.Models()
	model := models[min(int(st.ModelIndex), len(models)-1)]

	job := BuildJob(run, attempt, model, r.timeLeft(run, now), r.Jobs)
	if err := ctrl.SetControllerReference(run, job, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, job); apierrors.IsAlreadyExists(err) {
		var existing batchv1.Job
		if err := r.Get(ctx, client.ObjectKeyFromObject(job), &existing); err != nil {
			return ctrl.Result{}, err
		}
		if !metav1.IsControlledBy(&existing, run) {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}

	st.Attempt = attempt
	st.ActiveModel = model
	st.Phase = agentsv1.PhaseRunning
	st.NextAttemptAt = nil
	st.Message = fmt.Sprintf("attempt %d running on %s", attempt, model)
	st.Attempts = append(st.Attempts, agentsv1.AttemptRecord{
		Number: attempt, Model: model, Job: job.Name, StartedAt: &metav1.Time{Time: now},
	})
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	r.event(run, job, corev1.EventTypeNormal, "AttemptStarted", "CreateJob", "attempt %d on %s → job %s", attempt, model, job.Name)
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func (r *AgentRunReconciler) observeAttempt(ctx context.Context, run *agentsv1.AgentRun, now time.Time) (ctrl.Result, error) {
	st := &run.Status
	var job batchv1.Job
	jobKey := types.NamespacedName{Namespace: run.Namespace, Name: JobName(run, st.Attempt)}
	var res protocol.Result
	switch err := r.Get(ctx, jobKey, &job); {
	case apierrors.IsNotFound(err):
		res = protocol.Result{Outcome: protocol.OutcomeTransient, Reason: "attempt job disappeared", Model: st.ActiveModel}
	case err != nil:
		return ctrl.Result{}, err
	case !metav1.IsControlledBy(&job, run):
		// Leftover of a deleted run with the same name, still being garbage-collected.
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	default:
		done, failedReason := jobFinished(&job)
		if !done {
			return ctrl.Result{RequeueAfter: pollInterval}, nil
		}
		res = r.readResult(ctx, &job, failedReason, st.ActiveModel)
	}

	st.TokensUsed += res.Tokens()
	st.CostMicroUSD += res.CostMicroUSD
	st.CostUSD = protocol.FormatUSD(st.CostMicroUSD)
	if res.Outcome == protocol.OutcomeOK {
		st.Quality = fmt.Sprintf("%.2f", res.Quality)
	}
	outcome := Classify(res, threshold(&run.Spec))
	dec := Decide(&run.Spec, st, outcome, res.Reason)
	r.recordAttempt(run, res, dec, now)
	r.observeMetrics(run, res, dec)

	if res.Outcome == protocol.OutcomeOK {
		passed := metav1.ConditionTrue
		if outcome == agentsv1.OutcomeLowQuality {
			passed = metav1.ConditionFalse
		}
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{
			Type: CondQualityPassed, Status: passed, Reason: string(outcome),
			Message: fmt.Sprintf("%s scored %.2f (threshold %.2f)", res.Model, res.Quality, threshold(&run.Spec)), ObservedGeneration: run.Generation,
		})
	}

	models := run.Spec.Models()
	switch dec.Action {
	case ActSucceed:
		st.Answer = res.Answer
		return r.finish(ctx, run, agentsv1.PhaseSucceeded, dec.Reason, now)
	case ActBudget:
		return r.finish(ctx, run, agentsv1.PhaseBudgetExceeded, dec.Reason, now)
	case ActFail:
		return r.finish(ctx, run, agentsv1.PhaseFailed, dec.Reason, now)
	case ActRetry:
		st.Phase = agentsv1.PhaseRetrying
		st.NextAttemptAt = &metav1.Time{Time: now.Add(dec.Backoff)}
		st.Message = fmt.Sprintf("retrying %s in %s: %s", st.ActiveModel, dec.Backoff, dec.Reason)
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		r.event(run, &job, corev1.EventTypeWarning, "Retrying", "Retry", "attempt %d: %s → retry same model in %s", st.Attempt, dec.Reason, dec.Backoff)
		return ctrl.Result{RequeueAfter: dec.Backoff}, nil
	case ActFallback:
		from := st.ActiveModel
		st.Phase = agentsv1.PhaseFallingBack
		st.ModelIndex = dec.ModelIndex
		st.ActiveModel = models[dec.ModelIndex]
		st.NextAttemptAt = nil
		st.Message = fmt.Sprintf("falling back %s → %s: %s", from, st.ActiveModel, dec.Reason)
		if err := r.Status().Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		r.event(run, &job, corev1.EventTypeWarning, "FallingBack", "Fallback", "attempt %d: %s → %s (%s)", st.Attempt, from, st.ActiveModel, dec.Reason)
		return ctrl.Result{}, nil
	}
	return ctrl.Result{}, nil
}

// finish moves the run to a terminal phase and cleans up anything still running.
func (r *AgentRunReconciler) finish(ctx context.Context, run *agentsv1.AgentRun, phase agentsv1.AgentRunPhase, reason string, now time.Time) (ctrl.Result, error) {
	st := &run.Status
	st.Phase = phase
	st.Message = reason
	st.NextAttemptAt = nil
	st.CompletedAt = &metav1.Time{Time: now}
	ready := metav1.ConditionFalse
	if phase == agentsv1.PhaseSucceeded {
		ready = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: CondReady, Status: ready, Reason: string(phase), Message: reason, ObservedGeneration: run.Generation,
	})
	if err := r.Status().Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}

	// Kill switch: nothing may keep spending after a terminal decision.
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs, client.InNamespace(run.Namespace), client.MatchingLabels{LabelRun: sanitizeLabel(run.Name)}); err == nil {
		for i := range jobs.Items {
			if done, _ := jobFinished(&jobs.Items[i]); !done {
				_ = r.Delete(ctx, &jobs.Items[i], client.PropagationPolicy(metav1.DeletePropagationBackground))
			}
		}
	}

	runsFinished.WithLabelValues(run.Namespace, string(phase)).Inc()
	eventType := corev1.EventTypeNormal
	if phase != agentsv1.PhaseSucceeded {
		eventType = corev1.EventTypeWarning
	}
	note := fmt.Sprintf("%s · %d attempt(s) · %d tokens", reason, st.Attempt, st.TokensUsed)
	if phase != agentsv1.PhaseBudgetExceeded {
		note += " · " + st.CostUSD
	}
	r.event(run, nil, eventType, string(phase), "Finish", "%s", note)
	log.FromContext(ctx).Info("run finished", "phase", phase, "reason", reason, "cost", st.CostUSD, "tokens", st.TokensUsed)
	return ctrl.Result{}, nil
}

func (r *AgentRunReconciler) recordAttempt(run *agentsv1.AgentRun, res protocol.Result, dec Decision, now time.Time) {
	st := &run.Status
	if len(st.Attempts) == 0 {
		return
	}
	a := &st.Attempts[len(st.Attempts)-1]
	a.Outcome = dec.Outcome
	a.Reason = res.Reason
	if dec.Outcome == agentsv1.OutcomeLowQuality {
		a.Reason = dec.Reason
	}
	a.Tokens = res.Tokens()
	a.CostUSD = protocol.FormatUSD(res.CostMicroUSD)
	if res.Outcome == protocol.OutcomeOK {
		a.Quality = fmt.Sprintf("%.2f", res.Quality)
	}
	a.FinishedAt = &metav1.Time{Time: now}
}

func (r *AgentRunReconciler) observeMetrics(run *agentsv1.AgentRun, res protocol.Result, dec Decision) {
	ns, model := run.Namespace, res.Model
	if model == "" {
		model = run.Status.ActiveModel
	}
	tokensTotal.WithLabelValues(ns, model, "input").Add(float64(res.InputTokens))
	tokensTotal.WithLabelValues(ns, model, "output").Add(float64(res.OutputTokens))
	costTotal.WithLabelValues(ns, model).Add(float64(res.CostMicroUSD) / 1e6)
	attemptsTotal.WithLabelValues(ns, model, string(dec.Outcome), string(dec.Action)).Inc()
	if res.Outcome == protocol.OutcomeOK {
		quality.WithLabelValues(ns, model).Observe(res.Quality)
	}
}

// readResult extracts the agent's report from the Pod termination message,
// falling back to what Kubernetes knows when the agent could not report.
func (r *AgentRunReconciler) readResult(ctx context.Context, job *batchv1.Job, failedReason, model string) protocol.Result {
	var pods corev1.PodList
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	if err := reader.List(ctx, &pods, client.InNamespace(job.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": job.Name}); err == nil {
		for _, pod := range pods.Items {
			for _, cs := range pod.Status.ContainerStatuses {
				t := cs.State.Terminated
				if cs.Name != "agent" || t == nil {
					continue
				}
				if res, err := protocol.Decode(t.Message); err == nil {
					return res
				}
				switch {
				case t.Reason == "OOMKilled":
					return protocol.Result{Outcome: protocol.OutcomeFailed, Reason: "agent OOMKilled", Model: model}
				case t.ExitCode == protocol.ExitTransient:
					return protocol.Result{Outcome: protocol.OutcomeTransient, Reason: "agent exited as transient without a report", Model: model}
				}
			}
		}
	}
	switch failedReason {
	case "":
		return protocol.Result{Outcome: protocol.OutcomeFailed, Reason: "agent finished without a report", Model: model}
	case batchv1.JobReasonDeadlineExceeded:
		return protocol.Result{Outcome: protocol.OutcomeTransient, Reason: "attempt deadline exceeded", Model: model}
	default:
		return protocol.Result{Outcome: protocol.OutcomeFailed, Reason: "job failed: " + failedReason, Model: model}
	}
}

func (r *AgentRunReconciler) ensureNetworkPolicy(ctx context.Context, run *agentsv1.AgentRun) error {
	np := BuildNetworkPolicy(run)
	var existing networkingv1.NetworkPolicy
	err := r.Get(ctx, client.ObjectKeyFromObject(np), &existing)
	if err == nil || !apierrors.IsNotFound(err) {
		return client.IgnoreNotFound(err)
	}
	if err := ctrl.SetControllerReference(run, np, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, np); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *AgentRunReconciler) timeLeft(run *agentsv1.AgentRun, now time.Time) time.Duration {
	if run.Spec.Timeout.Duration <= 0 || run.Status.StartedAt == nil {
		return 24 * time.Hour
	}
	return run.Status.StartedAt.Add(run.Spec.Timeout.Duration).Sub(now)
}

func (r *AgentRunReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *AgentRunReconciler) event(run *agentsv1.AgentRun, related client.Object, eventType, reason, action, note string, args ...any) {
	if r.Recorder == nil {
		return
	}
	var rel runtime.Object
	if related != nil && related.GetName() != "" {
		rel = related
	}
	r.Recorder.Eventf(run, rel, eventType, reason, action, note, args...)
}

// jobFinished reports whether the Job reached Complete or Failed, and the failure reason.
func jobFinished(job *batchv1.Job) (bool, string) {
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return true, ""
		case batchv1.JobFailed:
			return true, c.Reason
		}
	}
	return false, ""
}

func maxCostLabel(spec *agentsv1.AgentRunSpec) string {
	if m := MaxCostMicro(spec); m > 0 {
		return protocol.FormatUSD(m)
	}
	return "∞"
}

// SetupWithManager wires the controller: AgentRuns in, owned Jobs watched.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agentsv1.AgentRun{}).
		Owns(&batchv1.Job{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 4}).
		Named("agentrun").
		Complete(r)
}
