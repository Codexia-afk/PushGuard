package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
)

func TestOversizedContextNeverReachesTransport(t *testing.T) {
	calls := 0
	p := Ollama{Model: "mock", ContextBudget: config.ContextBudget{MaxInputTokens: 1500}, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))}
	in := model.ContextBundle{Diagnostics: []model.Diagnostic{{ID: "D-known", Message: strings.Repeat("irreducible diagnostic ", 3000)}}}
	_, err := p.ProposeFix(context.Background(), in)
	var budget *ContextBudgetError
	if !errors.As(err, &budget) || calls != 0 {
		t.Fatalf("budget was advisory: calls=%d error=%v", calls, err)
	}
}

func TestContextReductionPreservesSelectedLocationsAndCaller(t *testing.T) {
	p := Ollama{Model: "mock", ContextBudget: config.ContextBudget{MaxInputTokens: 5000}}
	ds := []model.Diagnostic{{ID: "D-first", Location: model.SourceLocation{File: "a.go", Line: 20}}, {ID: "D-last", Location: model.SourceLocation{File: "a.go", Line: 170}}}
	var source strings.Builder
	for i := 0; i < 200; i++ {
		source.WriteString("// bounded source with sufficiently long comments for context estimation\n")
	}
	in := model.ContextBundle{Diagnostics: ds, Diagnostic: ds[0], EditableFiles: []string{"a.go"}, SourceFiles: []model.ContextFile{{File: "a.go", Content: source.String(), StartLine: 1, EndLine: 200, Editable: true}}, Diff: strings.Repeat("large unrelated diff", 1000), Metadata: map[string]string{"changedFiles": strings.Repeat("file ", 1000)}}
	prepared, usage, err := p.PrepareContext(in)
	if err != nil || !usage.ContextReduced || usage.EstimatedInputTokens > 5000 {
		t.Fatalf("context not bounded: %+v %v", usage, err)
	}
	if in.SourceFiles[0].Content != source.String() || in.Metadata["changedFiles"] == "" || len(in.Diagnostics) != 2 {
		t.Fatal("caller evidence mutated")
	}
	for _, d := range prepared.Diagnostics {
		found := false
		for _, f := range prepared.SourceFiles {
			found = found || f.File == d.Location.File && f.StartLine <= d.Location.Line && f.EndLine >= d.Location.Line
		}
		if !found {
			t.Fatalf("trimmed away selected diagnostic %s", d.ID)
		}
	}
}

func TestNativeBudgetMetricsAndStrictProposal(t *testing.T) {
	for _, tc := range []struct {
		name, extra, ids, reason string
		valid                    bool
	}{
		{"valid", "", `["D-known"]`, "stop", true},
		{"unknown ID", "", `["D-invented"]`, "stop", false},
		{"repeated ID", "", `["D-known","D-known"]`, "stop", false},
		{"command injection", `,"command":"touch unsafe"`, `["D-known"]`, "stop", false},
		{"truncated", "", `["D-known"]`, "length", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := Ollama{Model: "mock", ContextBudget: config.ContextBudget{MaxInputTokens: 5000, ReservedOutputTokens: 1000, SafetyMarginTokens: 300}, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct {
					Options map[string]int `json:"options"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Options["num_ctx"] != 6300 || body.Options["num_predict"] != 1000 || r.Header.Get("Authorization") != "" {
					t.Errorf("incorrect local request: %+v", body)
				}
				content := `{"summary":"source repair","rootCause":"incorrect value","edits":[{"file":"a.go","oldText":"return 0","newText":"return 1"}],"diagnosticsAddressed":` + tc.ids + tc.extra + `}`
				_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}, "done_reason": tc.reason, "prompt_eval_count": 600, "eval_count": 100, "load_duration": int64(time.Second)})
			}))}
			t.Setenv("PUSHGUARD_AI_API_KEY", "must-not-leak-to-local")
			proposal, err := p.ProposeFix(context.Background(), model.ContextBundle{Diagnostics: []model.Diagnostic{{ID: "D-known"}}, EditableFiles: []string{"a.go"}})
			if (err == nil) != tc.valid || calls != 1 {
				t.Fatalf("proposal validation: calls=%d err=%v", calls, err)
			}
			if tc.valid && (proposal.Inference.InputTokens != 600 || proposal.Inference.OutputTokens != 100 || proposal.Inference.LoadDuration != time.Second) {
				t.Fatalf("missing real metrics: %+v", proposal.Inference)
			}
		})
	}
}

func TestLocalCatalogRejectsCloudRoutedAlias(t *testing.T) {
	p := Ollama{Model: "alias", Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"alias:latest","remote_host":"https://ollama.com","remote_model":"coder:cloud"}]}`))
	}))}
	if err := p.Available(context.Background()); err == nil || !strings.Contains(err.Error(), "cloud-routed") {
		t.Fatalf("cloud alias accepted in local mode: %v", err)
	}
	p.AllowRemote = true
	if status := p.Status(context.Background()); status.Mode != "REMOTE" || status.ModelStatus != model.StatusPass {
		t.Fatalf("explicit cloud alias mislabeled as local: %+v", status)
	}
}
