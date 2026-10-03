package model

import (
	"encoding/json"
	"testing"
)

func TestProviderConfidenceVariantsRemainMetadata(t *testing.T) {
	for _, raw := range []string{`"high"`, `0.95`, `95`, `null`} {
		var a Analysis
		if err := json.Unmarshal([]byte(`{"summary":"evidence","rootCause":"hypothesis","confidence":`+raw+`}`), &a); err != nil || a.Summary != "evidence" || a.RootCause != "hypothesis" {
			t.Fatalf("%s: %+v %v", raw, a, err)
		}
		var p RepairProposal
		if err := json.Unmarshal([]byte(`{"patch":"unvalidated","confidence":`+raw+`}`), &p); err != nil || p.Patch != "unvalidated" {
			t.Fatalf("%s: %+v %v", raw, p, err)
		}
	}
	for _, raw := range []string{`true`, `[]`, `{}`, `-1`, `101`} {
		var p RepairProposal
		if err := json.Unmarshal([]byte(`{"confidence":`+raw+`}`), &p); err == nil {
			t.Fatalf("accepted malformed confidence %s", raw)
		}
	}
}
