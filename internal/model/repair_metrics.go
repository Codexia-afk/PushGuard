package model

import "time"

type InferenceMetrics struct {
	EstimatedInputTokens int           `json:"estimatedInputTokens"`
	MaxInputTokens       int           `json:"maxInputTokens"`
	ReservedOutputTokens int           `json:"reservedOutputTokens"`
	SafetyMarginTokens   int           `json:"safetyMarginTokens"`
	InputTokens          int           `json:"inputTokens,omitempty"`
	OutputTokens         int           `json:"outputTokens,omitempty"`
	LoadDuration         time.Duration `json:"loadDuration,omitempty"`
	RequestDuration      time.Duration `json:"requestDuration"`
	ContextReduced       bool          `json:"contextReduced,omitempty"`
}
type DiagnosticProgress struct {
	Before    int  `json:"before"`
	After     int  `json:"after"`
	Resolved  int  `json:"resolved"`
	Remaining int  `json:"remaining"`
	New       int  `json:"new"`
	Complete  bool `json:"complete"`
}
type CheckProgress struct {
	Check    string `json:"check"`
	Initial  int    `json:"initial"`
	Current  int    `json:"current"`
	Complete bool   `json:"complete"`
}
type RepairMetrics struct {
	Checks               []CheckProgress `json:"checks,omitempty"`
	Attempts             int             `json:"attempts"`
	AIRequests           int             `json:"aiRequests"`
	Applied              int             `json:"applied"`
	Rejected             int             `json:"rejected"`
	NewDiagnostics       int             `json:"newDiagnostics"`
	FilesModified        []string        `json:"filesModified,omitempty"`
	VerificationCommands []string        `json:"verificationCommands,omitempty"`
	StartedAt            time.Time       `json:"startedAt,omitempty"`
	Duration             time.Duration   `json:"duration"`
}
