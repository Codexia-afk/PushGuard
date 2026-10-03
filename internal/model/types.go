package model

import (
	"bytes"
	"encoding/json"
	"time"
)

type ResultStatus string

const (
	StatusPass    ResultStatus = "PASS"
	StatusFail    ResultStatus = "FAIL"
	StatusWarning ResultStatus = "WARNING"
	StatusBlocked ResultStatus = "BLOCKED"
	StatusUnknown ResultStatus = "UNKNOWN"
	StatusSkipped ResultStatus = "SKIPPED"
)

type Check struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args"`
	Required    bool     `json:"required"`
	TimeoutSecs int      `json:"timeoutSeconds,omitempty"`
	// WorkingDir is relative to the repository root. Empty means the root.
	WorkingDir string `json:"workingDir,omitempty"`
	// Language and Project describe the boundary a discovered check belongs to.
	Language string `json:"language,omitempty"`
	Project  string `json:"project,omitempty"`
	// Input is passed on stdin, e.g. an explicit file list for a syntax checker.
	Input string `json:"input,omitempty"`
	// Description is a human summary shown instead of very long commands.
	Description string `json:"description,omitempty"`
	// Unavailable records why a discovered check cannot run on this machine.
	// A required unavailable check blocks verification; it is never a PASS.
	Unavailable string `json:"unavailable,omitempty"`
	// Discovered marks checks PushGuard planned itself rather than configured ones.
	Discovered bool `json:"discovered,omitempty"`
}

// Failure classes decide whether source repair is an appropriate response.
const (
	ClassCode          = "CODE"
	ClassTest          = "TEST"
	ClassGit           = "GIT"
	ClassResource      = "RESOURCE"
	ClassEnvironment   = "ENVIRONMENT"
	ClassConfiguration = "CONFIGURATION"
	ClassNetwork       = "NETWORK"
	ClassSecurity      = "SECURITY"
	ClassUnknown       = "UNKNOWN"
)

type CommandResult struct {
	Args       []string      `json:"args"`
	Command    string        `json:"command"`
	WorkingDir string        `json:"workingDir"`
	ExitCode   int           `json:"exitCode"`
	Stdout     string        `json:"stdout,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	Duration   time.Duration `json:"duration"`
	TimedOut   bool          `json:"timedOut,omitempty"`
	Terminated string        `json:"terminated,omitempty"`
	Truncated  bool          `json:"truncated,omitempty"`
	Started    time.Time     `json:"started"`
	Finished   time.Time     `json:"finished"`
}

type SourceLocation struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Exact  bool   `json:"exact"`
}

type StackFrame struct {
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	Function string `json:"function,omitempty"`
}

type Diagnostic struct {
	ID             string         `json:"id,omitempty"`
	Check          string         `json:"check,omitempty"`
	Tool           string         `json:"tool"`
	Category       string         `json:"category"`
	Severity       string         `json:"severity"`
	Location       SourceLocation `json:"location"`
	Code           string         `json:"code,omitempty"`
	Rule           string         `json:"rule,omitempty"`
	TestName       string         `json:"testName,omitempty"`
	Classification string         `json:"classification,omitempty"`
	Message        string         `json:"message"`
	Command        string         `json:"command,omitempty"`
	ExitCode       int            `json:"exitCode"`
	StackTrace     []StackFrame   `json:"stackTrace,omitempty"`
	RawOutput      string         `json:"rawOutput,omitempty"`
	ReportedExact  bool           `json:"reportedExact"`
}

type CheckResult struct {
	DiagnosticsLimited bool          `json:"diagnosticsLimited,omitempty"`
	Preview            bool          `json:"historicalLogPreview,omitempty"`
	Check              Check         `json:"check"`
	Command            CommandResult `json:"command"`
	Status             ResultStatus  `json:"status"`
	Diagnostics        []Diagnostic  `json:"diagnostics,omitempty"`
	Started            time.Time     `json:"started"`
	Finished           time.Time     `json:"finished"`
}

// Blocking: a required check that failed, or that could not produce evidence.
func (r CheckResult) Blocking() bool {
	return r.Check.Required && r.Status != StatusPass
}

// ProjectBoundary is one directory that owns a language toolchain manifest
// (or a language detected purely from source extensions).
type ProjectBoundary struct {
	Dir        string   `json:"dir"`
	Kind       string   `json:"kind"`
	Language   string   `json:"language"`
	Indicators []string `json:"indicators,omitempty"`
	Sources    int      `json:"sources"`
}

type Project struct {
	Boundaries        []ProjectBoundary `json:"boundaries,omitempty"`
	SourceFiles       int               `json:"sourceFiles,omitempty"`
	Name              string            `json:"name"`
	Root              string            `json:"root"`
	Languages         []string          `json:"languages"`
	PackageManager    string            `json:"packageManager,omitempty"`
	Indicators        []string          `json:"indicators"`
	Frameworks        []string          `json:"frameworks,omitempty"`
	Scripts           []string          `json:"scripts,omitempty"`
	Workspace         bool              `json:"workspace,omitempty"`
	WorkspacePackages []string          `json:"workspacePackages,omitempty"`
	CI                []string          `json:"ci,omitempty"`
	CILocalSteps      []string          `json:"ciLocalSteps,omitempty"`
	CIRemoteSteps     []string          `json:"ciRemoteSteps,omitempty"`
}

type PushTarget struct {
	Remote       string `json:"remote"`
	RemoteURL    string `json:"remoteUrl,omitempty"`
	Branch       string `json:"branch"`
	Upstream     string `json:"upstream,omitempty"`
	DetachedHEAD bool   `json:"detachedHead"`
	Base         string `json:"base,omitempty"`
	Refspec      string `json:"refspec,omitempty"`
}

type ChangeSet struct {
	Staged    []string `json:"staged,omitempty"`
	Unstaged  []string `json:"unstaged,omitempty"`
	Untracked []string `json:"untracked,omitempty"`
	All       []string `json:"all,omitempty"`
	Commits   int      `json:"outgoingCommits"`
	Ahead     int      `json:"ahead"`
	Behind    int      `json:"behind"`
	Outgoing  []string `json:"outgoing,omitempty"`
	Files     []string `json:"outgoingFiles,omitempty"`
	Conflicts []string `json:"conflicts,omitempty"`
}

type Repository struct {
	Root         string     `json:"root"`
	GitDir       string     `json:"gitDir,omitempty"`
	CWD          string     `json:"cwd"`
	Branch       string     `json:"branch"`
	HEAD         string     `json:"head"`
	Project      Project    `json:"project"`
	Target       PushTarget `json:"target"`
	Changes      ChangeSet  `json:"changes"`
	HasLFS       bool       `json:"hasLfs"`
	LFSUsed      bool       `json:"lfsUsed"`
	LFSInstalled bool       `json:"lfsInstalled"`
	MergeState   bool       `json:"mergeState"`
	RebaseState  bool       `json:"rebaseState"`
	CherryPick   bool       `json:"cherryPickState"`
}

type ContextBundle struct {
	CheckName     string            `json:"checkName,omitempty"`
	Command       string            `json:"command,omitempty"`
	Diagnostic    Diagnostic        `json:"diagnostic"`
	Diagnostics   []Diagnostic      `json:"diagnostics,omitempty"`
	Analysis      *Analysis         `json:"analysis,omitempty"`
	Source        string            `json:"source,omitempty"`
	Related       []string          `json:"related,omitempty"`
	Diff          string            `json:"diff,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	EditableFiles []string          `json:"editableFiles,omitempty"`
	SourceFiles   []ContextFile     `json:"sourceFiles,omitempty"`
}

