package config

import "testing"

func TestGitHubNestedYAML(t *testing.T) {
	data := []byte("version: 1\ngithub:\n  enabled: true\n  pr:\n    enabled: true\n    base_branch: main\n    draft: true\n  checks:\n    publish: true\n    required: [\"ci\", \"tests\"]\n")
	cfg, err := Decode(data, ".pushguard.yaml")
	if err != nil || !cfg.GitHub.Checks.Publish || !cfg.GitHub.PR.Draft || len(cfg.GitHub.Checks.Required) != 2 {
		t.Fatalf("%v %+v", err, cfg.GitHub)
	}
}

func TestGitHubConfigIsOptionalAndHasNoCredentialFields(t *testing.T) {
	cfg, err := Decode([]byte(`{"version":1,"github":{"enabled":true,"pr":{"enabled":true,"base_branch":"main","draft":true},"checks":{"publish":true,"required":["lint","tests"]}}}`), ".pushguard.json")
	if err != nil || !cfg.GitHub.Enabled || !cfg.GitHub.PR.Draft || len(cfg.GitHub.Checks.Required) != 2 {
		t.Fatalf("%v %+v", err, cfg.GitHub)
	}
	if _, err = Decode([]byte(`{"version":1,"github":{"token":"do-not-store"}}`), ".pushguard.json"); err == nil {
		t.Fatal("credential accepted in repository config")
	}
	if Default().GitHub.Enabled {
		t.Fatal("GitHub forced on local-only users")
	}
	if _, err = Decode([]byte(`{"version":1,"github":{"checks":{"required":["PushGuard Verification"]}}}`), ".pushguard.json"); err == nil {
		t.Fatal("self-dependent check policy")
	}
}
