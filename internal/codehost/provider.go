// Package codehost isolates remote hosting operations from verification and AI.
// There is deliberately no merge operation or branch-writing repair operation.
package codehost

import (
	"context"
	"fmt"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

const CheckName = "PushGuard Verification"
const PublisherWorkflow = "PushGuard receipt integration"

type CheckState string

const (
	Queued     CheckState = "QUEUED"
	InProgress CheckState = "IN_PROGRESS"
	Success    CheckState = "SUCCESS"
	Failure    CheckState = "FAILURE"
	Neutral    CheckState = "NEUTRAL"
	Cancelled  CheckState = "CANCELLED"
	Unknown    CheckState = "UNKNOWN"
)

type RemoteRepository struct {
	Host          string `json:"host"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	DefaultBranch string `json:"defaultBranch"`
	CanPush       bool   `json:"canPush"`
}

func (r RemoteRepository) Identity() string {
	return strings.ToLower(r.Host + "/" + r.Owner + "/" + r.Name)
}

type PullRequest struct {
	Number         int    `json:"number"`
	URL            string `json:"url"`
	Title          string `json:"title"`
	Body           string `json:"-"`
	Base           string `json:"base"`
	Head           string `json:"head"`
	HeadSHA        string `json:"headSHA"`
	HeadRepository string `json:"headRepository"`
	Draft          bool   `json:"draft"`
	State          string `json:"state"`
}
type CreatePullRequestInput struct {
	Title, Body, Base, Head string
	Draft                   bool
}
type UpdatePullRequestInput struct {
	Number            int
	Title, Body, Base string
}
type CommitState struct{ TreeHash, ConfigHash string }
type HostedCheck struct {
	ID          int64              `json:"id,omitempty"`
	Name        string             `json:"name"`
	State       CheckState         `json:"state"`
	URL         string             `json:"url,omitempty"`
	Source      string             `json:"source"`
	ReceiptID   string             `json:"receiptId,omitempty"`
	Diagnostics []model.Diagnostic `json:"diagnostics,omitempty"`
}
type Annotation struct {
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	StartColumn int    `json:"start_column,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
	Level       string `json:"annotation_level"`
	Title       string `json:"title"`
	Message     string `json:"message"`
}
type HostedVerification struct {
	HeadSHA     string
	ReceiptID   string
	State       CheckState
	Summary     string
	Annotations []Annotation
}
type CodeHostProvider interface {
	DetectRepository(context.Context, model.Repository) (*RemoteRepository, error)
	CreatePullRequest(context.Context, CreatePullRequestInput) (*PullRequest, error)
	UpdatePullRequest(context.Context, UpdatePullRequestInput) error
	GetPullRequest(context.Context, int) (*PullRequest, error)
	FindPullRequest(context.Context, string, string) (*PullRequest, error)
	GetCommitState(context.Context, string) (CommitState, error)
	GetChecks(context.Context, string) ([]HostedCheck, error)
	GetChangedFiles(context.Context, int) ([]string, error)
	PublishVerification(context.Context, HostedVerification) error
}

type ErrorKind string

const (
	Authentication ErrorKind = "AUTHENTICATION"
	Permission     ErrorKind = "PERMISSION"
	NotFound       ErrorKind = "NOT_FOUND"
	RateLimit      ErrorKind = "RATE_LIMIT"
	Network        ErrorKind = "NETWORK"
	Invalid        ErrorKind = "INVALID_REQUEST"
	Incomplete     ErrorKind = "INCOMPLETE_EVIDENCE"
)

type Error struct {
	Kind      ErrorKind
	Operation string
	Detail    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("code host %s (%s): %s", e.Operation, e.Kind, e.Detail)
}