type ContextFile struct {
	FocusLines []int  `json:"focusLines,omitempty"`
	File       string `json:"file"`
	Content    string `json:"content"`
	Editable   bool   `json:"editable"`
	StartLine  int    `json:"startLine,omitempty"`
	EndLine    int    `json:"endLine,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
}

type Analysis struct {
	Summary    string   `json:"summary"`
	RootCause  string   `json:"rootCause"`
	Confidence string   `json:"confidence,omitempty"`
	Evidence   []string `json:"evidence,omitempty"`
}

type RepairProposal struct {
	Status               string            `json:"status,omitempty"`
	ContextRequired      *ContextRequest   `json:"contextRequired,omitempty"`
	DiagnosticsAddressed []string          `json:"diagnosticsAddressed,omitempty"`
	Inference            *InferenceMetrics `json:"-"`
	Summary              string            `json:"summary"`
	RootCause            string            `json:"rootCause"`
	Files                []string          `json:"files"`
	Patch                string            `json:"patch"`
	Verification         []string          `json:"verification,omitempty"`
	Confidence           string            `json:"confidence,omitempty"`
	Risks                []string          `json:"risks,omitempty"`
	Edits                []TextEdit        `json:"edits,omitempty"`
}

// ContextRequest asks the controller for bounded evidence, never a tool call.
type ContextRequest struct {
	Files   []string `json:"files,omitempty"`
	Symbols []string `json:"symbols,omitempty"`
	Reason  string   `json:"reason"`
}

// TextEdit is a proposal for one unique source replacement, never an executable
// action. The Go patch engine turns edits into an exact reviewable unified diff.
type TextEdit struct {
	File    string `json:"file"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// RepairAudit retains decisions even when the proposal is rejected or unavailable.
type RepairAudit struct {
	ContextRequest  *ContextRequest     `json:"contextRequest,omitempty"`
	GroupID         string              `json:"groupId,omitempty"`
	GroupCount      int                 `json:"groupCount,omitempty"`
	ContextEstimate int                 `json:"contextEstimate,omitempty"`
	RequestDuration time.Duration       `json:"requestDuration,omitempty"`
	Inference       *InferenceMetrics   `json:"inference,omitempty"`
	Progress        *DiagnosticProgress `json:"progress,omitempty"`
	FailureID       string              `json:"failureId"`
	Diagnostics     []Diagnostic        `json:"diagnostics"`
	Provider        string              `json:"provider"`
	Model           string              `json:"model,omitempty"`
	RepairDecision  string              `json:"repairDecision"`
	PatchDecision   string              `json:"patchDecision"`
	Proposal        *RepairProposal     `json:"proposal,omitempty"`
	PatchHash       string              `json:"patchHash,omitempty"`
	Rejection       string              `json:"rejection,omitempty"`
	Verification    *CheckResult        `json:"verification,omitempty"`
	Applied         bool                `json:"applied"`
	At              time.Time           `json:"at"`
}

type Snapshot struct {
	ID        string            `json:"id"`
	Root      string            `json:"root"`
	CreatedAt time.Time         `json:"createdAt"`
	Files     []string          `json:"files"`
	Modes     map[string]uint32 `json:"modes,omitempty"`
	Dir       string            `json:"dir"`
}

type PreflightItem struct {
	Name     string       `json:"name"`
	Status   ResultStatus `json:"status"`
	Detail   string       `json:"detail,omitempty"`
	Blocking bool         `json:"blocking"`
}

type PreflightResult struct {
	Items      []PreflightItem `json:"items"`
	Passed     bool            `json:"passed"`
	RemoteHEAD string          `json:"remoteHead,omitempty"`
	Bytes      int64           `json:"estimatedUncompressedBytes,omitempty"`
}

type StateFingerprint struct {
	Value       string    `json:"value"`
	HEAD        string    `json:"head"`
	CreatedAt   time.Time `json:"createdAt"`
	Description string    `json:"description,omitempty"`
}

type ReviewEntry struct {
	File         string   `json:"file"`
	Reason       string   `json:"reason"`
	Diagnostics  []string `json:"diagnostics,omitempty"`
	Checks       []string `json:"checks,omitempty"`
	AfterHash    string   `json:"afterHash,omitempty"`
	PatchApplied bool     `json:"patchApplied"`
}

type SessionReport struct {
	RepairMetrics     RepairMetrics     `json:"repairMetrics"`
	LogArtifacts      []string          `json:"logArtifacts,omitempty"`
	SchemaVersion     string            `json:"schemaVersion"`
	Status            ResultStatus      `json:"status"`
	State             string            `json:"state"`
	Repository        *Repository       `json:"repository,omitempty"`
	Checks            []CheckResult     `json:"checks,omitempty"`
	Diagnostics       []Diagnostic      `json:"diagnostics,omitempty"`
	Repairs           []RepairProposal  `json:"repairs,omitempty"`
	Preflight         *PreflightResult  `json:"preflight,omitempty"`
	Review            []ReviewEntry     `json:"review,omitempty"`
	Approvals         []string          `json:"approvals,omitempty"`
	Error             string            `json:"error,omitempty"`
	GeneratedAt       time.Time         `json:"generatedAt"`
	SessionID         string            `json:"sessionId"`
	Version           string            `json:"version"`
	Root              string            `json:"root"`
	Operation         string            `json:"operation"`
	Mode              string            `json:"mode,omitempty"`
	ConfigHash        string            `json:"configurationHash,omitempty"`
	Fingerprint       *StateFingerprint `json:"fingerprint,omitempty"`
	ApprovalEvents    []ApprovalEvent   `json:"approvalEvents,omitempty"`
	RepairCycles      int               `json:"repairCycles"`
	ReviewComplete    bool              `json:"reviewComplete"`
	PushResult        *CommandResult    `json:"pushResult,omitempty"`
	CommitResult      *CommandResult    `json:"commitResult,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	Snapshots         []Snapshot        `json:"snapshots,omitempty"`
	History           []CheckResult     `json:"verificationHistory,omitempty"`
	RepairAudit       []RepairAudit     `json:"repairAudit,omitempty"`
	StateHistory      []string          `json:"stateHistory,omitempty"`
	Events            []Event           `json:"events,omitempty"`
	DeliveryReceiptID string            `json:"deliveryReceiptId,omitempty"`
	PullRequestURL    string            `json:"pullRequestUrl,omitempty"`
	PullRequestNumber int               `json:"pullRequestNumber,omitempty"`
	HostedState       string            `json:"hostedState,omitempty"`
}

type Event struct {
	Name   string    `json:"name"`
	At     time.Time `json:"at"`
	Detail string    `json:"detail,omitempty"`
}

type ApprovalEvent struct {
	ID      string    `json:"id"`
	Scope   string    `json:"scope"`
	Binding string    `json:"binding"`
	At      time.Time `json:"at"`
}

const Version = "0.3.0"

// Build metadata is injected by release builds. Development builds remain
// useful and clearly identify themselves instead of pretending to be releases.
var (
	BuildCommit = "dev"
	BuildDate   = "unknown"
)

// Configured checks are required unless the configuration explicitly opts out.
func (c *Check) UnmarshalJSON(data []byte) error {
	type plain Check
	value := plain{Required: true}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*c = Check(value)
	return nil
}
