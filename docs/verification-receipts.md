# Verification receipts and issuer trust

PushGuard retains its existing private `SessionReport` evidence for diagnostics,
snapshots and rollback. PR delivery adds a separate minimal, portable
`receipt.VerificationReceipt` (version 1).

## Contents

- Canonical repository identity (`github.com/owner/repo`), exact commit and branch.
- Git tree hash, clean diff hash, effective committed configuration hash.
- UTC timestamp and PushGuard version.
- Named checks, redacted command, required flag, status, duration and tool/category.
- Applied AI repairs: provider/model, affected paths, hashed diagnostic IDs,
  patch hash, human approval and deterministic checks that verified the final state.
- Local state hash, stable receipt ID, delivery fingerprint, Ed25519 signature.

No patches, full prompts, raw logs, API keys, environment values or source are
included. Local checkout roots are replaced with `<repository>` in commands.
Legacy repair provenance is restored only when existing file hashes still match.

Portable delivery receipts also retain historical approved operations from intact
locally trusted receipts on the same branch's verified Git ancestry (the most
recent matching receipt within a bounded 100-receipt lookup). Later developer
edits therefore do not erase prior assistance. These records describe historical
operations and affected files, not a claim that current files are byte-for-byte
the original AI output. Current tools independently verify the new commit.

Receipt creation requires a clean committed checkout and an effective PushGuard
configuration that matches the committed config blob. User-only config cannot
be reconstructed remotely and is therefore insufficient for PR evidence.

## Hashing and signing

The ID is SHA-256 of canonical Go JSON fields, with ID, delivery fingerprint and
signature cleared. The delivery fingerprint hashes NUL-separated commit, tree,
diff, config, local-state hash and receipt ID. The signature is Ed25519 over a
versioned domain separator, receipt ID and fingerprint. Version/algorithm/key ID
are explicit so future signing/attestation schemes can coexist.

The private key is generated in the private PushGuard cache `signing/ed25519.key`,
not in the repository. Unix permissions and the existing protected Windows ACL
mechanism protect it. `pushguard pr key --json` exports only the public key.
Back up the signer through your normal secret-management process; deleting its
cache creates a new identity that maintainers must explicitly trust.

Receipts are stored by ID under the repository-scoped private cache and embedded
as a bounded base64 JSON block in PR metadata. They are not committed into the
tree they certify (which would create a circular commit identity problem).
`pushguard pr receipt --json` exports the latest delivery receipt.

## Validation states

| State | Meaning |
|---|---|
| `UNVERIFIED_RECEIPT` | Missing/unsupported/incomplete evidence, unknown issuer, unsigned claim, or invalid required-check evidence |
| `VALID_RECEIPT` | Trusted signature and exact repository/commit/branch/tree/config match |
| `TAMPERED_RECEIPT` | Digest, fingerprint or trusted signature mismatch |
| `STALE_RECEIPT` | Same PR branch has a different HEAD; verification is required again |
| `COMMIT_MISMATCH` | Different repository, commit, branch or tree |
| `CONFIG_MISMATCH` | Verified config differs from the commit's configuration |

The validator also checks bounded schema, timestamp plausibility, required check
success, unique check names and AI approval evidence. The hosted provider fetches
the actual commit/tree/config through GitHub; it does not trust the PR author's
description of those values. Symlinked/ambiguous config is rejected.

## What a valid receipt proves

A hash alone is not authentication. A self-signed receipt from an unknown key
remains **UNVERIFIED**. Maintainers provision allowed public keys out-of-band in
`PUSHGUARD_TRUSTED_KEYS` (a JSON map of SHA-256 key ID to base64 public key) or a
trusted file passed to the publisher. Trust is never loaded from PR content.

A trusted signature proves that a trusted issuer made an intact claim about
the exact commit. It cannot prevent a malicious trusted developer from lying
about their local machine. Independent hosted CI and repository policy are still
required. Remote-attestation hardware, Sigstore/OIDC attestations, revocation
services and organizational key enrollment are future extensions, not current
claims. Receipt authenticity is not a guarantee of semantic code correctness.
