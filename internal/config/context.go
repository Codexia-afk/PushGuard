package config

import "fmt"

// MaxInputTokens includes instructions, normalized evidence and response schema.
// The runtime window additionally reserves output and a separate safety margin.
type ContextBudget struct {
	MaxInputTokens       int `json:"max_input_tokens,omitempty"`
	ReservedOutputTokens int `json:"reserved_output_tokens,omitempty"`
	SafetyMarginTokens   int `json:"safety_margin_tokens,omitempty"`
}

func (b ContextBudget) Effective(output int) ContextBudget {
	if b.MaxInputTokens == 0 {
		b.MaxInputTokens = 6000
	}
	if b.ReservedOutputTokens == 0 {
		b.ReservedOutputTokens = output
	}
	if b.ReservedOutputTokens == 0 {
		b.ReservedOutputTokens = 2048
	}
	if b.SafetyMarginTokens == 0 {
		b.SafetyMarginTokens = 512
	}
	return b
}
func (b ContextBudget) Validate(output int) error {
	if output != 0 && b.ReservedOutputTokens != 0 && output != b.ReservedOutputTokens {
		return fmt.Errorf("ai.maxOutputTokens and ai.context.reserved_output_tokens must agree when both are set")
	}
	b = b.Effective(output)
	if b.MaxInputTokens < 1500 || b.MaxInputTokens > 64000 || b.ReservedOutputTokens < 256 || b.ReservedOutputTokens > 16384 || b.SafetyMarginTokens < 128 || b.SafetyMarginTokens > 8192 {
		return fmt.Errorf("AI context budget requires input 1500..64000, output 256..16384, safety margin 128..8192")
	}
	return nil
}
func (c RepairConfig) GroupLimit() int {
	if c.MaxDiagnosticsPerGroup == 0 {
		return 10
	}
	return c.MaxDiagnosticsPerGroup
}
