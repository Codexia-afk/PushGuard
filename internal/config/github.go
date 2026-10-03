package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Authentication is delegated to gh / its environment, never repository config.
type GitHubConfig struct {
	Enabled bool               `json:"enabled"`
	PR      PRConfig           `json:"pr"`
	Checks  GitHubChecksConfig `json:"checks"`
}

type PRConfig struct {
	Enabled    bool   `json:"enabled"`
	BaseBranch string `json:"base_branch,omitempty"`
	Draft      bool   `json:"draft"`
}

func setGitHubYAML(c *GitHubConfig, section string, indent int, key, value string) error {
	if indent == 2 && key == "enabled" {
		v, err := strconv.ParseBool(unquote(value))
		if err != nil {
			return err
		}
		c.Enabled = v
		return nil
	}
	if indent != 4 {
		return fmt.Errorf("GitHub PR/check fields require four-space indentation")
	}
	switch section + "." + key {
	case "pr.base_branch":
		c.PR.BaseBranch = unquote(value)
		return nil
	case "checks.required":
		if err := json.Unmarshal([]byte(value), &c.Checks.Required); err != nil {
			return fmt.Errorf("github.checks.required uses a JSON-style list, for example [\"ci\", \"tests\"]")
		}
		return nil
	case "pr.enabled", "pr.draft", "checks.publish":
		v, err := strconv.ParseBool(unquote(value))
		if err != nil {
			return err
		}
		switch section + "." + key {
		case "pr.enabled":
			c.PR.Enabled = v
		case "pr.draft":
			c.PR.Draft = v
		case "checks.publish":
			c.Checks.Publish = v
		}
		return nil
	default:
		return fmt.Errorf("unknown GitHub configuration field %s.%s", section, key)
	}
}

type GitHubChecksConfig struct {
	Publish  bool     `json:"publish"`
	Required []string `json:"required,omitempty"`
}

func (c GitHubConfig) Validate() error {
	if strings.ContainsAny(c.PR.BaseBranch, "\x00\r\n") || strings.HasPrefix(c.PR.BaseBranch, "-") {
		return fmt.Errorf("github.pr.base_branch is invalid")
	}
	seen := map[string]bool{}
	for _, name := range c.Checks.Required {
		if strings.TrimSpace(name) == "" || len(name) > 200 || strings.ContainsAny(name, "\r\n\x00") || seen[name] || name == "PushGuard Verification" {
			return fmt.Errorf("invalid or duplicate github.checks.required name")
		}
		seen[name] = true
	}
	return nil
}
