package config

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pushguard/pushguard/internal/model"
)

type RepairConfig struct {
	Enabled                  bool `json:"enabled"`
	RequireGenerationConsent bool `json:"requireGenerationConsent"`
	RequireApplyApproval     bool `json:"requireApplyApproval"`
	MaxAttempts              int  `json:"maxAttempts"`
	MaxDiagnosticsPerGroup   int  `json:"maxDiagnosticsPerGroup,omitempty"`
}

type AIConfig struct {
	Enabled               *bool         `json:"enabled,omitempty"`
	Provider              string        `json:"provider"`
	Model                 string        `json:"model"`
	Endpoint              string        `json:"endpoint"`
	LocalFirst            bool          `json:"localFirst"`
	APIKeyEnv             string        `json:"apiKeyEnv,omitempty"`
	MaxOutputTokens       int           `json:"maxOutputTokens,omitempty"`
	ReasoningEffort       string        `json:"reasoningEffort,omitempty"`
	Context               ContextBudget `json:"context,omitempty"`
	RequestTimeoutSeconds int           `json:"requestTimeoutSeconds,omitempty"`
}

func (a AIConfig) IsEnabled() bool {
	return (a.Enabled == nil || *a.Enabled) && a.Provider != "none" && a.Provider != ""
}

// Accept the documented snake_case API fields while retaining existing config
// files. Conflicting aliases and unknown fields fail instead of being ignored.
func (a *AIConfig) UnmarshalJSON(data []byte) error {
	type plain AIConfig
	v := struct {
		*plain
		BaseURL *string `json:"base_url"`
		KeyEnv  *string `json:"api_key_env"`
	}{plain: (*plain)(a)}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if v.BaseURL != nil {
		if _, exists := fields["endpoint"]; exists {
			return fmt.Errorf("use ai.endpoint or ai.base_url, not both")
		}
		a.Endpoint = *v.BaseURL
	}
	if v.KeyEnv != nil {
		if _, exists := fields["apiKeyEnv"]; exists {
			return fmt.Errorf("use ai.apiKeyEnv or ai.api_key_env, not both")
		}
		a.APIKeyEnv = *v.KeyEnv
	}
	return nil
}

type ReviewConfig struct {
	RequiredAfterAIChanges bool `json:"requiredAfterAIChanges"`
}
type PushConfig struct {
	RequireExplicitConfirmation bool `json:"requireExplicitConfirmation"`
	InvalidateOnStateChange     bool `json:"invalidateConfirmationOnStateChange"`
}
type PreflightConfig struct {
	Git              bool `json:"git"`
	LFS              bool `json:"lfs"`
	Remote           bool `json:"remote"`
	Resource         bool `json:"resource"`
	RepositoryPolicy bool `json:"repositoryPolicy"`
}
type PrivacyConfig struct {
	RedactSecrets bool `json:"redactSecrets"`
	AllowRemoteAI bool `json:"allowRemoteAI"`
}

type Config struct {
	Version   int             `json:"version"`
	Checks    []model.Check   `json:"checks,omitempty"`
	Repair    RepairConfig    `json:"repair"`
	AI        AIConfig        `json:"ai"`
	Review    ReviewConfig    `json:"review"`
	Push      PushConfig      `json:"push"`
	Preflight PreflightConfig `json:"preflight"`
	Privacy   PrivacyConfig   `json:"privacy"`
	GitHub    GitHubConfig    `json:"github,omitempty"`
}

func Default() Config {
	return Config{
		Version:   1,
		Repair:    RepairConfig{Enabled: true, RequireGenerationConsent: true, RequireApplyApproval: true, MaxAttempts: 5},
		AI:        AIConfig{Provider: "none", Model: "", Endpoint: "http://127.0.0.1:11434", LocalFirst: true},
		Review:    ReviewConfig{RequiredAfterAIChanges: true},
		Push:      PushConfig{RequireExplicitConfirmation: true, InvalidateOnStateChange: true},
		Preflight: PreflightConfig{Git: true, LFS: true, Remote: true, Resource: true, RepositoryPolicy: true},
		Privacy:   PrivacyConfig{RedactSecrets: true, AllowRemoteAI: false},
	}
}

