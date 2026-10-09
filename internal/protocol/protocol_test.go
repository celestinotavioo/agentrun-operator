package protocol

import (
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Result{Outcome: OutcomeOK, Model: "gemini-flash-latest", InputTokens: 10, OutputTokens: 5, CostMicroUSD: 42, Quality: 0.8, Answer: "ok"}
	out, err := Decode(string(in.Encode()))
	if err != nil || out != in {
		t.Fatalf("round trip = %+v, %v", out, err)
	}
	if out.Tokens() != 15 {
		t.Fatalf("Tokens() = %d", out.Tokens())
	}
}

func TestEncodeFitsTerminationMessage(t *testing.T) {
	r := Result{Outcome: OutcomeOK, Model: "m", Answer: strings.Repeat("ação \"quoted\" ", 2000)}
	b := r.Encode()
	if len(b) > 4096 {
		t.Fatalf("encoded %d bytes, termination messages are capped at 4096", len(b))
	}
	if _, err := Decode(string(b)); err != nil {
		t.Fatalf("truncated payload must stay valid JSON: %v", err)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "panic: boom", `{"m":"x"}`} {
		if _, err := Decode(s); err == nil {
			t.Errorf("Decode(%q) should fail", s)
		}
	}
}
