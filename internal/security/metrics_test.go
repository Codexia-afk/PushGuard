package security

import (
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestRepairMetricsRedactCommandsWithoutMutatingEvidence(t *testing.T) {
	in := model.SessionReport{RepairMetrics: model.RepairMetrics{VerificationCommands: []string{"verify --token secret-value"}}}
	out := SanitizeReport(in)
	if strings.Contains(out.RepairMetrics.VerificationCommands[0], "secret-value") || !strings.Contains(in.RepairMetrics.VerificationCommands[0], "secret-value") {
		t.Fatal("metric command sanitation failed or changed input")
	}
	message := "Ollama observed token counts (input/output): 3109/868"
	if Redact(message) != message {
		t.Fatal("numeric inference telemetry was hidden")
	}
}