func Load(root string) (Config, string, error) {
	cfg := Default()
	selected, err := selectConfig(localConfigPaths(root))
	if err != nil {
		return cfg, "", err
	}
	if selected == "" {
		selected, err = selectConfig(globalConfigPaths())
		if err != nil {
			return cfg, "", err
		}
	}
	if selected == "" {
		return cfg, "defaults", nil
	}
	path := selected
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return cfg, path, fmt.Errorf("configuration disappeared while it was being read")
	}
	if err != nil {
		return cfg, path, err
	}
	cfg, err = Decode(data, filepath.Base(path))
	return cfg, path, err
}

// Decode also validates committed configuration fetched by a code host. It
// never reads user defaults, executes scripts, or interprets arbitrary YAML.
func Decode(data []byte, name string) (Config, error) {
	cfg := Default()
	path := name
	if strings.HasSuffix(path, ".json") {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return cfg, fmt.Errorf("configuration must contain one JSON object")
		}
	} else if err := parseYAML(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range cfg.Checks {
		if len(cfg.Checks[i].Args) == 0 && cfg.Checks[i].Command != "" {
			args, err := ParseCommand(cfg.Checks[i].Command)
			if err != nil {
				return cfg, fmt.Errorf("check %s: %w", cfg.Checks[i].Name, err)
			}
			cfg.Checks[i].Args = args
		}
	}
	return cfg, Validate(cfg)
}

func localConfigPaths(root string) []string {
	return []string{filepath.Join(root, ".pushguard.json"), filepath.Join(root, ".pushguard.yaml"), filepath.Join(root, ".pushguard.yml")}
}

