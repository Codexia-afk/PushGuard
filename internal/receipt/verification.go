package receipt

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pushguard/pushguard/internal/config"
	"github.com/pushguard/pushguard/internal/model"
	"github.com/pushguard/pushguard/internal/repository"
	"github.com/pushguard/pushguard/internal/runner"
	"github.com/pushguard/pushguard/internal/security"
)

type VerifiedCheck struct {
	Name     string             `json:"name"`
	Command  string             `json:"command"`
	Status   model.ResultStatus `json:"status"`
	Required bool               `json:"required"`
	Duration time.Duration      `json:"duration"`
	Tool     string             `json:"tool"`
}

type AIRepairRecord struct {
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Files         []string `json:"files"`
	DiagnosticIDs []string `json:"diagnosticIds"`
	PatchHash     string   `json:"patchHash"`
	HumanApproved bool     `json:"humanApproved"`
	VerifiedBy    []string `json:"verifiedBy"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"keyId"`
	Value     string `json:"value"`
}

// VerificationReceipt is portable evidence. It excludes logs, patches, prompts,
// absolute local paths and environment values. A signature authenticates an
// issuer's claim; it is not a remote attestation of that issuer's machine.
type VerificationReceipt struct {
	Version          int              `json:"version"`
	Repository       string           `json:"repository"`
	CommitSHA        string           `json:"commitSHA"`
	Branch           string           `json:"branch"`
	TreeHash         string           `json:"treeHash"`
	DiffHash         string           `json:"diffHash"`
	ConfigHash       string           `json:"configHash"`
	Timestamp        time.Time        `json:"timestamp"`
	PushGuardVersion string           `json:"pushguardVersion"`
	Checks           []VerifiedCheck  `json:"checks"`
	AIRepairs        []AIRepairRecord `json:"aiRepairs"`
	LocalStateHash   string           `json:"localStateHash"`
	ID               string           `json:"id"`
	StateFingerprint string           `json:"stateFingerprint"`
	Signature        *Signature       `json:"signature,omitempty"`
}

func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (v VerificationReceipt) Hash() string {
	v.ID, v.StateFingerprint, v.Signature = "", "", nil
	data, _ := json.Marshal(v)
	return Digest(data)
}
func (v VerificationReceipt) Fingerprint() string {
	return Digest([]byte(strings.Join([]string{v.CommitSHA, v.TreeHash, v.DiffHash, v.ConfigHash, v.LocalStateHash, v.ID}, "\x00")))
}
func (v *VerificationReceipt) Sign(key ed25519.PrivateKey) {
	v.ID = v.Hash()
	v.StateFingerprint = v.Fingerprint()
	pub := key.Public().(ed25519.PublicKey)
	v.Signature = &Signature{Algorithm: "ed25519", KeyID: Digest(pub), Value: base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte("pushguard-receipt-v1\x00"+v.ID+"\x00"+v.StateFingerprint)))}
}

type Trust string

const (
	Unverified     Trust = "UNVERIFIED_RECEIPT"
	Valid          Trust = "VALID_RECEIPT"
	Tampered       Trust = "TAMPERED_RECEIPT"
	Stale          Trust = "STALE_RECEIPT"
	CommitMismatch Trust = "COMMIT_MISMATCH"
	ConfigMismatch Trust = "CONFIG_MISMATCH"
)

type Validation struct {
	Status Trust  `json:"status"`
	Reason string `json:"reason"`
}
type Expectation struct {
	Repository, CommitSHA, Branch, TreeHash, ConfigHash string
	Keys                                                map[string]ed25519.PublicKey
}

func ValidateVerification(v *VerificationReceipt, e Expectation) Validation {
	bad := func(status Trust, reason string) Validation { return Validation{status, reason} }
	if v == nil {
		return bad(Unverified, "No local receipt was supplied")
	}
	if v.Version != 1 || v.Timestamp.IsZero() || v.Timestamp.After(time.Now().Add(5*time.Minute)) || len(v.Checks) == 0 || v.PushGuardVersion == "" {
		return bad(Unverified, "Unsupported or incomplete receipt")
	}
	if v.ID != v.Hash() || v.StateFingerprint != v.Fingerprint() {
		return bad(Tampered, "Receipt digest or fingerprint does not match its contents")
	}
	if v.Repository != e.Repository {
		return bad(CommitMismatch, "Receipt belongs to a different repository")
	}
	if v.CommitSHA != e.CommitSHA {
		if e.Branch != "" && e.Branch == v.Branch {
			return bad(Stale, "PR HEAD changed; verification is required for the new commit")
		}
		return bad(CommitMismatch, "Receipt commit does not match PR HEAD")
	}
	if e.TreeHash == "" || v.TreeHash != e.TreeHash {
		return bad(CommitMismatch, "Receipt tree does not match the commit tree")
	}
	if e.ConfigHash == "" || v.ConfigHash != e.ConfigHash {
		return bad(ConfigMismatch, "Committed PushGuard configuration does not match the verified configuration")
	}
	if v.DiffHash != Digest(nil) {
		return bad(Unverified, "Receipt includes uncommitted changes")
	}
	if e.Branch != "" && v.Branch != e.Branch {
		return bad(CommitMismatch, "Receipt branch does not match PR head branch")
	}
	if v.Signature == nil || v.Signature.Algorithm != "ed25519" {
		return bad(Unverified, "A trusted signature is required; a self-hash is not proof of verification")
	}
	key := e.Keys[v.Signature.KeyID]
	if len(key) != ed25519.PublicKeySize || Digest(key) != v.Signature.KeyID {
		return bad(Unverified, "Receipt signer is not in the maintainer-controlled trust store")
	}
	sig, err := base64.StdEncoding.DecodeString(v.Signature.Value)
	if err != nil || !ed25519.Verify(key, []byte("pushguard-receipt-v1\x00"+v.ID+"\x00"+v.StateFingerprint), sig) {
		return bad(Tampered, "Receipt signature is invalid")
	}
	seen, required := map[string]bool{}, 0
	for _, c := range v.Checks {
		if c.Name == "" || seen[c.Name] || c.Command == "" {
			return bad(Unverified, "Incomplete or duplicate check evidence")
		}
		seen[c.Name] = true
		if c.Required {
			required++
			if c.Status != model.StatusPass {
				return bad(Unverified, "Required local check did not pass")
			}
		}
	}
	if required == 0 {
		return bad(Unverified, "No required checks were verified")
	}
	for _, repair := range v.AIRepairs {
		if !repair.HumanApproved || repair.PatchHash == "" || len(repair.VerifiedBy) == 0 {
			return bad(Unverified, "AI repair approval/verification evidence is incomplete")
		}
	}
	return Validation{Valid, "Trusted receipt matches repository, PR HEAD, tree and committed configuration; hosted CI is separate"}
}

