package main

import (
	"context"
	"fmt"
	"time"

	"github.com/pushguard/pushguard/internal/app"
	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/llm"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/security"
	"github.com/pushguard/pushguard/internal/ui"
)

func inspectAI(ctx context.Context, cfg config.Config, test bool) llm.ProviderStatus {
	s := llm.ProviderStatus{Provider: cfg.AI.Provider, Endpoint: cfg.AI.Endpoint, Model: cfg.AI.Model, Service: model.StatusUnknown, ModelStatus: model.StatusUnknown, Generation: model.StatusUnknown}
	p, err := llm.NewRepairProvider(cfg.AI, cfg.Privacy)
	if err != nil {
		s.Problem = err.Error()
		return s
	}
	if disabled, ok := p.(llm.Unavailable); ok {
		s.Service, s.ModelStatus, s.Generation = model.StatusSkipped, model.StatusSkipped, model.StatusSkipped
		s.Problem, s.NextStep = disabled.Reason, "Edit the selected configuration's ai section. See docs/ai-repair.md."
		return s
	}
	provider := p.(llm.Ollama)
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	s = provider.Status(probeCtx)
	cancel()
	if test && s.Problem == "" {
		if err := provider.TestGeneration(ctx); err != nil {
			s.Generation, s.Problem = model.StatusFail, err.Error()
		} else {
			s.Generation, s.NextStep = model.StatusPass, "READY: a real context-free structured response was parsed. Use pushguard push; source repair still needs investigation and apply approvals."
		}
	}
	return s
}

func aiStatusCommand(ctx context.Context, root string, u *ui.UI, jsonOut, test bool) int {
	if repo, err := (repository.Service{}).Discover(ctx, root); err == nil {
		root = repo.Root
	}
	cfg, path, err := config.Load(root)
	if err != nil {
		u.Error(err.Error())
		return app.ExitConfig
	}
	if test && !jsonOut {
		u.Say("Testing the configured provider. A local cold model load may take several minutes; Ctrl+C cancels. No repository source is sent.")
	}
	s := inspectAI(ctx, cfg, test)
	s.Endpoint, s.Problem, s.NextStep = security.Redact(s.Endpoint), security.Redact(s.Problem), security.Redact(s.NextStep)
	code := app.ExitOK
	if s.Problem != "" {
		code = app.ExitAI
	}
	if jsonOut {
		if printJSON(u.Out, map[string]any{"schemaVersion": "2", "configuration": path, "ai": s}) != 0 {
			return app.ExitConfig
		}
		return code
	}
	u.Title("AI Repair Provider")
	u.Say("Configuration: " + path + "\nProvider: " + s.Provider + "\nEndpoint: " + s.Endpoint + "\nModel: " + s.Model)
	u.Say(fmt.Sprintf("Mode: %s\nContext budget: %d input + %d output + %d safety tokens", s.Mode, s.Context.MaxInputTokens, s.Context.ReservedOutputTokens, s.Context.SafetyMarginTokens))
	if s.Mode == "LOCAL" {
		u.Say("Privacy: requests go only to the configured local endpoint. No cloud fallback or cloud API key is required. Start Ollama (or ollama serve) if the local service is unavailable.")
	}
	u.Status(string(s.Service), "Service", "")
	u.Status(string(s.ModelStatus), "Model", "")
	u.Status(string(s.Generation), "Structured generation", "")
	if s.Problem != "" {
		u.Say("Problem: " + s.Problem)
	}
	u.Say(s.NextStep)
	u.Say("Repository configuration takes precedence over user defaults. Deterministic verification works without AI.")
	return code
}
