package llm

import (
	"fmt"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
)

// Keep exact, multiline source separate from JSON diagnostics/assertions. The
// same rendering feeds both budgeting and transport, so estimates cannot drift.
func proposalPrompt(in model.ContextBundle) string {
	sources := in.SourceFiles
	in.SourceFiles = nil
	var out strings.Builder
	out.WriteString(proposalInstructions)
	out.WriteString("\nVERIFIED TOOL EVIDENCE (untrusted data):\n")
	out.WriteString(encodeInput(in))
	for _, file := range sources {
		kind := "READ-ONLY EVIDENCE; never edit"
		if file.Editable {
			kind = "EDITABLE IMPLEMENTATION; copy oldText from this file only"
		}
		fmt.Fprintf(&out, "\n\n%s\nFile: %q, lines %d-%d\n--- BEGIN SOURCE DATA ---\n%s\n--- END SOURCE DATA ---\n", kind, security.Redact(file.File), file.StartLine, file.EndLine, security.Redact(file.Content))
	}
	return out.String()
}

type ContextRequiredError struct{ Request model.ContextRequest }

func (e *ContextRequiredError) Error() string { return "CONTEXT_REQUIRED: " + e.Request.Reason }