func NewVerification(ctx context.Context, root, identity string, report model.SessionReport, r runner.Runner) (*VerificationReceipt, error) {
	if report.Repository == nil || report.Fingerprint == nil || len(report.Repository.Changes.All) > 0 || len(report.Checks) == 0 {
		return nil, fmt.Errorf("delivery receipt requires a clean, committed, verified repository")
	}
	svc := repository.Service{Runner: r}
	tree, err := svc.Git(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return nil, err
	}
	diff, err := svc.Git(ctx, root, "diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil || diff != "" {
		return nil, fmt.Errorf("delivery receipt cannot include a working-tree diff")
	}
	// Hosting must be able to reconstruct the effective config. User-only defaults
	// are intentionally insufficient for portable delivery evidence.
	committedHash := ""
	for _, name := range []string{".pushguard.json", ".pushguard.yaml", ".pushguard.yml"} {
		data, e := svc.Git(ctx, root, "show", "HEAD:"+name)
		if e != nil {
			continue
		}
		cfg, e := config.Decode([]byte(data), name)
		if e != nil {
			return nil, e
		}
		if committedHash != "" {
			return nil, fmt.Errorf("multiple committed PushGuard configurations")
		}
		committedHash = config.Hash(cfg)
	}
	if committedHash == "" || committedHash != report.ConfigHash {
		return nil, fmt.Errorf("PR delivery requires the effective .pushguard configuration to be committed and reverified")
	}
	v := &VerificationReceipt{Version: 1, Repository: identity, CommitSHA: report.Fingerprint.HEAD, Branch: report.Repository.Branch, TreeHash: strings.TrimSpace(tree), DiffHash: Digest([]byte(diff)), ConfigHash: report.ConfigHash, Timestamp: time.Now().UTC(), PushGuardVersion: model.Version, LocalStateHash: report.Fingerprint.Value}
	var verified []string
	for _, c := range report.Checks {
		if c.Check.Required && c.Status != model.StatusPass {
			return nil, fmt.Errorf("required check %s has not passed", c.Check.Name)
		}
		command := c.Command.Command
		if command == "" {
			command = runner.CommandString(c.Check.Args)
		}
		command = strings.ReplaceAll(command, root, "<repository>")
		v.Checks = append(v.Checks, VerifiedCheck{security.Redact(c.Check.Name), security.Redact(command), c.Status, c.Check.Required, c.Command.Duration, security.Redact(c.Check.Category)})
		if c.Status == model.StatusPass {
			verified = append(verified, c.Check.Name)
		}
	}
	for _, a := range report.RepairAudit {
		if !a.Applied || a.Proposal == nil {
			continue
		}
		repair := AIRepairRecord{Provider: security.Redact(a.Provider), Model: security.Redact(a.Model), Files: append([]string(nil), a.Proposal.Files...), PatchHash: a.PatchHash, HumanApproved: a.PatchDecision == "ALLOW", VerifiedBy: append([]string(nil), verified...)}
		for _, d := range a.Diagnostics {
			d.RawOutput = ""
			data, _ := json.Marshal(d)
			repair.DiagnosticIDs = append(repair.DiagnosticIDs, Digest(data))
		}
		v.AIRepairs = append(v.AIRepairs, repair)
	}
	key, err := SigningKey()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, repair := range v.AIRepairs {
		seen[repair.PatchHash] = true
	}
	for _, repair := range priorRepairs(ctx, root, identity, v.Branch, v.CommitSHA, key.Public().(ed25519.PublicKey), r) {
		if !seen[repair.PatchHash] {
			v.AIRepairs = append(v.AIRepairs, repair)
			seen[repair.PatchHash] = true
		}
	}
	v.Sign(key)
	return v, nil
}
