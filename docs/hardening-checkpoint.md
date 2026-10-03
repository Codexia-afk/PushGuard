# Local verification and repair hardening checkpoint

## Audit and reproduced failures

This checkpoint builds on the existing controller, state machine, discovery,
diagnostics, proposal validator, approval ledger, snapshots and delivery gates.
It does not represent completion of the entire engineering milestone.

The current-code audit and new failing regressions established:

- Provider retries retained an investigated-state marker and were incorrectly
  rejected as oscillation. Decrementing attempts also defeated the retry budget.
- Standalone verification rejected a missing Git executable; standalone approval
  fingerprints missed newly added files.
- Required WARNING/SKIPPED/UNKNOWN outcomes escaped the blocking predicate.
- Cancellation could remain blocked waiting for terminal input.
- Python syntax checking imported a repository-local `json.py`, executing code
  while claiming to parse only.
- Nested malformed Node manifests could fall through to Git sanity; a workspace
  declaration incorrectly suppressed member checks without proof that they ran.

## Changes

Required checks now need PASS evidence. Python syntax checking uses isolated,
site-disabled interpreter startup and never executes application source.
Incomplete automatic discovery and malformed nested manifests fail closed;
Node member plans remain explicit. Missing configured TypeScript is a required
unavailable check. Configured AI credentials are withheld from verification.

Retries retain the attempt cap and release unapplied-state markers. Exact repeated
invalid proposals stop before further repair cycles. Prompt reads are cancelable,
and standalone sessions use the existing advisory-lock implementation. Ownership
metadata includes PID, session, start time, command and version. Metadata is
cleared on release; the empty advisory lock inode is deliberately retained to
avoid concurrent processes locking different inodes. The OS releases ownership
after process termination.

Context uses the session discovery index, resolves Python imports as read-only
evidence, preserves implementation focus lines during trimming, and separates
multiline implementation source from test evidence in the prompt. A structured
`CONTEXT_REQUIRED` response may request four source paths and four identifiers;
retrieval validates paths, reads at most four requested files, searches at most
200 indexed files/2 MiB, and respects the source and inference budgets. There are
at most two expansions per repair cycle, each requiring fresh investigation
permission. Neither requests nor provider recommendations execute commands.

Replacement candidates receive isolated Python compilation and, when the root
project's TypeScript compiler is installed, in-memory parsing and comparison of
new compiler diagnostics against the original project. Actual failed checks and
the full pipeline remain authoritative. Check-only repairs now have an explicit
review acknowledgment after fresh verification.

The macOS source installer builds on a fresh inode, ad-hoc signs and verifies the
staged binary, tests startup, and atomically replaces the installed executable.

## Regression coverage

New tests cover missing Git, new standalone files, retry recovery/caps, cancelled
approval prompts, required non-PASS outcomes, isolated Python syntax checking,
malformed/incomplete discovery, workspace-member checks, Python test imports,
bounded retrieval/path protection, fresh context permission, strict request
decoding, prompt separation and repeated invalid proposals. Existing tests cover
investigation/application denial, real reruns, partial/regressed repairs,
stale/active/crashed locks, post-review state changes, and check-only push guards.

## Validation and remaining acceptance

Validation on macOS arm64 with Go 1.26.5:

- `go test ./...` — passed; controller suite 129.778 seconds.
- `go vet ./...` — passed.
- `go test -race ./internal/ui ./internal/repolock ./internal/context ./internal/llm ./internal/verify` — passed.
- `go build -o bin/pushguard ./cmd/pushguard` and `bin/pushguard version` — passed.
- `sh -n scripts/install.sh` and `git diff --check` — passed.

The initial regression run reproduced the defects above. The full suite also
caught test transcripts needing the new explicit review response; those
transcripts now provide that decision without weakening assertions.

A new real-Ollama acceptance run is still pending for this checkpoint. Historical
model runs in other documents do not validate these changes. Nested TypeScript
candidate compiler selection, richer cross-language symbol resolution, and
comprehensive standalone repair without the Git executable remain follow-up
work. Ordinary standalone verification does not require Git; diff application
still uses `git apply`. Candidate semantic checks are not a substitute for real
verification and cannot establish general behavioral correctness.
