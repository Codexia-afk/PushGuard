package config

import "testing"

func TestContextBudgetJSONAndYAML(t *testing.T) {
	for name, data := range map[string]string{
		"config.json": `{"ai":{"context":{"max_input_tokens":5000,"reserved_output_tokens":1000,"safety_margin_tokens":300},"requestTimeoutSeconds":300},"repair":{"maxDiagnosticsPerGroup":8}}`,
		"config.yaml": "ai:\n  context:\n    max_input_tokens: 5000\n    reserved_output_tokens: 1000\n    safety_margin_tokens: 300\n  requestTimeoutSeconds: 300\nrepair:\n  maxDiagnosticsPerGroup: 8\n",
	} {
		cfg, err := Decode([]byte(data), name)
		if err != nil || cfg.AI.Context.MaxInputTokens != 5000 || cfg.AI.Context.ReservedOutputTokens != 1000 || cfg.AI.RequestTimeoutSeconds != 300 || cfg.Repair.GroupLimit() != 8 {
			t.Fatalf("%s: %+v %v", name, cfg, err)
		}
	}
	for _, data := range []string{`{"ai":{"context":{"max_input_tokens":-1}}}`, `{"ai":{"maxOutputTokens":2048,"context":{"reserved_output_tokens":1000}}}`, `{"repair":{"maxDiagnosticsPerGroup":51}}`, `{"ai":{"requestTimeoutSeconds":1}}`} {
		if _, err := Decode([]byte(data), "config.json"); err == nil {
			t.Fatalf("invalid budget accepted: %s", data)
		}
	}
}
