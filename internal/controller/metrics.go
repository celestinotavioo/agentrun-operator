package controller

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	tokensTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentrun_tokens_total",
		Help: "Tokens consumed by agent attempts.",
	}, []string{"namespace", "model", "kind"})

	costTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentrun_cost_usd_total",
		Help: "Dollars spent by agent attempts.",
	}, []string{"namespace", "model"})

	attemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentrun_attempts_total",
		Help: "Finished attempts by outcome and the decision taken after them.",
	}, []string{"namespace", "model", "outcome", "decision"})

	runsFinished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "agentrun_runs_finished_total",
		Help: "Runs that reached a terminal phase.",
	}, []string{"namespace", "phase"})

	quality = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "agentrun_quality_score",
		Help:    "Eval score of successful-looking attempts.",
		Buckets: []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1},
	}, []string{"namespace", "model"})
)

func init() {
	metrics.Registry.MustRegister(tokensTotal, costTotal, attemptsTotal, runsFinished, quality)
}
