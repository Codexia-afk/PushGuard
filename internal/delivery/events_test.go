package delivery

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestHostedEventRoutingNeverTrustsEventConclusions(t *testing.T) {
	for _, body := range []string{
		`{"repository":{"full_name":"acme/repo"},"number":42,"pull_request":{"head":{"sha":"abc","ref":"feature"}}}`,
		`{"repository":{"full_name":"acme/repo"},"workflow_run":{"head_sha":"abc","head_branch":"feature","conclusion":"success","pull_requests":[{"number":42}]}}`,
	} {
		targets, err := EventTargets([]byte(body), "acme/repo")
		if err != nil || len(targets) != 1 || targets[0].Number != 42 {
			t.Fatalf("%v %+v", err, targets)
		}
	}
	if _, err := EventTargets([]byte(`{"repository":{"full_name":"evil/repo"},"number":42,"pull_request":{}}`), "acme/repo"); err == nil {
		t.Fatal("cross-repository event accepted")
	}
	if _, err := EventTargets([]byte(`{"repository":{"full_name":"acme/repo"},"check_run":{"conclusion":"success"}}`), "acme/repo"); err == nil {
		t.Fatal("unsupported event accepted")
	}
}
func TestWebhookRequiresValidHMAC(t *testing.T) {
	body, secret := []byte("event body"), []byte("test-webhook-secret-long-enough")
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write(body)
	sig := "sha256=" + hex.EncodeToString(m.Sum(nil))
	if err := VerifyWebhook(body, sig, secret); err != nil {
		t.Fatal(err)
	}
	if err := VerifyWebhook([]byte("modified"), sig, secret); err == nil {
		t.Fatal("tampered event accepted")
	}
	if err := VerifyWebhook(body, sig, nil); err == nil {
		t.Fatal("missing webhook secret accepted")
	}
}
