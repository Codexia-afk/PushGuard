# Architecture

The Go control plane owns all authority. AI providers exchange structured analysis/proposals and cannot execute tools.

```text
CLI -> explicit workflow/state machine -> scoped approval ledger
         |-> repository discovery -> deterministic check selection
         |-> bounded command runner -> diagnostic adapters
         |-> minimized/redacted context -> optional provider
         |-> candidate validation -> approved snapshot/apply
         |-> failed check -> next check -> clean complete verification
         |-> live push-destination/object/LFS/resource preflight
         |-> verified fingerprint -> complete review -> final push authorization
         |-> fresh state/remote recheck -> pinned commit push -> actual Git result
```

`internal/app` checks every state transition and returns documented exit codes. Permissions are consumed one time and recorded with exact bindings. The final push cannot run without passing results, completed human review, passing preflight, a writable receipt, and current verified state.

The command runner uses argument arrays, bounded stdout/stderr, timeouts, direct stdin, process cancellation, and platform-specific process termination. Git uses installed executables; macOS can bypass an unusable license shim through an already installed working Git. Verification commands receive CI/non-color environment defaults, not model-generated commands.

Repository discovery parses NUL-delimited status including renames, resolves worktree Git directories, separates fetch and push URLs, respects pushRemote/pushDefault, and determines a destination branch independently of its local name. Initial outgoing history starts at the complete reachable HEAD, rather than a nonexistent tracking ref. Preflight reconciles that estimate with the live destination tip and inspects actual historical objects in a batch.

Fingerprints include explicit tracked bytes and modes, rather than trusting a stat cache. Missing Git output or file evidence is an error. Configuration bytes are covered even when ignored by Git. A pipeline that changes the repository cannot issue verification evidence for a mixed state. The outgoing commit itself is pinned in the executed refspec.

Snapshots and receipts live outside the repository so evidence cannot mutate its own fingerprint. Repair provenance is retained only while affected-file hashes match. AI edits are reviewed before the commit gate. The human may explicitly authorize a path-limited local commit (`git commit --only`) or commit separately and continue. The same session reruns verification on the committed tree and asks for a fresh review and push authorization. The provider has no commit capability.

`app/workflow.go` runs checks sequentially, pauses at each blocking failure, and restarts full verification whenever that phase needed a repair. `app/conversation.go` maps human questions to typed read-only actions; only explicit decisions create scoped approval grants. The state machine also guards sensitive transitions with state-specific authorization, and check-only machines reject commit/push states.

`diagnostics/annotations.go` decodes CI file/line/col properties and escaped values ahead of generic parsing. Tool evidence, failure classification, and AI root-cause analysis remain separate. `RepairAudit` retains denied as well as applied proposals with provider/model, patch hash, decisions, and actual verification results. Redaction is applied at context, provider transport, and receipt boundaries.

Repair attempts are counted session-wide (default five). Repeated patches, inverse edits, and recurring investigated states stop oscillation. Rollback restores only sealed repair paths and refuses to overwrite subsequent developer edits.

Source archives can verify explicitly configured or discoverable project checks. Their receipts explicitly omit a Git fingerprint. They cannot enter the push workflow.
