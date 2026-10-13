// Package llm holds the providers the sandboxed agent can talk to.
package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Request is one attempt's worth of work.
type Request struct {
	Model          string
	Prompt         string
	Tools          []string
	Attempt        int
	ModelIndex     int
	RemainingTok   int64
	RemainingMicro int64
	Scenario       string
	Speed          float64
	// Step reports progress so the agent can render it in the pod logs.
	Step func(icon, format string, args ...any)
}

// Response is a finished answer with its usage and eval score.
type Response struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
	Quality      float64
}

// Provider runs a prompt against a model.
type Provider interface {
	Run(ctx context.Context, req Request) (Response, error)
}

// ErrorKind classifies provider failures for the controller's retry/fallback decision.
type ErrorKind int

const (
	KindFailed ErrorKind = iota
	KindTransient
	KindBudget
)

// Error is a classified provider failure. Usage is what was spent before failing.
type Error struct {
	Kind   ErrorKind
	Reason string
	Usage  Response
}

func (e *Error) Error() string { return e.Reason }

// Transient builds a retryable error.
func Transient(reason string, usage Response) error {
	return &Error{Kind: KindTransient, Reason: reason, Usage: usage}
}

// Failed builds a non-retryable error (the controller falls back to the next model).
func Failed(reason string, usage Response) error {
	return &Error{Kind: KindFailed, Reason: reason, Usage: usage}
}

// Budget builds a budget-exhausted error.
func Budget(reason string, usage Response) error {
	return &Error{Kind: KindBudget, Reason: reason, Usage: usage}
}

// Classify extracts a classified error, treating unknown errors as hard failures.
func Classify(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Kind: KindTransient, Reason: "deadline exceeded"}
	}
	return &Error{Kind: KindFailed, Reason: err.Error()}
}

// Price is USD per 1M tokens.
type Price struct{ In, Out float64 }

// Illustrative list prices (USD / 1M tokens). Check https://ai.google.dev/pricing before quoting them.
var prices = []struct {
	match string
	price Price
}{
	{"flash-lite", Price{0.10, 0.40}},
	{"flash", Price{0.30, 2.50}},
	{"pro", Price{1.25, 10.00}},
}

// PriceFor returns the price of a model, matching on its family name.
func PriceFor(model string) Price {
	m := strings.ToLower(model)
	for _, p := range prices {
		if strings.Contains(m, p.match) {
			return p.price
		}
	}
	return Price{1.00, 4.00}
}

// CostMicroUSD converts token usage into millionths of a dollar.
func CostMicroUSD(model string, in, out int64) int64 {
	p := PriceFor(model)
	return int64(float64(in)*p.In + float64(out)*p.Out)
}

// New returns the provider by name.
func New(name string) (Provider, error) {
	switch name {
	case "", "mock":
		return &Mock{}, nil
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
}
