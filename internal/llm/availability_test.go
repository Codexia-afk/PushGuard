package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
)

func TestAvailabilityRequiresValidCatalogAndExactModel(t *testing.T) {
	for _, tc := range []struct {
		name, body, model string
		status            int
		ok                bool
	}{
		{"installed", `{"models":[{"name":"code:4b"}]}`, "code:4b", 200, true},
		{"default tag", `{"models":[{"name":"code:latest"}]}`, "code", 200, true},
		{"wrong tag", `{"models":[{"name":"code:4b"}]}`, "code", 200, false},
		{"empty catalog", `{"models":[]}`, "code", 200, false},
		{"invalid json", `not json`, "code", 200, false},
		{"wrong object", `{}`, "code", 200, false},
		{"null catalog", `{"models":null}`, "code", 200, false},
		{"unavailable", ``, "code", 503, false},
		{"no model", `{"models":[]}`, "", 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Ollama{Endpoint: "http://127.0.0.1:11434", Model: tc.model, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" || r.Method != "GET" {
					t.Errorf("wrong probe: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))}
			s := p.Status(context.Background())
			if (s.Problem == "") != tc.ok || s.Generation != model.StatusUnknown {
				t.Fatalf("false readiness: %+v", s)
			}
		})
	}
}

func TestCompatibleProbeUsesV1AndConfiguredCredential(t *testing.T) {
	t.Setenv("TEST_AI_KEY", "test-credential")
	for _, endpoint := range []string{"https://provider.example", "https://provider.example/v1"} {
		p := Ollama{Endpoint: endpoint, Model: "model", Compatible: true, AllowRemote: true, APIKeyEnv: "TEST_AI_KEY", Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-credential" {
				t.Errorf("wrong request: %s %s", r.URL.Path, r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"data":[{"id":"model"}]}`))
		}))}
		if err := p.Available(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProviderFactoryPreservesExplicitDisableAndRejectsInvalidConfiguration(t *testing.T) {
	cfg := config.Default()
	p, err := NewRepairProvider(cfg.AI, cfg.Privacy)
	if err != nil || p.Name() != "disabled" || p.Available(context.Background()) == nil {
		t.Fatal("disabled provider became available")
	}
	cfg.AI.Provider = "ollama"
	if _, err := NewRepairProvider(cfg.AI, cfg.Privacy); err == nil {
		t.Fatal("empty model accepted")
	}
	cfg.AI.Model = "any-user-model"
	p, err = NewRepairProvider(cfg.AI, cfg.Privacy)
	if err != nil || p.Name() != "Ollama" {
		t.Fatalf("valid provider: %v", err)
	}
	cfg.AI.Provider = "unsupported"
	if _, err := NewRepairProvider(cfg.AI, cfg.Privacy); err == nil {
		t.Fatal("unsupported provider accepted")
	}
	cfg.AI.Provider, cfg.AI.Endpoint = "ollama", "https://remote.example"
	if _, err := NewRepairProvider(cfg.AI, cfg.Privacy); err == nil {
		t.Fatal("cloud privacy bypass")
	}
}

func TestContextFreeGenerationMustParse(t *testing.T) {
	for _, response := range []string{`{"message":{"content":"{}"}}`, `{"message":{"content":"not JSON"}}`} {
		p := Ollama{Model: "code", Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))}
		if err := p.TestGeneration(context.Background()); err == nil {
			t.Fatal("invalid response considered ready")
		}
	}
	if strings.Contains(RepairPolicy, "may push") {
		t.Fatal("unsafe provider policy")
	}
}

func TestSchemaAnchorsAreExactBoundedAndRedacted(t *testing.T) {
	files := []model.ContextFile{
		{File: "src/cart.ts", Editable: true, Content: "export function add(a, b) {\n  return a - b;\n}\n"},
		{File: "src/client.ts", Editable: true, Content: "const API_KEY = 'private-value';\n"},
		{File: "tests/test.ts", Editable: false, Content: "do not offer this test\n"},
	}
	anchors := sourceAnchors(files)
	joined := strings.Join(anchors, "\n")
	if !strings.Contains(joined, "  return a - b;") || strings.Contains(joined, "private-value") || strings.Contains(joined, "do not offer") {
		t.Fatal(anchors)
	}
	for _, a := range anchors {
		if a == "export function add(a, b) {" || a == "}" {
			t.Fatal("incomplete structural anchor offered")
		}
	}
}
