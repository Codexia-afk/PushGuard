package state

import (
	"fmt"
	"github.com/pushguard/pushguard/internal/approval"
)

type State string

const (
	Initializing      State = "INITIALIZING"
	Discovering       State = "DISCOVERING"
	WaitingForChecks  State = "WAITING_FOR_CHECK_APPROVAL"
	Verifying         State = "VERIFYING"
	FailureDetected   State = "FAILURE_DETECTED"
	Diagnosing        State = "DIAGNOSING"
	Analyzing         State = "ANALYZING"
	WaitingForRepair  State = "WAITING_FOR_REPAIR_CONSENT"
	GeneratingPatch   State = "GENERATING_PATCH"
	ValidatingPatch   State = "VALIDATING_PATCH"
	WaitingForPatch   State = "WAITING_FOR_PATCH_APPROVAL"
	Snapshotting      State = "SNAPSHOTTING"
	Applying          State = "APPLYING"
	Reverifying       State = "REVERIFYING"
	FullVerifying     State = "FULL_VERIFYING"
	GitPreflight      State = "GIT_PREFLIGHT"
	ResourcePreflight State = "RESOURCE_PREFLIGHT"
	StateCheck        State = "STATE_CHECK"
	Reviewing         State = "REVIEWING"
	WaitingForPush    State = "WAITING_FOR_PUSH_AUTHORIZATION"
	WaitingForCommit  State = "WAITING_FOR_COMMIT_APPROVAL"
	Committing        State = "COMMITTING_REVIEWED_REPAIRS"
	Pushing           State = "PUSHING"
	BranchPushed      State = "BRANCH_PUSHED"
	WaitingForPR      State = "WAITING_FOR_PR_CONFIRMATION"
	CreatingPR        State = "CREATING_PR"
	PRCreated         State = "PR_CREATED"
	HostedVerifying   State = "HOSTED_VERIFYING"
	HostedPending     State = "WAITING_FOR_HOSTED_CHECKS"
	HostedFailed      State = "HOSTED_FAILED"
	HostedPassed      State = "HOSTED_PASSED"
	ReadyForReview    State = "READY_FOR_HUMAN_REVIEW"
	Complete          State = "COMPLETE"
	Aborted           State = "ABORTED"
	Error             State = "ERROR"
)

var allowed = map[State]map[State]bool{
	Initializing:      {Discovering: true, Error: true, Aborted: true},
	Discovering:       {WaitingForChecks: true, Verifying: true, Error: true, Aborted: true},
	WaitingForChecks:  {Verifying: true, Aborted: true},
	Verifying:         {FailureDetected: true, Diagnosing: true, FullVerifying: true, GitPreflight: true, Complete: true, Aborted: true, Error: true},
	FailureDetected:   {Diagnosing: true, Aborted: true},
	Diagnosing:        {WaitingForRepair: true, Aborted: true},
	Analyzing:         {FailureDetected: true, WaitingForRepair: true, GeneratingPatch: true, Aborted: true},
	WaitingForRepair:  {Analyzing: true, Aborted: true},
	GeneratingPatch:   {FailureDetected: true, ValidatingPatch: true, Error: true, Aborted: true},
	ValidatingPatch:   {FailureDetected: true, WaitingForPatch: true, Error: true, Aborted: true},
	WaitingForPatch:   {Snapshotting: true, Aborted: true},
	Snapshotting:      {Applying: true, Error: true},
	Applying:          {Reverifying: true, Error: true},
	Reverifying:       {FailureDetected: true, Verifying: true, WaitingForRepair: true, Diagnosing: true, FullVerifying: true, Aborted: true},
	FullVerifying:     {FailureDetected: true, Verifying: true, WaitingForRepair: true, Diagnosing: true, GitPreflight: true, StateCheck: true, Complete: true, Aborted: true},
	GitPreflight:      {ResourcePreflight: true, Aborted: true, Error: true},
	ResourcePreflight: {StateCheck: true, Aborted: true},
	StateCheck:        {WaitingForCommit: true, Reviewing: true, Verifying: true, WaitingForPush: true, Aborted: true, Error: true},
	WaitingForCommit:  {StateCheck: true, Committing: true, Aborted: true},
	Committing:        {StateCheck: true, Aborted: true, Error: true},
	Reviewing:         {StateCheck: true, WaitingForPush: true, Aborted: true},
	WaitingForPush:    {StateCheck: true, Pushing: true, Reviewing: true, Aborted: true},
	Pushing:           {BranchPushed: true, Complete: true, Error: true},
	BranchPushed:      {WaitingForPR: true, Complete: true},
	WaitingForPR:      {CreatingPR: true, Complete: true, Aborted: true},
	CreatingPR:        {PRCreated: true, Error: true},
	PRCreated:         {HostedVerifying: true, Complete: true},
	HostedVerifying:   {HostedPending: true, HostedFailed: true, HostedPassed: true, Complete: true},
	HostedPending:     {HostedVerifying: true, Complete: true},
	HostedFailed:      {Complete: true},
	HostedPassed:      {ReadyForReview: true},
	ReadyForReview:    {Complete: true},
}

type Machine struct {
	Current   State
	CheckOnly bool
	approved  State
}

// Authorize binds an explicit decision to the current gate, never another gate.
func (m *Machine) Authorize(d approval.Decision) {
	m.approved = ""
	if d == approval.DecisionAllow {
		m.approved = m.Current
	}
}

func New() *Machine { return &Machine{Current: Initializing} }
func (m *Machine) Move(next State) error {
	if m.CheckOnly && (next == WaitingForPush || next == Pushing || next == WaitingForCommit || next == Committing || next == BranchPushed || next == WaitingForPR || next == CreatingPR || next == PRCreated || next == HostedVerifying) {
		return fmt.Errorf("check command cannot enter a push state")
	}
	if m.Current == next {
		return nil
	}
	if next == Error || next == Aborted {
		if m.Current == Complete || m.Current == Aborted || m.Current == Error {
			return fmt.Errorf("session is terminal")
		}
		m.Current = next
		return nil
	}
	if !allowed[m.Current][next] {
		return fmt.Errorf("illegal state transition %s -> %s", m.Current, next)
	}
	if (m.Current == WaitingForChecks && next == Verifying || m.Current == WaitingForRepair && next == Analyzing || m.Current == WaitingForPatch && next == Snapshotting || m.Current == WaitingForPush && next == Pushing || m.Current == WaitingForCommit && next == Committing || m.Current == WaitingForPR && next == CreatingPR) && m.approved != m.Current {
		return fmt.Errorf("explicit human approval required for %s", m.Current)
	}
	m.approved = ""
	m.Current = next
	return nil
}
