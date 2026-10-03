package diagnostics

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestNoisyToolEvidenceIsBounded(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&log, "source.go:%d:1: error: %s\n", i+1, strings.Repeat("x", 200))
	}
	result := model.CommandResult{ExitCode: 1, Stdout: log.String()}
	ds := Parse(model.Check{Name: "compile", Args: []string{"go"}}, result)
	if len(ds) != maxDiagnostics {
		t.Fatalf("diagnostic count = %d", len(ds))
	}
	for _, d := range ds {
		if len(d.RawOutput) > 600 || len(d.Message) > 2100 {
			t.Fatal("unbounded normalized evidence")
		}
	}
	if len(result.Stdout) != log.Len() {
		t.Fatal("original tool evidence was changed")
	}
}
