# Verification of the single-command workflow

Validated in the existing Hackspire1 checkout on macOS arm64, using Go 1.26.5.
The primary installed command is `pushguard push`.

## Jest context and interrupted-session regression (2026-10-03)

The PulseDesk reproduction exposed a parser/context failure before inference:
passing Jest test names containing “error” and failed-suite totals became
location-less diagnostics, and the first sorted repair group contained only
those summaries. Default Jest text output now produces one diagnostic per
failure block, preserving its test name, expected/received evidence and stack.
Located diagnostics take priority over location-less summaries.

Implementation discovery reads complete test imports, including multiline and
aliased imports outside the assertion window. Related implementation windows
retain the matched declaration location, and resolved imports take precedence
over broad substring guesses. Regression tests cover these cases and stable
diagnostic identity when an assertion moves to another line.

Repository ownership now uses nonblocking OS locks on macOS/Linux and Windows.
Subprocess tests confirm live-owner exclusion, automatic release after a killed
process, and recovery of an interrupted legacy PID lock. The reusable lock file
is retained to avoid unlink/recreation races.

Real local `qwen2.5-coder:7b` checks against PulseDesk reached inference with
three genuine failing-test diagnostics and editable analytics implementation
context. The model proposed assertion text as an implementation edit; exact
anchor validation rejected those proposals. **This verified context delivery
and rejection behavior, not a successful PulseDesk repair.** Its three failing
tests remained failing, and no proposal was applied. The PushGuard formatting,
vet, complete test suite and build checks passed after the fixes.

## Automated verification

- `gofmt` applied to modified Go files; `go vet ./...` and `go test ./...` passed.
- Exact-location tests cover the requested `::error file=...,line=...,col=...::`
  annotation, multiline rule messages, escaped filenames, Windows paths,
  TypeScript parentheses/colon forms, bare locations, stack frames, and ESLint
  stylish output.
- Temporary-repository tests prove no provider calls on investigation denial,
  no edits on patch denial, immediate real reverification, repeated approvals,
  ordered checks, full-pipeline failure blocking push, and check-only push-state
  rejection.
- HTTP integration exercises the configured Ollama adapter using a deterministic
  test endpoint. Normal tests require no paid provider or running model.
- Additional coverage includes infrastructure failures, protected security
  tests, repair limits, rollback preserving staged/untracked work, oscillation,
  state drift, commit denial, same-session approved commits, remote rejection,
  and actual Git push success to temporary local remotes.
- Typed replacement proposals are tested against actual `git apply`, including
  missing final newlines, empty results, ambiguity and protected paths.

## Disposable demonstrations

The labeled `all` demo passed repair-denial, patch-denial, two sequential
repairs, successful push, final-push denial, and stale-state scenarios. Its
provider is explicitly a mock, while Go checks and Git operations are real.

The separate **live** demo also completed using the real installed Ollama
`gemma3:4b` model:

1. A real Go test reported `arithmetic_test.go:7`: expected 5, received -1.
2. After scripted demo investigation approval, Gemma proposed changing the
   implementation's subtraction to addition. Go displayed the exact diff.
3. After separate apply approval, Go snapshotted and applied it.
4. Actual reverification found the next failure at `arithmetic_test.go:10`:
   expected 6, received 5.
5. Fresh investigation and apply approvals led to the multiplication repair.
6. The failed test, remaining build, and complete formatting/vet/test/build
   pipeline passed.
7. Git/resource preflight identified the uncommitted work. Both repair diffs
   were reviewed, and a separate local commit approval was consumed.
8. Verification ran again against the committed state, followed by preflight,
   review and independent push authorization.
9. The actual Git push to the disposable bare remote succeeded; the remote tip
   matched the verified commit. The fixture was then removed.

Earlier live trials correctly rejected malformed model responses and invalid
proposals without applying them. Those trials motivated schema-constrained
output, confidence normalization, and exact replacement proposals. A model's
claim or confidence never became verification evidence.

## Local installation

The updated executable is installed at `~/.local/bin/pushguard`, already on
PATH. User defaults now select the Groq budget profile described below; the
earlier Ollama runs used `gemma3:4b` at loopback with cloud context sharing
disabled. Repository configuration takes precedence.

Hosted CI jobs, deployment services, write permissions, and quotas are not
claimed to be verified when they cannot be observed locally.

## Node/ESLint real-provider acceptance

The installed binary was also exercised on a disposable copy of
`testdata/ai-node`, using Node 26, npm 11, actual ESLint 9.39.1 and TypeScript
5.9.3, and the real local Ollama `gemma3:4b` model. This was a scripted
acceptance driver feeding the ordinary `pushguard push` stdin, not a fake
terminal or mocked provider.

- Investigation denial: lint failed; no provider connection and no source edits.
- Patch denial: real analysis/proposal returned a two-file diff; denying apply
  left source identical to HEAD (`git diff --exit-code` verified it).
- Approved flow: two real `no-var` errors were repaired as one displayed batch.
  Actual `npm run lint` passed; `npm run typecheck` passed next.
- `npm run test` then failed on `add(10, 20)` returning -10 rather than 30.
  Fresh investigation and apply approvals produced an implementation repair.
  The test and `npm run build` passed afterward.
- The complete pipeline passed, then review and explicit commit approval ran.
  The committed state was reverified, reviewed and independently authorized
  for a real push to the disposable bare remote. The remote tip matched HEAD.

