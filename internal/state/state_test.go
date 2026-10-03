package state

import (
	"github.com/pushguard/pushguard/internal/approval"
	"testing"
)

func TestMachineRejectsPushFromFailure(t *testing.T) {
	m := New()
	if err := m.Move(Pushing); err == nil {
		t.Fatal("expected illegal transition")
	}
	if m.Current != Initializing {
		t.Fatalf("state changed after rejected transition: %s", m.Current)
	}
}

func TestMachineAllowsApprovedPath(t *testing.T) {
	m := New()
	for _, next := range []State{Discovering, Verifying, FullVerifying, GitPreflight, ResourcePreflight, StateCheck, Reviewing, WaitingForPush, Pushing, Complete} {
		if next == Pushing {
			m.Authorize(approval.DecisionAllow)
		}
		if err := m.Move(next); err != nil {
			t.Fatalf("%s: %v", next, err)
		}
	}
}

func TestPrivilegedTransitionsRequireFreshAllow(t *testing.T) {
	for from, to := range map[State]State{WaitingForChecks: Verifying, WaitingForRepair: Analyzing, WaitingForPatch: Snapshotting, WaitingForPush: Pushing, WaitingForCommit: Committing} {
		m := &Machine{Current: from}
		for _, d := range []approval.Decision{approval.DecisionUnknown, approval.DecisionDeny} {
			m.Authorize(d)
			if err := m.Move(to); err == nil {
				t.Fatalf("%s bypassed approval", from)
			}
		}
		m.Authorize(approval.DecisionAllow)
		if err := m.Move(to); err != nil {
			t.Fatal(err)
		}
		m.Current = from
		if err := m.Move(to); err == nil {
			t.Fatal("approval reused")
		}
	}
	for _, from := range []State{FailureDetected, GeneratingPatch, ValidatingPatch, Diagnosing} {
		m := &Machine{Current: from}
		m.Authorize(approval.DecisionAllow)
		if m.Move(Pushing) == nil || m.Move(Applying) == nil {
			t.Fatalf("illegal shortcut from %s", from)
		}
	}
}

func TestCheckCanNeverEnterPushStates(t *testing.T) {
	for _, next := range []State{WaitingForPush, Pushing, WaitingForCommit, Committing} {
		m := &Machine{Current: WaitingForPush, CheckOnly: true}
		m.Authorize(approval.DecisionAllow)
		if m.Move(next) == nil {
			t.Fatal("check entered push state")
		}
	}
}
