package codehost

import (
	"context"
	"fmt"

	"github.com/pushguard/pushguard/internal/model"
)

// MockCodeHostProvider is deterministic test infrastructure, never a production
// fallback. Real CLI construction always selects the GitHub provider.
type MockCodeHostProvider struct {
	Remote    RemoteRepository
	PR        *PullRequest
	Commit    CommitState
	Checks    []HostedCheck
	Files     []string
	Err       error
	Calls     []string
	Published []HostedVerification
	OnCreate  func(CreatePullRequestInput) (*PullRequest, error)
	OnGet     func(int) (*PullRequest, error)
}

func (m *MockCodeHostProvider) DetectRepository(context.Context, model.Repository) (*RemoteRepository, error) {
	m.Calls = append(m.Calls, "detect")
	return &m.Remote, m.Err
}
func (m *MockCodeHostProvider) CreatePullRequest(_ context.Context, in CreatePullRequestInput) (*PullRequest, error) {
	m.Calls = append(m.Calls, "create")
	if m.Err != nil {
		return nil, m.Err
	}
	if m.OnCreate != nil {
		p, e := m.OnCreate(in)
		m.PR = p
		return p, e
	}
	return nil, fmt.Errorf("mock create callback required")
}
func (m *MockCodeHostProvider) UpdatePullRequest(_ context.Context, in UpdatePullRequestInput) error {
	m.Calls = append(m.Calls, "update")
	if m.Err != nil {
		return m.Err
	}
	if m.PR == nil {
		return fmt.Errorf("mock PR missing")
	}
	m.PR.Title, m.PR.Body, m.PR.Base = in.Title, in.Body, in.Base
	return nil
}
func (m *MockCodeHostProvider) GetPullRequest(_ context.Context, n int) (*PullRequest, error) {
	m.Calls = append(m.Calls, "get")
	if m.OnGet != nil {
		return m.OnGet(n)
	}
	return m.PR, m.Err
}
func (m *MockCodeHostProvider) FindPullRequest(context.Context, string, string) (*PullRequest, error) {
	m.Calls = append(m.Calls, "find")
	return m.PR, m.Err
}
func (m *MockCodeHostProvider) GetCommitState(context.Context, string) (CommitState, error) {
	m.Calls = append(m.Calls, "commit")
	return m.Commit, m.Err
}
func (m *MockCodeHostProvider) GetChecks(context.Context, string) ([]HostedCheck, error) {
	m.Calls = append(m.Calls, "checks")
	return m.Checks, m.Err
}
func (m *MockCodeHostProvider) GetChangedFiles(context.Context, int) ([]string, error) {
	m.Calls = append(m.Calls, "files")
	return m.Files, m.Err
}
func (m *MockCodeHostProvider) PublishVerification(_ context.Context, r HostedVerification) error {
	m.Calls = append(m.Calls, "publish")
	if m.Err != nil {
		return m.Err
	}
	m.Published = append(m.Published, r)
	return nil
}
