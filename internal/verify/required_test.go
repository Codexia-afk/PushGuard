package verify

import (
	"github.com/pushguard/pushguard/internal/model"
	"testing"
)

func TestRequiredChecksNeedPassEvidence(t *testing.T) {
	for _, status := range []model.ResultStatus{model.StatusWarning, model.StatusSkipped, model.StatusUnknown, model.StatusBlocked, model.StatusFail} {
		r := model.CheckResult{Check: model.Check{Name: "required", Required: true}, Status: status}
		if len(Blocking([]model.CheckResult{r})) != 1 {
			t.Errorf("required %s escaped verification gate", status)
		}
	}
}
