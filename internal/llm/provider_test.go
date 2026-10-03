package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestRemotePrivacyAndRedirects(t *testing.T) {
	for _, url := range []string{"http://example.com", "https://example.com", "http://127.0.0.1.evil.test", "http://user:password@127.0.0.1", "file:///etc/passwd"} {
		if _, err := ValidateEndpoint(url, false); err == nil {
			t.Fatalf("accepted unapproved endpoint %s", url)
		}
	}
	if local, err := ValidateEndpoint("https://example.com", true); err != nil || local {
		t.Fatal("explicit remote TLS endpoint refused")
	}
	if _, err := ValidateEndpoint("http://example.com", true); err == nil {
		t.Fatal("remote plaintext allowed")
	}
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.com", http.StatusTemporaryRedirect)
	}))

	_, err := (Ollama{Endpoint: "http://127.0.0.1:11434", Client: client, Model: "test"}).Analyze(context.Background(), model.ContextBundle{})
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("redirect escaped local boundary: %v", err)
	}
}
func TestOllamaAndCompatibleStructuredResponses(t *testing.T) {
	for _, compatible := range []bool{false, true} {
		t.Run(map[bool]string{false: "ollama", true: "compatible"}[compatible], func(t *testing.T) {
			client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				want := "/api/chat"
				if compatible {
					want = "/v1/chat/completions"
				}
				if r.URL.Path != want || body["model"] != "test" {
					t.Errorf("unexpected request %s %+v", r.URL.Path, body)
				}
				content := `{"summary":"tool evidence","rootCause":"implementation mismatch"}`
				if compatible {
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
				} else {
					json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}})
				}
			}))

			result, err := (Ollama{Endpoint: "http://127.0.0.1:11434", Client: client, Model: "test", Compatible: compatible}).Analyze(context.Background(), model.ContextBundle{})
			if err != nil || result.RootCause == "" {
				t.Fatalf("invalid result %+v %v", result, err)
			}
		})
	}
}
func TestMalformedResponseIsNotARepair(t *testing.T) {
	client := handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"message":{"content":"{\"patch\":\"\"}"}}`))
	}))

	if _, err := (Ollama{Endpoint: "http://127.0.0.1:11434", Client: client, Model: "test"}).ProposeFix(context.Background(), model.ContextBundle{}); err == nil {
		t.Fatal("empty repair accepted")
	}
}

func TestOllamaSchemaConstrainsProposalPaths(t *testing.T) {
	schema := responseSchema(&model.RepairProposal{}, []string{"src/implementation.ts"})
	data, err := json.Marshal(schema)
	if err != nil || !strings.Contains(string(data), `"enum":["src/implementation.ts"]`) || !strings.Contains(string(data), `"oldText"`) || strings.Contains(string(data), `"command"`) {
		t.Fatalf("unsafe proposal schema: %s %v", data, err)
	}
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, r)
	response := recorder.Result()
	response.Request = r
	return response, nil
}
func handlerClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: handlerTransport{handler}}
}
