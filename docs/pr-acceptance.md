# PR-native acceptance evidence

Validated on 2026-10-03. The user explicitly authorized a new private disposable
repository, fixture commits/pushes, a test PR, Actions runs and Check publication.
No production repository was pushed and no PR was merged or deleted.

Private test PR:
<https://github.com/Codexia-afk/pushguard-pr-integration-20261003-051935/pull/1>

## Real local-to-hosted flow

The actual compiled `pushguard pr` binary ran against a disposable Node fixture.
The driver supplied visibly scripted developer decisions; model, ESLint,
TypeScript, Node tests, Git push, GitHub APIs and GitHub Actions were real.

1. ESLint reported a real `no-var` failure, classified CODE.
2. Groq `openai/gpt-oss-20b` proposed a source repair after investigation approval.
3. The exact diff passed policy/syntax checks and separate apply approval.
4. Real lint/typecheck passed; Node tests found an arithmetic failure.
5. A fresh investigation/apply cycle corrected the implementation.
6. The remaining build and complete pipeline passed.
7. Review and a separately approved tracked-repair commit ran, followed by fresh
   verification and review of the committed state.
8. A separate push gate sent the pinned feature commit through actual Git.
9. Separate PR approval created draft PR #1 with a signed receipt and two
   approved repair records affecting two files.
10. Actual GitHub Actions push and PR CI passed.
11. A trusted publisher built from the test repository's default branch validated
    the configured public key, signature, PR HEAD, tree and committed config.
12. **PushGuard Verification** was published with conclusion SUCCESS.

Initial verified commit: `6b6aa37ca6cee20f211a5c35de4d422405dfd0fa`.
Initial successful Check run: `111137396061`.

Live failures improved the implementation: publisher check aggregation initially
included its own job (a self-dependency); it now excludes jobs belonging to the
reserved trusted publisher workflow. The hosted workflow needs `statuses: read`
as well as checks/actions/contents/PR permissions. A fixture ignore rule initially
excluded publisher source; the test setup was corrected. None of these failures
were represented as successful hosted verification.

## Stale receipt, hosted failure and annotations

A new fixture commit `a5f658e425b6b15b385c2e3ba18803c507fbc38c` was intentionally
pushed without updating the prior receipt. `pr status` reported STALE_RECEIPT
and overall FAILURE while hosted CI was running.

Local checks were reverified, and a separately approved PR update supplied a new
signed receipt. A real hosted-only assertion then failed: the fixture's source
did not provide its required hosted greeting. The assertion emitted a GitHub
annotation and exited nonzero. The publisher reported:

- Local receipt: VALID_RECEIPT; lint/typecheck/tests/build PASS.
- Hosted CI: FAILURE.
- PushGuard Verification: FAILURE.
- Annotation: `src/example.ts:1:1`, hosted greeting requirement.

Failing PushGuard Check run `111138110470` contained four bounded annotations,
including the source diagnostic. The historical local receipt was retained.

The fixture source was then extended to satisfy the hosted contract while
preserving the original local greeting/test assertions. A new commit,
`cccea8d4003bcbd6c261390f2d5ebdd65e0d3ce3`, went through real local verification,
review, push approval and PR-update approval. The hosted assertion and both CI
runs passed. PushGuard Check `111139074183` transitioned to SUCCESS with a receipt
for that exact commit. Its `started_at` and later `completed_at` record the real
pending-to-completed lifecycle. The final `pushguard pr` invocation exited 0 and
reported readiness for human review. The PR remained open and draft.

This also exposed transient PR-index lag after Git push. The CLI now retries only
read operations while confirming the Git remote still points to the exact pushed
commit; it does not send another push or authorize a different state.

## Automated verification

Deterministic tests use `MockCodeHostProvider` and command-level `gh` fixtures,
plus temporary real Git repositories and bare remotes. Coverage includes
independent gates, denied repair/application/push/PR, approved repair and local
commit/reverification, current-branch targeting, state drift, receipt signature
tampering, stale/commit/config mismatch, unknown issuers, pending/pass/fail CI,
annotation/path conversion, read-only polling, provider auth/network/permission
errors, webhook authentication, JSON/CLI configuration, and no merge transition.

Required commands passed during implementation:

```sh
gofmt -w .
go vet ./...
go test ./...
go test -race ./internal/app ./internal/codehost ./internal/delivery ./internal/receipt
```

## Remaining deployment validation

The live test covers same-repository github.com PRs with GitHub CLI OAuth locally
and Actions `GITHUB_TOKEN` for the hosted publisher. It does not certify GitHub
Enterprise, fork PRs, organization-specific GitHub Apps, merge queues, every
ruleset, key-revocation policy, or Windows/macOS hosted deployment. Each target
repository must enroll trusted public keys, install reviewed publisher code,
configure expected check names, and require the correct Check integration in
branch protection. Existing Actions workflows are not modified automatically.
