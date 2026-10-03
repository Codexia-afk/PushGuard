# Controlled-workflow implementation and evidence

| Requirement | Implementation | Evidence |
|---|---|---|
| One primary command | `pushguard push`; compact plan and Allow/Deny prompts | CLI tests and `demo --scenario all` |
| Same repair flow without push | interactive `pushguard check`; check-only state machine | configured HTTP-provider integration test; push/commit state rejection |
| Real discovery | repository and detector packages; npm/pnpm/yarn/bun, Go, Python, Rust, Java, .NET, Make/CMake indicators | manifest/package-manager and repository tests |
| Ordered checks and immediate repair | `app/workflow.go`, `repairLoop`, configured argument arrays | failed first check prevents running later checks; sequential-error tests |
| Deterministic evidence | ESLint JSON/stylish/CI annotations, TypeScript, Go JSON/compiler/vet, Jest/Vitest JSON, Ruff, Cargo, pytest/generic locations | `diagnostics/annotations_test.go`, parser fixtures and bounds tests |
| Exact reported source | file/line/column adapters, source caret, bounded repository reads | CI annotation regression matrix, stack/source tests |
| Separate investigation and apply gates | scoped one-use approvals and guarded state transitions | counted-provider denial tests; patch-denial filesystem assertions |
| Provider is proposal-only | real Ollama/OpenAI-compatible HTTP APIs; no terminal/filesystem capability | HTTP request integration, schema/privacy/redirect tests; clearly labeled demo mocks |
| Source context and privacy | source, related calls/symbols, diagnostic group, metadata, diff; transport and receipt redaction | context, security, audit-redaction tests |
| Safe patch application | path/symlink/binary/size policy, protected tests/configuration/secrets, suppression rejection, exact diff approval | patch policy/hardening tests; concurrent-edit rejection |
| Snapshot and rollback | sealed snapshots of affected files; original modes; no index reset | staged/untracked preservation, rollback conflict and repair-limit tests |
| Real reverification and full pass | failed command reruns, then remaining checks, then clean full pipeline | failed-reverification/new-approval and full-verification-failure tests |
| Attempts and oscillation | session-wide max attempts; duplicate/inverse-patch and state detection | limit and oscillation tests |
| Infrastructure/security handling | deterministic classification; no AI source repair for infrastructure; tests protected | environment and security integration scenarios |
| Git/resource preflight | live destination, history/objects, LFS, writable storage, known/unknown CI conditions | preflight tests and real temporary remotes |
| Complete review | approved diffs/reasons/results, details/files/logs/undo | repair review and conversation tests |
| One-session repaired push | optional explicit commit approval for tracked repair paths, then new verification | `TestSingleCommandCanCommitApprovedRepairsAndPush`; one-session demo |
| Independent push approval | actual pinned commit/ref/URL, current fingerprint, last remote/local checks | denial, actual Git success/rejection, alternate push URL tests |
| State drift invalidates approval | in-session local re-verification; changed policy/remote history blocks | stale-state tests and demo |
| Audit | diagnostics, provider/model, proposal/hash, allow/deny decisions, timestamp, real verification | receipts, JSON, audit and persistence-failure tests |

Hosted deployment, billing, secrets and runner availability are not executed or
claimed as verified. Providers with an OpenAI-compatible HTTP endpoint can use
that adapter; unrestricted Claude/Codex shell agents are deliberately not
exposed as command executors. The default test suite needs no AI service or paid
API. The CLI's local checks cannot certify unexecuted hosted CI jobs.
