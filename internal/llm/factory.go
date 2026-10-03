package llm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/config"
)

// RepairProvider extends the existing proposal-only interface without breaking
// injected providers. Availability never grants investigation or apply authority.
type RepairProvider interface {
	Provider
	Name() string
	Available(context.Context) error
}

func NewRepairProvider(ai config.AIConfig, privacy config.PrivacyConfig) (RepairProvider, error) {
	if !ai.IsEnabled() {
		return Unavailable{Reason: "AI repair is disabled in the selected configuration (ai.provider=none or ai.enabled=false). Set ai.provider=ollama and ai.model to an installed model. Repository configuration overrides user-wide defaults."}, nil
	}
	switch ai.Provider {
	case "ollama", "openai-compatible":
	default:
		return nil, fmt.Errorf("unsupported AI provider %q", ai.Provider)
	}
	if strings.TrimSpace(ai.Model) == "" {
		return nil, fmt.Errorf("ai.model is empty; select an installed model with ollama list or the provider's model catalog")
	}
	endpoint := strings.TrimRight(ai.Endpoint, "/")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434"
	}
	remote := privacy.AllowRemoteAI && !ai.LocalFirst
	if _, err := ValidateEndpoint(endpoint, remote); err != nil {
		return nil, err
	}
	if ai.Provider == "ollama" && !remote && (strings.Contains(ai.Model, ":cloud") || strings.HasSuffix(ai.Model, "-cloud")) {
		return nil, fmt.Errorf("cloud-routed Ollama model refused in local-only mode; select an installed local model")
	}
	return Ollama{Endpoint: endpoint, Model: ai.Model, AllowRemote: remote, Compatible: ai.Provider == "openai-compatible", APIKeyEnv: ai.APIKeyEnv, MaxOutputTokens: ai.MaxOutputTokens, ReasoningEffort: ai.ReasoningEffort, ContextBudget: ai.Context, RequestTimeout: time.Duration(ai.RequestTimeoutSeconds) * time.Second}, nil
}

// OllamaProvider names the real native adapter without creating a second AI
// architecture; Ollama remains source-compatible with existing adapters/tests.
type OllamaProvider = Ollama

func (u Unavailable) Name() string                    { return "disabled" }
func (u Unavailable) Available(context.Context) error { return fmt.Errorf("%s", u.Reason) }
func (o Ollama) Name() string {
	if o.Compatible {
		return "OpenAI-compatible"
	}
	return "Ollama"
}
func (o Ollama) Available(ctx context.Context) error { return o.Probe(ctx) }
