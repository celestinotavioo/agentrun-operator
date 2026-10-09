package llm

import (
	"context"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mockRun(t *testing.T, scenario string, attempt, modelIdx int, model string) (Response, *Error) {
	t.Helper()
	m := &Mock{Rand: rand.New(rand.NewPCG(1, 2))}
	resp, err := m.Run(context.Background(), Request{
		Model: model, Prompt: "triage", Tools: []string{"github"}, Attempt: attempt, ModelIndex: modelIdx,
		RemainingTok: 200_000, RemainingMicro: 500_000, Scenario: scenario, Speed: 1000,
	})
	if err != nil {
		return resp, Classify(err)
	}
	return resp, nil
}

func TestMockScenarios(t *testing.T) {
	if r, err := mockRun(t, ScenarioHappy, 1, 0, "gemini-pro-latest"); err != nil || r.Quality < 0.7 || r.OutputTokens == 0 {
		t.Fatalf("happy: %+v %v", r, err)
	}
	if _, err := mockRun(t, ScenarioFlaky, 1, 0, "gemini-pro-latest"); err == nil || err.Kind != KindTransient {
		t.Fatalf("flaky attempt 1 should be transient, got %v", err)
	}
	if _, err := mockRun(t, ScenarioFlaky, 3, 0, "gemini-pro-latest"); err != nil {
		t.Fatalf("flaky attempt 3 should succeed, got %v", err)
	}
	if r, _ := mockRun(t, ScenarioHallucinate, 1, 0, "gemini-pro-latest"); r.Quality >= 0.7 {
		t.Fatalf("hallucinate on the primary model must fail the quality gate, got %.2f", r.Quality)
	}
	if r, _ := mockRun(t, ScenarioHallucinate, 2, 1, "gemini-flash-latest"); r.Quality < 0.7 {
		t.Fatalf("hallucinate on the fallback model must pass, got %.2f", r.Quality)
	}
	if r, err := mockRun(t, ScenarioBudgetBuster, 1, 0, "gemini-pro-latest"); err == nil || err.Kind != KindBudget || r.InputTokens == 0 {
		t.Fatalf("budget-buster must stop itself and report usage: %+v %v", r, err)
	}
	if _, err := mockRun(t, ScenarioDoomed, 1, 0, "gemini-pro-latest"); err == nil || err.Kind != KindFailed {
		t.Fatalf("doomed must fail hard, got %v", err)
	}
}

func TestCost(t *testing.T) {
	if got := CostMicroUSD("gemini-pro-latest", 1_000_000, 0); got != 1_250_000 {
		t.Fatalf("1M pro input tokens = %d micro-USD, want 1.25 USD", got)
	}
	if PriceFor("gemini-flash-lite-latest") != (Price{0.10, 0.40}) {
		t.Fatal("flash-lite must not be priced as flash")
	}
}

func TestGeminiClassifiesErrors(t *testing.T) {
	status := http.StatusTooManyRequests
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "k" {
			t.Errorf("api key not sent in header")
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`))
			return
		}
		if strings.Contains(r.URL.Path, JudgeModel) {
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"{\"score\": 0.83}"}]}}],"usageMetadata":{"promptTokenCount":50,"candidatesTokenCount":5}}`))
			return
		}
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}]}}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,"thoughtsTokenCount":7}}`))
	}))
	defer srv.Close()
	g := NewGemini("k", srv.URL)

	_, err := g.Run(context.Background(), Request{Model: "gemini-pro-latest", Prompt: "p", RemainingTok: 10_000})
	if e := Classify(err); err == nil || e.Kind != KindTransient {
		t.Fatalf("429 must be transient, got %v", err)
	}

	status = http.StatusOK
	resp, err := g.Run(context.Background(), Request{Model: "gemini-pro-latest", Prompt: "p", RemainingTok: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "answer" || resp.Quality != 0.83 || resp.InputTokens != 150 || resp.OutputTokens != 32 {
		t.Fatalf("unexpected response %+v", resp)
	}
}
