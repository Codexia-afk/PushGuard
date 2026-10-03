package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestCompatibleGenerationProbeExplicitlyRequestsJSON(t *testing.T) {
	p := Ollama{Model: "test-model", Compatible: true, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		joined := ""
		for _, m := range request.Messages {
			joined += m.Content
		}
		if !strings.Contains(strings.ToLower(joined), "json") {
			t.Error("JSON mode request lacks explicit JSON instruction")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"summary\":\"probe\",\"rootCause\":\"not applicable\"}"}}]}`))
	}))}
	if err := p.TestGeneration(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCombinedRepairUsesOneBudgetedRequest(t *testing.T) {
	calls := 0
	p := Ollama{Model: "budget-model", Compatible: true, MaxOutputTokens: 2048, ReasoningEffort: "low", Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["max_completion_tokens"] != float64(2048) || body["reasoning_effort"] != "low" {
			t.Errorf("missing budget: %+v", body)
		}
		content := `{"summary":"Proposed lint repair","rootCause":"no-var","edits":[{"file":"src/a.ts","oldText":"var a","newText":"const a"}]}`
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]string{"content": content}}}})
	}))}
	a, proposal, err := p.InvestigateAndPropose(context.Background(), model.ContextBundle{})
	if err != nil || calls != 1 || a.RootCause != "no-var" || len(proposal.Edits) != 1 {
		t.Fatalf("combined repair: calls=%d err=%v", calls, err)
	}
}

func TestTruncatedProviderResponseCannotBecomeProposal(t *testing.T) {
	p := Ollama{Model: "test", Compatible: true, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"{\"summary\":\"partial\",\"rootCause\":\"unknown\"}"}}]}`))
	}))}
	if _, err := p.Analyze(context.Background(), model.ContextBundle{}); err == nil || !strings.Contains(err.Error(), "output-token limit") {
		t.Fatalf("truncated response accepted: %v", err)
	}
}

func TestProviderErrorDoesNotLeakCredential(t *testing.T) {
	key := "gsk_" + strings.Repeat("test", 8)
	t.Setenv("TEST_PROVIDER_CREDENTIAL", key)
	p := Ollama{Model: "test", APIKeyEnv: "TEST_PROVIDER_CREDENTIAL", Compatible: true, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Invalid key " + key}})
	}))}
	err := p.TestGeneration(context.Background())
	if err == nil || strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("unsafe error: %v", err)
	}
}
