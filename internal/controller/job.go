package controller

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	agentsv1 "github.com/celestinotavioo/agentrun-operator/api/v1alpha1"
	"github.com/celestinotavioo/agentrun-operator/internal/protocol"
)

const (
	LabelRun     = "agents.devfest.dev/run"
	LabelAttempt = "agents.devfest.dev/attempt"
	LabelModel   = "agents.devfest.dev/model"
)

// JobName is deterministic per run incarnation and attempt, which makes Create idempotent
// across reconciles. The UID prefix keeps a recreated run (same name) from adopting the
// previous run's Jobs while those are still being garbage-collected.
func JobName(run *agentsv1.AgentRun, attempt int32) string {
	name := run.Name
	if len(name) > 45 {
		name = name[:45]
	}
	uid := string(run.UID)
	if len(uid) > 5 {
		uid = uid[:5]
	}
	return fmt.Sprintf("%s-%s-a%d", strings.TrimRight(name, "-"), uid, attempt)
}

// JobOptions carries controller-level settings into the Job builder.
type JobOptions struct {
	AgentImage      string
	ImagePullPolicy corev1.PullPolicy
	MockSpeed       string
	// DefaultRuntimeClass applies when the run does not pick one (e.g. "gvisor" on GKE Sandbox).
	DefaultRuntimeClass string
}

// BuildJob creates the sandboxed Job for one attempt.
func BuildJob(run *agentsv1.AgentRun, attempt int32, model string, deadline time.Duration, opts JobOptions) *batchv1.Job {
	tokens, micro := Remaining(&run.Spec, &run.Status)
	tools := make([]string, 0, len(run.Spec.Tools))
	for _, t := range run.Spec.Tools {
		tools = append(tools, t.Name)
	}
	provider := string(run.Spec.Provider)
	if provider == "" {
		provider = string(agentsv1.ProviderMock)
	}
	labels := map[string]string{
		LabelRun:                       sanitizeLabel(run.Name),
		LabelAttempt:                   strconv.Itoa(int(attempt)),
		LabelModel:                     sanitizeLabel(model),
		"app.kubernetes.io/managed-by": "agentrun-operator",
		"app.kubernetes.io/component":  "agent",
	}
	env := []corev1.EnvVar{
		{Name: protocol.EnvRunName, Value: run.Name},
		{Name: protocol.EnvProvider, Value: provider},
		{Name: protocol.EnvModel, Value: model},
		{Name: protocol.EnvModelIndex, Value: strconv.Itoa(int(run.Status.ModelIndex))},
		{Name: protocol.EnvAttempt, Value: strconv.Itoa(int(attempt))},
		{Name: protocol.EnvPrompt, Value: run.Spec.Prompt},
		{Name: protocol.EnvTools, Value: strings.Join(tools, ",")},
		{Name: protocol.EnvMaxTokens, Value: strconv.FormatInt(tokens, 10)},
		{Name: protocol.EnvMaxCostMicro, Value: strconv.FormatInt(micro, 10)},
		{Name: protocol.EnvScenario, Value: run.Annotations[protocol.ScenarioAnnotation]},
		{Name: protocol.EnvSpeed, Value: opts.MockSpeed},
	}
	runtimeClass := run.Spec.Sandbox.RuntimeClassName
	if runtimeClass == nil && opts.DefaultRuntimeClass != "" {
		runtimeClass = ptr.To(opts.DefaultRuntimeClass)
	}
	deadlineSec := int64(deadline.Seconds())
	if deadlineSec < 10 {
		deadlineSec = 10
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      JobName(run, attempt),
			Namespace: run.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			// The controller owns retries: Kubernetes must never re-run an LLM call on its own.
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   &deadlineSec,
			TTLSecondsAfterFinished: ptr.To[int32](3600),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:                 corev1.RestartPolicyNever,
					RuntimeClassName:              runtimeClass,
					AutomountServiceAccountToken:  ptr.To(false),
					EnableServiceLinks:            ptr.To(false),
					TerminationGracePeriodSeconds: ptr.To[int64](10),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To[int64](65532),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:                     "agent",
						Image:                    opts.AgentImage,
						ImagePullPolicy:          opts.ImagePullPolicy,
						Env:                      env,
						TerminationMessagePolicy: corev1.TerminationMessageReadFile,
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("10m"),
								corev1.ResourceMemory: resource.MustParse("16Mi"),
							},
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("128Mi"),
							},
						},
					}},
				},
			},
		},
	}
}

// BuildNetworkPolicy denies all ingress and all egress: the mock agent needs no network.
func BuildNetworkPolicy(run *agentsv1.AgentRun) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      run.Name + "-sandbox",
			Namespace: run.Namespace,
			Labels:    map[string]string{LabelRun: sanitizeLabel(run.Name), "app.kubernetes.io/managed-by": "agentrun-operator"},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelRun: sanitizeLabel(run.Name)}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{},
			Egress:      []networkingv1.NetworkPolicyEgressRule{},
		},
	}
}

func sanitizeLabel(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			b[i] = '-'
		}
	}
	out := strings.Trim(string(b), "-_.")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-_.")
	}
	return out
}
