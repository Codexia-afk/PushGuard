package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestProposalPromptSeparatesExactSourceAndTestEvidence(t *testing.T) {
	in := model.ContextBundle{EditableFiles: []string{"core.py"}, SourceFiles: []model.ContextFile{{File: "core.py", Editable: true, Content: "def sum(a, b):\n    return a - b\n"}, {File: "tests/test_sum.py", Content: "assert sum(2, 3) == 5\n"}}}
	prompt := proposalPrompt(in)
	if !strings.Contains(prompt, "def sum(a, b):\n    return a - b\n") || !strings.Contains(prompt, "READ-ONLY EVIDENCE; never edit") || strings.Contains(prompt, `"sourceFiles"`) {
		t.Fatal(prompt)
	}
}

func TestContextRequiredIsTypedAndCannotContainEditsOrCommands(t *testing.T) {
	for _, extra := range []string{"", `,"edits":[{"file":"a.py","oldText":"x","newText":"y"}]`, `,"command":"git push"`} {
		p := Ollama{Model: "test", Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content := `{"status":"CONTEXT_REQUIRED","contextRequired":{"files":["core.py"],"symbols":["subtotal"],"reason":"need implementation"}` + extra + `}`
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": content}})
		}))}
		_, err := p.ProposeFix(context.Background(), model.ContextBundle{})
		var needed *ContextRequiredError
		if extra == "" && (!errors.As(err, &needed) || needed.Request.Files[0] != "core.py") {
			t.Fatalf("missing typed request: %v", err)
		}
		if extra != "" && !IsResponseError(err) {
			t.Fatalf("unsafe request accepted: %v", err)
		}
	}
}
