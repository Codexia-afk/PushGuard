package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/model"
)

func (a *App) recordCheckMetrics(result model.CheckResult) {
	m := &a.Report.RepairMetrics
	complete := !result.DiagnosticsLimited && !result.Command.Truncated
	found := false
	for i := range m.Checks {
		if m.Checks[i].Check == result.Check.Name {
			m.Checks[i].Current = len(result.Diagnostics)
			m.Checks[i].Complete = m.Checks[i].Complete && complete
			found = true
			break
		}
	}
	if !found {
		m.Checks = append(m.Checks, model.CheckProgress{Check: result.Check.Name, Initial: len(result.Diagnostics), Current: len(result.Diagnostics), Complete: complete})
	}
	command := result.Command.Command
	for _, previous := range m.VerificationCommands {
		if previous == command {
			return
		}
	}
	m.VerificationCommands = append(m.VerificationCommands, command)
}
func (a *App) repairMetricsSummary() {
	m := a.Report.RepairMetrics
	if m.Attempts == 0 {
		return
	}
	initial, current := 0, 0
	for _, c := range m.Checks {
		initial += c.Initial
		current += c.Current
	}
	a.UI.Title("Local repair execution summary")
	a.UI.Say(fmt.Sprintf("Provider: %s / %s\nInitial diagnostics (first observed per check): %d\nCurrent diagnostics (last observed per check): %d\nInvestigation attempts: %d\nAI inference requests: %d\nApproved patches applied: %d\nRejected proposals: %d\nNew diagnostics introduced during rechecks: %d\nFiles modified: %d\nRepair duration: %s", a.Cfg.AI.Provider, a.Cfg.AI.Model, initial, current, m.Attempts, m.AIRequests, m.Applied, m.Rejected, m.NewDiagnostics, len(m.FilesModified), m.Duration.Round(time.Millisecond)))
	a.UI.Say("Counts describe captured tool diagnostics, not proven bug counts. Full verification and push authorization remain separate.")
	if len(m.FilesModified) > 0 {
		a.UI.Say("AI-modified files: " + strings.Join(m.FilesModified, ", "))
	}
}
