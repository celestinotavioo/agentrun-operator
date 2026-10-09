package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// JudgeModel scores answers (LLM-as-judge). Cheap and fast on purpose.
const JudgeModel = "gemini-flash-lite-latest"

// Gemini calls the Gemini API over plain REST, so the agent image needs no SDK.
type Gemini struct {
	key     string
	baseURL string
	http    *http.Client
}

// NewGemini builds a client. baseURL is overridable for tests.
func NewGemini(key, baseURL string) *Gemini {
	if baseURL == "" {
		baseURL = geminiBaseURL
	}
	return &Gemini{key: key, baseURL: baseURL, http: &http.Client{Timeout: 120 * time.Second}}
}

type gPart struct {
	Text string `json:"text,omitempty"`
}
type gContent struct {
	Role  string  `json:"role,omitempty"`
	Parts []gPart `json:"parts"`
}
type gRequest struct {
	Contents         []gContent     `json:"contents"`
	GenerationConfig map[string]any `json:"generationConfig,omitempty"`
}
type gResponse struct {
	Candidates []struct {
		Content gContent `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

func (g *Gemini) generate(ctx context.Context, model, prompt string, cfg map[string]any) (string, Response, error) {
	body, _ := json.Marshal(gRequest{
		Contents:         []gContent{{Role: "user", Parts: []gPart{{Text: prompt}}}},
		GenerationConfig: cfg,
	})
	url := fmt.Sprintf("%s/models/%s:generateContent", g.baseURL, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", Response{}, Failed(err.Error(), Response{})
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.key)

	resp, err := g.http.Do(req)
	if err != nil {
		return "", Response{}, Transient("network error: "+err.Error(), Response{})
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var gr gResponse
	_ = json.Unmarshal(raw, &gr)
	usage := Response{
		InputTokens:  gr.UsageMetadata.PromptTokenCount,
		OutputTokens: gr.UsageMetadata.CandidatesTokenCount + gr.UsageMetadata.ThoughtsTokenCount,
	}
	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
		if gr.Error != nil {
			msg = fmt.Sprintf("%d %s: %s", gr.Error.Code, gr.Error.Status, firstLine(gr.Error.Message))
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return "", usage, Transient(msg, usage)
		}
		return "", usage, Failed(msg, usage)
	}
	var sb strings.Builder
	for _, c := range gr.Candidates {
		for _, p := range c.Content.Parts {
			sb.WriteString(p.Text)
		}
	}
	if sb.Len() == 0 {
		return "", usage, Failed("empty response (blocked or no candidates)", usage)
	}
	return sb.String(), usage, nil
}

// Run answers the prompt and then asks a judge model to score the answer.
func (g *Gemini) Run(ctx context.Context, req Request) (Response, error) {
	step := func(icon, f string, a ...any) {
		if req.Step != nil {
			req.Step(icon, f, a...)
		}
	}
	maxOut := int64(2048)
	if req.RemainingTok > 0 && req.RemainingTok-1000 < maxOut {
		maxOut = req.RemainingTok - 1000
	}
	if maxOut < 64 {
		return Response{}, Budget("not enough token budget left to start an attempt", Response{})
	}
	if len(req.Tools) > 0 {
		step("ℹ️ ", "MCP tools %v are allow-listed; real tool calls are out of scope for this demo agent", req.Tools)
	}

	step("🧠", "calling %s", req.Model)
	text, usage, err := g.generate(ctx, req.Model, req.Prompt, map[string]any{"maxOutputTokens": maxOut})
	if err != nil {
		return usage, err
	}
	usage.Text = text
	step("✍️ ", "got %d output tokens", usage.OutputTokens)

	step("⚖️ ", "judging with %s", JudgeModel)
	judgePrompt := fmt.Sprintf(`You are a strict evaluator. Score from 0 to 1 how correct, specific and useful the ANSWER is for the TASK.
Penalise invented commands, flags or facts heavily. Reply only with JSON like {"score": 0.42}.

TASK:
%s

ANSWER:
%s`, req.Prompt, text)
	raw, judgeUsage, err := g.generate(ctx, JudgeModel, judgePrompt, map[string]any{
		"responseMimeType": "application/json",
		"maxOutputTokens":  256,
	})
	// Judge tokens count against the run budget and are priced like the attempt's model (a slight overestimate).
	usage.InputTokens += judgeUsage.InputTokens
	usage.OutputTokens += judgeUsage.OutputTokens
	if err != nil {
		return usage, err
	}
	var score struct {
		Score float64 `json:"score"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &score); err != nil {
		return usage, Transient("judge returned non-JSON output", usage)
	}
	usage.Quality = clamp(score.Score)
	return usage, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
