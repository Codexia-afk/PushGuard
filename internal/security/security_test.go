package security

import (
	"strings"
	"testing"

	"github.com/pushguard/pushguard/internal/model"
)

func TestRedactAndSanitizeReport(t *testing.T) {
	report := model.SessionReport{Error: "token: abc123", Checks: []model.CheckResult{{Command: model.CommandResult{Stdout: "Authorization: Bearer abc123"}}}, Repairs: []model.RepairProposal{{Patch: "+ password=topsecret"}}}
	safe := SanitizeReport(report)
	joined := safe.Error + safe.Checks[0].Command.Stdout + safe.Repairs[0].Patch
	if strings.Contains(joined, "abc123") || strings.Contains(joined, "topsecret") {
		t.Fatalf("secret leaked: %q", joined)
	}
}

func TestStandaloneProviderKeysAreFullyRedacted(t *testing.T) {
	for _, prefix := range []string{"gsk_", "sk-", "ghp_", "gho_", "ghs_", "ghu_", "ghr_", "github_pat_"} {
		key := prefix + strings.Repeat("synthetic", 4)
		if got := Redact("failure with " + key); strings.Contains(got, key) {
			t.Fatalf("provider key leaked: prefix=%s", prefix)
		}
	}
}

func TestCredentialCommandArgumentsAreRedacted(t *testing.T) {
	for _, command := range []string{"check --token secret-value", "check --password=secret-value", `check --api-key "secret-value"`} {
		if strings.Contains(Redact(command), "secret-value") {
			t.Fatal("credential argument leaked")
		}
	}
}
func TestSensitivePath(t *testing.T) {
	for _, p := range []string{".env", "private.pem", "credentials.json"} {
		if !SensitivePath(p) {
			t.Fatalf("%s not sensitive", p)
		}
	}
}

func TestSanitizationPreservesOriginalProposal(t *testing.T) {
	in := model.SessionReport{Repairs: []model.RepairProposal{{Patch: "+ password=literal"}}}
	out := SanitizeReport(in)
	if out.Repairs[0].Patch == in.Repairs[0].Patch || in.Repairs[0].Patch != "+ password=literal" {
		t.Fatal("sanitization mutated the approved proposal")
	}
}

func TestQuotedJSONSecretsAndCredentialsAreRedacted(t *testing.T) {
	for _, text := range []string{`{"password":"secret with spaces"}`, `api_key='literal value'`, `https://name:password@example.test/repo`} {
		redacted := Redact(text)
		if redacted == text || strings.Contains(redacted, "secret with spaces") || strings.Contains(redacted, "literal value") || strings.Contains(redacted, "name:password") {
			t.Fatalf("secret leaked: %s", redacted)
		}
	}
}

func TestTypedCredentialParametersRetainExactSource(t *testing.T) {
	for _, source := range []string{
		"public isTokenValid(token: string, payload: TokenPayload): boolean {",
		"public async executePayment(amount: number, userToken: string): Promise<string> {",
		"function authenticate(password: string, apiKey: string) {",
	} {
		if got := Redact(source); got != source {
			t.Fatalf("type annotation corrupted: %q", got)
		}
	}
	for _, source := range []string{"TOKEN=string", "token: abc123", `{"token":"string"}`, "token: string", "function login(password = 'real-secret') {"} {
		if Redact(source) == source {
			t.Fatalf("credential not redacted: %q", source)
		}
	}
}

func TestRedactionDoesNotCorruptSourceCode(t *testing.T) {
	for _, code := range []string{
		"token = get_token()",
		"const token = process.env.API_TOKEN;",
		"self.secret = secret_provider.load(name)",
		"api_key = os.environ[\"API_KEY\"]",
		"if (token === undefined) { return null }",
	} {
		if got := Redact(code); got != code {
			t.Fatalf("Redact corrupted %q -> %q", code, got)
		}
	}
	for _, secret := range []string{"password = \"hunter22\"", "token=abc123", "API_KEY=supersecretvalue"} {
		if Redact(secret) == secret {
			t.Fatalf("literal secret not redacted: %q", secret)
		}
	}
}