// globalConfigPaths returns user-level defaults. A repository configuration
// always wins; PUSHGUARD_CONFIG_FILE is useful for managed developer setups
// and isolated CI environments.
func globalConfigPaths() []string {
	if path := os.Getenv("PUSHGUARD_CONFIG_FILE"); path != "" {
		return []string{path}
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(dir, "pushguard")
	return []string{filepath.Join(base, "config.json"), filepath.Join(base, "config.yaml"), filepath.Join(base, "config.yml")}
}

func selectConfig(paths []string) (string, error) {
	var selected string
	for _, path := range paths {
		info, statErr := os.Lstat(path)
		if os.IsNotExist(statErr) {
			continue
		}
		if statErr != nil {
			return "", fmt.Errorf("inspect configuration %s: %w", path, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("configuration must not be a symbolic link: %s", path)
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return "", fmt.Errorf("configuration must be a regular file no larger than 1 MiB: %s", path)
		}
		if selected != "" {
			return "", fmt.Errorf("multiple PushGuard configuration files found: %s and %s; keep only one", selected, path)
		}
		selected = path
	}
	return selected, nil
}

func Save(root string, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return fmt.Errorf("cannot save invalid configuration: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, ".pushguard.json")
	for _, name := range []string{".pushguard.json", ".pushguard.yaml", ".pushguard.yml"} {
		if _, err := os.Lstat(filepath.Join(root, name)); err == nil {
			return fmt.Errorf("%s already exists; edit it with pushguard config instead", name)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	f, err := os.CreateTemp(root, ".pushguard-config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmp)
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func Hash(cfg Config) string {
	data, _ := json.Marshal(cfg)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func parseYAML(data []byte, cfg *Config) error {
	section := ""
	githubSection := ""
	aiSection := ""
	index := -1
	seen := map[string]bool{}
	known := map[string]map[string]bool{
		"repair":    {"enabled": true, "requireGenerationConsent": true, "require_generation_consent": true, "requireApplyApproval": true, "require_apply_approval": true, "maxAttempts": true, "max_attempts": true, "maxDiagnosticsPerGroup": true},
		"ai":        {"enabled": true, "provider": true, "model": true, "endpoint": true, "base_url": true, "apiKeyEnv": true, "api_key_env": true, "localFirst": true, "local_first": true, "maxOutputTokens": true, "reasoningEffort": true, "context": true, "requestTimeoutSeconds": true},
		"review":    {"requiredAfterAIChanges": true, "required_after_ai_changes": true},
		"push":      {"requireExplicitConfirmation": true, "require_explicit_confirmation": true, "invalidateConfirmationOnStateChange": true, "invalidate_confirmation_on_state_change": true},
		"preflight": {"git": true, "lfs": true, "remote": true, "resource": true, "repositoryPolicy": true, "repository_policy": true},
		"privacy":   {"redactSecrets": true, "redact_secrets": true, "allowRemoteAI": true, "allow_remote_ai": true},
		"checks":    {"name": true, "category": true, "command": true, "required": true, "timeoutSeconds": true},
		"github":    {"enabled": true, "pr": true, "checks": true},
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		if strings.ContainsRune(raw, '\t') {
			return fmt.Errorf("YAML line %d: tabs are unsupported", lineNo)
		}
		line := strings.TrimSpace(yamlComment(raw))
		if line == "" || line == "---" {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		list := strings.HasPrefix(line, "- ")
		if list {
			if section != "checks" || indent != 2 {
				return fmt.Errorf("YAML line %d: check lists require two-space indentation", lineNo)
			}
			cfg.Checks = append(cfg.Checks, model.Check{Required: true})
			index = len(cfg.Checks) - 1
			line = strings.TrimSpace(line[2:])
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("YAML line %d: expected key: value", lineNo)
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if indent == 0 {
			if seen[key] {
				return fmt.Errorf("YAML duplicate section %s", key)
			}
			seen[key] = true
			if key == "version" {
				number, err := strconv.Atoi(value)
				if err != nil {
					return err
				}
				cfg.Version = number
				section = ""
				continue
			}
			if key != "checks" && known[key] == nil {
				return fmt.Errorf("YAML unknown section %s", key)
			}
			if value != "" {
				return fmt.Errorf("YAML inline objects are unsupported; use the documented scalar/list form")
			}
			section = key
			githubSection = ""
			aiSection = ""
			continue
		}
		if section == "ai" && indent == 2 && key == "context" && value == "" {
			aiSection = "context"
			continue
		}
		if section == "ai" && aiSection == "context" && indent == 4 {
			number, err := strconv.Atoi(unquote(value))
			if err != nil {
				return fmt.Errorf("AI context values must be integers")
			}
			switch key {
			case "max_input_tokens":
				cfg.AI.Context.MaxInputTokens = number
			case "reserved_output_tokens":
				cfg.AI.Context.ReservedOutputTokens = number
			case "safety_margin_tokens":
				cfg.AI.Context.SafetyMarginTokens = number
			default:
				return fmt.Errorf("unknown AI context field %s", key)
			}
			continue
		}
		if section == "github" {
			if indent == 2 && (key == "pr" || key == "checks") && value == "" {
				githubSection = key
				continue
			}
			if err := setGitHubYAML(&cfg.GitHub, githubSection, indent, key, value); err != nil {
				return fmt.Errorf("YAML line %d: %w", lineNo, err)
			}
			continue
		}
		if !known[section][key] {
			return fmt.Errorf("YAML unknown key %s.%s", section, key)
		}
		expected := 2
		if section == "checks" && !list {
			expected = 4
		}
		if indent != expected {
			return fmt.Errorf("YAML line %d: expected %d-space indentation", lineNo, expected)
		}
		if value == "" || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") || strings.HasPrefix(value, "&") || strings.HasPrefix(value, "*") {
			return fmt.Errorf("YAML multiline/anchor values are unsupported; use JSON for complex configuration")
		}
		boolKey := key == "required" || key == "enabled" || strings.HasPrefix(key, "require") || strings.HasPrefix(key, "invalidate") || strings.HasPrefix(key, "redact") || strings.HasPrefix(key, "allow") || key == "localFirst" || key == "local_first" || section == "preflight"
		if boolKey {
			if _, err := strconv.ParseBool(unquote(value)); err != nil {
				return fmt.Errorf("YAML %s.%s requires a boolean", section, key)
			}
		}
		if key == "maxAttempts" || key == "max_attempts" || key == "timeoutSeconds" || key == "maxOutputTokens" || key == "maxDiagnosticsPerGroup" || key == "requestTimeoutSeconds" {
			if _, err := strconv.Atoi(unquote(value)); err != nil {
				return fmt.Errorf("YAML %s.%s requires an integer", section, key)
			}
		}
		if strings.HasPrefix(value, "\"") && !strings.HasSuffix(value, "\"") || strings.HasPrefix(value, "'") && !strings.HasSuffix(value, "'") {
			return fmt.Errorf("YAML unterminated quoted value")
		}
		if section == "checks" {
			if index < 0 {
				return fmt.Errorf("YAML check properties require a list item")
			}
			setCheckScalar(&cfg.Checks[index], key, value)
		} else {
			setNestedScalar(cfg, section, key, value)
		}
	}
	return scanner.Err()
}
func yamlComment(s string) string {
	var quote rune
	for i, r := range s {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
		}
		if r == '#' {
			return s[:i]
		}
	}
	return s
}

func unquote(v string) string { return strings.Trim(strings.TrimSpace(v), "\"'") }
func boolValue(v string) bool { b, _ := strconv.ParseBool(unquote(v)); return b }
func intValue(v string) int   { i, _ := strconv.Atoi(unquote(v)); return i }
func setYAMLScalar(cfg *Config, key, value string) {
	switch key {
	case "version":
		cfg.Version = intValue(value)
	}
}
func setCheckScalar(c *model.Check, key, value string) {
	switch key {
	case "name":
		c.Name = unquote(value)
	case "category":
		c.Category = unquote(value)
	case "command":
		c.Command = unquote(value)
		c.Args = splitCommand(c.Command)
	case "required":
		c.Required = boolValue(value)
	case "timeoutSeconds":
		c.TimeoutSecs = intValue(value)
	}
}
func setNestedScalar(cfg *Config, section, key, value string) {
	switch section {
	case "repair":
		switch key {
		case "maxDiagnosticsPerGroup":
			cfg.Repair.MaxDiagnosticsPerGroup = intValue(value)
		case "enabled":
			cfg.Repair.Enabled = boolValue(value)
		case "require_generation_consent":
			cfg.Repair.RequireGenerationConsent = boolValue(value)
		case "requireGenerationConsent":
			cfg.Repair.RequireGenerationConsent = boolValue(value)
		case "require_apply_approval":
			cfg.Repair.RequireApplyApproval = boolValue(value)
		case "requireApplyApproval":
			cfg.Repair.RequireApplyApproval = boolValue(value)
		case "max_attempts":
			cfg.Repair.MaxAttempts = intValue(value)
		case "maxAttempts":
			cfg.Repair.MaxAttempts = intValue(value)
		}
	case "ai":
		switch key {
		case "requestTimeoutSeconds":
			cfg.AI.RequestTimeoutSeconds = intValue(value)
		case "maxOutputTokens":
			cfg.AI.MaxOutputTokens = intValue(value)
		case "reasoningEffort":
			cfg.AI.ReasoningEffort = unquote(value)
		case "enabled":
			value := boolValue(value)
			cfg.AI.Enabled = &value
		case "provider":
			cfg.AI.Provider = unquote(value)
		case "model":
			cfg.AI.Model = unquote(value)
		case "endpoint", "base_url":
			cfg.AI.Endpoint = unquote(value)
		case "apiKeyEnv", "api_key_env":
			cfg.AI.APIKeyEnv = unquote(value)
		case "local_first":
			cfg.AI.LocalFirst = boolValue(value)
		case "localFirst":
			cfg.AI.LocalFirst = boolValue(value)
		}
	case "review":
		if key == "required_after_ai_changes" || key == "requiredAfterAIChanges" {
			cfg.Review.RequiredAfterAIChanges = boolValue(value)
		}
	case "push":
		switch key {
		case "require_explicit_confirmation", "requireExplicitConfirmation":
			cfg.Push.RequireExplicitConfirmation = boolValue(value)
		case "invalidate_confirmation_on_state_change", "invalidateConfirmationOnStateChange":
			cfg.Push.InvalidateOnStateChange = boolValue(value)
		}
	case "preflight":
		switch key {
		case "git":
			cfg.Preflight.Git = boolValue(value)
		case "lfs":
			cfg.Preflight.LFS = boolValue(value)
		case "remote":
			cfg.Preflight.Remote = boolValue(value)
		case "resource":
			cfg.Preflight.Resource = boolValue(value)
		case "repository_policy", "repositoryPolicy":
			cfg.Preflight.RepositoryPolicy = boolValue(value)
		}
	case "privacy":
		switch key {
		case "redact_secrets", "redactSecrets":
			cfg.Privacy.RedactSecrets = boolValue(value)
		case "allow_remote_ai", "allowRemoteAI":
			cfg.Privacy.AllowRemoteAI = boolValue(value)
		}
	}
}

func splitCommand(s string) []string { args, _ := ParseCommand(s); return args }

// ParseCommand tokenizes arguments without shell expansion. Backslashes in Windows
// paths are preserved; only escaped quotes, spaces, and backslashes are special.
func ParseCommand(s string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	started := false
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\\' && i+1 < len(rs) && (rs[i+1] == quote && quote != 0 || quote == 0 && (rs[i+1] == ' ' || rs[i+1] == '\t' || rs[i+1] == '"' || rs[i+1] == '\'')) {
			i++
			b.WriteRune(rs[i])
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			started = true
			continue
		}
		if r == ' ' || r == '\t' {
			if started {
				args = append(args, b.String())
				b.Reset()
				started = false
			}
			continue
		}
		if r == '\n' || r == '\r' || r == 0 {
			return nil, fmt.Errorf("commands cannot contain control characters")
		}
		b.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in command")
	}
	if started {
		args = append(args, b.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}

func Validate(c Config) error {
	if err := c.AI.Context.Validate(c.AI.MaxOutputTokens); err != nil {
		return err
	}
	if c.AI.RequestTimeoutSeconds != 0 && (c.AI.RequestTimeoutSeconds < 10 || c.AI.RequestTimeoutSeconds > 900) {
		return fmt.Errorf("ai.requestTimeoutSeconds must be between 10 and 900")
	}
	if c.Repair.MaxDiagnosticsPerGroup < 0 || c.Repair.MaxDiagnosticsPerGroup > 50 {
		return fmt.Errorf("repair.maxDiagnosticsPerGroup must be between 1 and 50 when configured")
	}
	if err := c.GitHub.Validate(); err != nil {
		return err
	}
	if c.Version != 1 {
		return fmt.Errorf("unsupported configuration version %d", c.Version)
	}
	if c.Repair.MaxAttempts < 1 || c.Repair.MaxAttempts > 20 {
		return fmt.Errorf("repair.maxAttempts must be between 1 and 20")
	}
	if !c.Repair.RequireGenerationConsent || !c.Repair.RequireApplyApproval || !c.Review.RequiredAfterAIChanges || !c.Push.RequireExplicitConfirmation || !c.Push.InvalidateOnStateChange {
		return fmt.Errorf("human approvals, review, and state protection cannot be disabled")
	}
	if !c.Preflight.Git || !c.Preflight.Remote {
		return fmt.Errorf("Git and remote preflight cannot be disabled for the push workflow")
	}
	if !c.Privacy.RedactSecrets {
		return fmt.Errorf("secret redaction cannot be disabled")
	}
	switch c.AI.Provider {
	case "none", "ollama", "openai-compatible":
	default:
		return fmt.Errorf("unsupported AI provider %q", c.AI.Provider)
	}
	for i, ch := range c.AI.APIKeyEnv {
		if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || i > 0 && ch >= '0' && ch <= '9') {
			return fmt.Errorf("ai.apiKeyEnv must name an environment variable, never contain a credential")
		}
	}
	if c.AI.MaxOutputTokens != 0 && (c.AI.MaxOutputTokens < 256 || c.AI.MaxOutputTokens > 16384) {
		return fmt.Errorf("ai.maxOutputTokens must be 256..16384, or omitted for the 2048 default")
	}
	switch c.AI.ReasoningEffort {
	case "", "low", "medium", "high":
	default:
		return fmt.Errorf("ai.reasoningEffort must be low, medium, high, or omitted")
	}
	if len(c.Checks) > 50 {
		return fmt.Errorf("at most 50 checks may be configured")
	}
	seen := map[string]bool{}
	for _, check := range c.Checks {
		if len(check.Name) > 128 || strings.TrimSpace(check.Name) == "" || seen[check.Name] {
			return fmt.Errorf("checks must have distinct nonempty names")
		}
		seen[check.Name] = true
		if len(check.Args) == 0 || len(check.Args) > 256 || strings.TrimSpace(check.Args[0]) == "" {
			return fmt.Errorf("check %s has no executable", check.Name)
		}
		if check.TimeoutSecs < 0 || check.TimeoutSecs > 7200 {
			return fmt.Errorf("invalid timeout for check %s", check.Name)
		}
		for _, arg := range check.Args {
			if len(arg) > 8192 || strings.ContainsRune(arg, 0) {
				return fmt.Errorf("check %s contains NUL", check.Name)
			}
		}
	}
	return nil
}
