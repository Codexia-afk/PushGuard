package receipt

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pushguard/pushguard/internal/model"
)

func signedFixture(t *testing.T) (*VerificationReceipt, Expectation, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v := &VerificationReceipt{Version: 1, Repository: "github.com/acme/example", CommitSHA: strings.Repeat("a", 40), Branch: "feature/fix", TreeHash: strings.Repeat("b", 40), DiffHash: Digest(nil), ConfigHash: Digest([]byte("configuration")), Timestamp: time.Now().UTC(), PushGuardVersion: model.Version, LocalStateHash: Digest([]byte("state")), Checks: []VerifiedCheck{{Name: "tests", Command: "go test ./...", Status: model.StatusPass, Required: true, Tool: "go test"}}}
	v.Sign(key)
	e := Expectation{Repository: v.Repository, CommitSHA: v.CommitSHA, Branch: v.Branch, TreeHash: v.TreeHash, ConfigHash: v.ConfigHash, Keys: map[string]ed25519.PublicKey{Digest(pub): pub}}
	return v, e, key
}
func TestVerificationTrustBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*VerificationReceipt, *Expectation, ed25519.PrivateKey)
		want   Trust
	}{
		{"valid", func(*VerificationReceipt, *Expectation, ed25519.PrivateKey) {}, Valid},
		{"tampered check", func(v *VerificationReceipt, _ *Expectation, _ ed25519.PrivateKey) { v.Checks[0].Command = "skip tests" }, Tampered},
		{"forged hash", func(v *VerificationReceipt, _ *Expectation, _ ed25519.PrivateKey) {
			v.Checks[0].Command = "skip tests"
			v.ID = v.Hash()
			v.StateFingerprint = v.Fingerprint()
		}, Tampered},
		{"unknown issuer", func(_ *VerificationReceipt, e *Expectation, _ ed25519.PrivateKey) { e.Keys = nil }, Unverified},
		{"unsigned", func(v *VerificationReceipt, _ *Expectation, _ ed25519.PrivateKey) { v.Signature = nil }, Unverified},
		{"new branch commit", func(_ *VerificationReceipt, e *Expectation, _ ed25519.PrivateKey) {
			e.CommitSHA = strings.Repeat("c", 40)
		}, Stale},
		{"different commit", func(_ *VerificationReceipt, e *Expectation, _ ed25519.PrivateKey) {
			e.CommitSHA = strings.Repeat("c", 40)
			e.Branch = "other"
		}, CommitMismatch},
		{"other repository", func(_ *VerificationReceipt, e *Expectation, _ ed25519.PrivateKey) {
			e.Repository = "github.com/attacker/example"
		}, CommitMismatch},
		{"other tree", func(_ *VerificationReceipt, e *Expectation, _ ed25519.PrivateKey) {
			e.TreeHash = strings.Repeat("c", 40)
		}, CommitMismatch},
		{"changed config", func(_ *VerificationReceipt, e *Expectation, _ ed25519.PrivateKey) {
			e.ConfigHash = Digest([]byte("new"))
		}, ConfigMismatch},
		{"uncommitted diff", func(v *VerificationReceipt, _ *Expectation, k ed25519.PrivateKey) {
			v.DiffHash = Digest([]byte("diff"))
			v.Sign(k)
		}, Unverified},
		{"failed required check", func(v *VerificationReceipt, _ *Expectation, k ed25519.PrivateKey) {
			v.Checks[0].Status = model.StatusFail
			v.Sign(k)
		}, Unverified},
		{"unapproved AI", func(v *VerificationReceipt, _ *Expectation, k ed25519.PrivateKey) {
			v.AIRepairs = []AIRepairRecord{{PatchHash: "a", HumanApproved: false}}
			v.Sign(k)
		}, Unverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, e, k := signedFixture(t)
			tc.mutate(v, &e, k)
			if got := ValidateVerification(v, e); got.Status != tc.want {
				t.Fatalf("%+v want %s", got, tc.want)
			}
		})
	}
}

func TestReceiptHashIsStableAndStoredImmutably(t *testing.T) {
	t.Setenv("PUSHGUARD_CACHE_DIR", t.TempDir())
	root := t.TempDir()
	v, e, _ := signedFixture(t)
	data, _ := json.Marshal(v)
	var roundtrip VerificationReceipt
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if v.ID != roundtrip.Hash() {
		t.Fatal("hash changed across JSON round trip")
	}
	if err := SaveVerification(root, v); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadVerification(root, v.ID)
	if err != nil || ValidateVerification(loaded, e).Status != Valid {
		t.Fatalf("%v", err)
	}
	if _, err = LoadVerification(root, "../../key"); err == nil {
		t.Fatal("receipt traversal accepted")
	}
	dir, err := repositoryDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "delivery", v.ID+".json"), []byte(`{"id":"tampered"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = SaveVerification(root, v); err == nil {
		t.Fatal("tampered immutable evidence silently reused")
	}
}
func TestSigningKeysAreStablePrivateAndNeverExported(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PUSHGUARD_CACHE_DIR", base)
	key, err := SigningKey()
	if err != nil {
		t.Fatal(err)
	}
	again, err := SigningKey()
	if err != nil || !key.Equal(again) {
		t.Fatal("signer changed")
	}
	public, err := PublicTrust()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(public)
	parsed, err := ParseTrust(data)
	if err != nil || len(parsed) != 1 {
		t.Fatal("public trust did not round trip")
	}
	info, err := os.Lstat(filepath.Join(base, "signing", "ed25519.key"))
	if err != nil || info.Size() != ed25519.PrivateKeySize {
		t.Fatal("missing signing key")
	}
	if strings.Contains(string(data), "PRIVATE") {
		t.Fatal("private key export")
	}
}
