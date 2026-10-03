package app

import (
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/security"
	"github.com/pushguard/pushguard/internal/state"
	"time"
)

func (a *App) event(name, detail string) {
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	a.Report.Events = append(a.Report.Events, model.Event{Name: name, At: time.Now().UTC(), Detail: security.Redact(detail)})
}
func (a *App) stateEvent(s state.State) {
	name := map[state.State]string{state.Verifying: "verification.started", state.FailureDetected: "verification.failed", state.WaitingForPatch: "repair.proposed", state.Reverifying: "repair.applied", state.HostedVerifying: "hosted_check.started", state.HostedFailed: "hosted_check.failed", state.HostedPassed: "hosted_check.completed"}[s]
	if name != "" {
		a.event(name, "")
	}
}