The run also exposed invalid model file metadata, collapsed multiline anchors,
and Node `test at`/`file://` location handling. Fixes derive affected files from
validated edits, ground local-model anchors in actual source bytes, and connect
ESM tests to imported TypeScript implementations. Invalid responses fail closed;
applied but incorrect repairs fail real checks and return to a fresh approval.

The small model's prose and edits were not always minimal: the successful test
repair also removed an unrelated exported constant. That deletion was visible
in the exact diff and was approved by the disposable test driver. Passing tests
prove the configured checks passed, not that all possible behavior is correct;
production users must inspect the diff rather than blindly approve model edits.

The original `error file` project's provider configuration now selects the real
configured model, and `pushguard ai status --test` reports service, model and
structured generation PASS. Its original static lint wrapper has subsequently
been replaced with real ESLint; see the update below and the
[AI repair guide](ai-repair.md). Other static wrappers remain, so the complete
project pipeline is not claimed to pass. Typecheck/build/test wrappers were
subsequently converted to genuine tools as described in the latest section.

## Groq budget-profile connection and repair verification

The configuration was subsequently switched to Groq `openai/gpt-oss-20b`, using
the Keychain-backed `GROQ_API_KEY`, 2048 maximum completion tokens, and low
reasoning effort. Both the user defaults and the `error file` project select
this profile. No credential is stored in repository configuration.

The installed CLI's live status test returned Service PASS, Model PASS and
Structured generation PASS. The OpenAI-compatible JSON-mode request now always
explicitly requests JSON, including during context-free probes.

A real Groq-backed `demo --scenario live` then repaired both arithmetic defects
in **one generation request**. The exact diff was displayed before the
scripted demo apply approval. The actual failed test passed, followed by the
remaining build, full formatting/vet/test/build pipeline, review, separately
approved local commit, fresh verification, independent push approval and a
successful real push to the disposable bare remote. The fixture was removed.

`go vet ./...` and the full Go test suite passed after the cost-profile changes.
Tests assert one combined generation request, output-budget transmission,
rejection of truncated output, and redaction of provider credentials/errors.

## Rejected-proposal recovery and genuine lint evidence

The reported `eslint-disable` proposal was correctly blocked by patch policy.
Provider instructions now explicitly require source repairs, even for
intentionally broken fixtures. A rejected proposal returns to investigation
with its reason recorded; another request requires new permission, and its
result requires independent application approval. Session limits still apply.

Regression tests exercise rejection followed by denial, EOF, corrected-patch
denial, and approved correction with actual reverification. They check that no
suppression reaches source and that the next request receives policy feedback.

The `error file` project's lint wrapper now calls actual ESLint with fixing
disabled. A disposable source fixture confirmed exit 1 with a real `no-var`
diagnostic, then exit 0 after changing `var` to `const`. The actual project
currently reports 71 ESLint/Prettier errors; the acceptance check left its source
files untouched.

Structured lint output can contain source literals such as “Missing required
environment variable”. Regression tests ensure these remain CODE diagnostics,
rather than false environment/network/resource failures. Actual runtime failures
on stderr or in termination metadata, as well as timeouts and truncated output,
still block repair.

After `go vet ./...` and `go test ./...` passed, the updated binary was installed
at `~/.local/bin/pushguard`. An installed-CLI check against the actual project
captured all 71 lint diagnostics as CODE and reached the investigation gate.
The acceptance driver denied investigation: exit code 3, no AI-generation or
application stage entered, and before/after source hashes matched. This run
validates the diagnostic/approval flow, not an AI repair of all 71 errors.

## Real local-repair reliability update

A real Groq run against a disposable copy of the user's failing project applied
a source patch, then stopped on HTTP 429: the next request exceeded the model's
8000-token-per-minute quota. This reproduced an execution blocker rather than
assuming the provider was disconnected. Smaller lint context and explicit,
cancellable retry-window recovery now address that path. Regression tests cover
retry approval, denial, EOF, cancellation and provider-header/body timing.

A later live proposal introduced mismatched quotes. Candidate TypeScript parsing
now runs in memory before application. A deterministic HTTP provider plus actual
ESLint and TypeScript exercised invalid proposal → rejection → new approval →
corrected proposal → apply → passing real lint and full verification. Only the
corrected patch reached source, and the tests were unchanged.

The real Groq `openai/gpt-oss-20b` then completed a separate disposable Node
acceptance run through the ordinary interactive `check` command with labeled
scripted decisions: approved source edits, actual lint/typecheck/test/build PASS,
and a complete clean verification pass. No test changes were needed. This is
evidence of real local editing and execution, not merely a connectivity probe.

Additional regressions cover bounded lint context, test-runner implementation
discovery, exact typed-parameter redaction, protected verification launchers and
dependency directories, and proposal targets bound to supplied editable files.

The original `error file` checkout now uses real TypeScript typecheck/build and
executes four actual test functions. Its health assertion makes a local HTTP
request and retains the expected 200 status. Initial real results were 23
compiler errors and four failing tests. These are genuine defects in the
deliberately broken example; its full application is not claimed to be repaired.

Final checks for this update: `go vet ./...`, `go test ./...`, and
`go test -race ./internal/app ./internal/llm` all passed. The verified backend was
installed at `~/.local/bin/pushguard`; the installed-binary denial test again
captured 71 CODE diagnostics in the actual project and preserved source hashes.
