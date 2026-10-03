# PushGuard

**Verify locally. Explain failures. Review approved repairs. Push only with human authorization.**

PushGuard is a Go engineering system that runs before `git push`. Its deterministic engine owns checks, Git inspection, diagnostics, approvals, snapshots, patch policy, verification receipts, and push execution. An optional AI provider can explain a failure and propose a minimal diff.

**AI proposes. Human authorizes. Tools verify. Policy decides. Git pushes.**

## PR-native verified delivery

`pushguard pr` extends the existing local engine through feature-branch push,
separately approved PR creation/update, signed verification receipts, hosted CI
observation and a trusted **PushGuard Verification** GitHub Check.

```sh
pushguard pr --title "Fix checkout calculation" --wait 5m
pushguard pr status
pushguard pr review 42
pushguard pr repair 42
```

Configure GitHub explicitly and use a feature branch. Local verification, AI
investigation, patch application, local commit, push and PR creation retain
independent human gates. Hosted CI remains independent of local success. A
separate maintainer-installed `pushguard-hosted` publisher validates receipt
signatures against trusted public keys and checks the actual PR commit/state.
**No automatic merge exists.** Existing local-only `check`/`push` remain useful.

- [GitHub setup and permissions](docs/github-integration.md)
- [PR workflow and commands](docs/pr-workflow.md)
- [Receipt format and trust](docs/verification-receipts.md)
- [Hosted Check publisher and Actions integration](docs/github-checks.md)
- [Verified-delivery security model](docs/security-model.md)
- [Real GitHub acceptance results](docs/pr-acceptance.md)

## Real AI repair: enable it once, then use one command

**[AI repair setup, configuration, troubleshooting and provider integration guide →](docs/ai-repair.md)**

PushGuard includes real **Ollama** and **OpenAI-compatible HTTP** repair
providers. It does not inherit your IDE agent automatically. A repository
configuration with `"ai": {"provider": "none"}` disables AI even when your
user-wide configuration has a model. The CLI now displays which configuration
was selected.

Set the existing project's `ai` section (keep its other settings):

```json
"ai": {
  "enabled": true,
  "provider": "ollama",
  "model": "YOUR_INSTALLED_MODEL",
  "endpoint": "http://127.0.0.1:11434",
  "localFirst": true
}
```

Check setup with `pushguard ai status --test` (a real, context-free API request).
For daily work, run **`pushguard push`**. It asks before investigation, before
applying each displayed diff, and separately before the actual Git push.
`pushguard doctor` also reports AI service/model status. Verification remains
available without a model.

For lower-cost cloud repair, the guide includes a **Groq GPT-OSS 20B profile**.
Built-in HTTP providers combine analysis and proposal into one generation
request per repair cycle, with a configurable output-token cap and no automatic
paid retries. The current local installation uses **Qwen Coder 7B through native
Ollama**, with explicit input/output budgets and bounded diagnostic groups.
See [local setup, Pi and measured large-error acceptance](docs/ollama.md).
In that real run, lint diagnostics decreased **50 → 35** before the six-attempt
limit; the repository remained **FAIL**, and no push occurred.

## Install

Install the latest published binary on macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/Codexia-afk/Hackspire_project/main/scripts/install.sh | sh
```

Install it on Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/Codexia-afk/Hackspire_project/main/scripts/install.ps1 | iex
```

The installers download the platform archive from the project's GitHub
Releases, verify `checksums.txt`, install into a user-local directory, and
never require administrator/root access. Set `PUSHGUARD_RELEASE_BASE_URL` to a
company mirror that serves the same HTTPS assets. `PUSHGUARD_RELEASE_VERSION`
can select a specific release instead of `latest`.

If you are working from the source checkout, use the explicit source-install
mode:

```sh
PUSHGUARD_INSTALL_FROM_SOURCE=1 sh scripts/install.sh
```

Requirements for a source installation: **Go 1.22+ and Git**. On macOS 26 or newer, use **Go 1.24+** (current stable recommended); older Go binaries lack the UUID required by the macOS loader. The installers verify the Go version before building. The Unix source installer builds to a fresh staging file, ad-hoc signs and verifies it on macOS, tests startup, and atomically replaces `~/.local/bin/pushguard` (or `PUSHGUARD_INSTALL_DIR`). It also embeds the source commit and build date. A direct `go install` instead uses `GOBIN`, or `GOPATH/bin` when unset; add the chosen directory to `PATH`. A packaged binary does not require Go to run the CLI; checking a Go project still requires its Go toolchain.

Convenience installers are included:

| Platform | Command from this folder |
|---|---|
| macOS / Linux source checkout | `PUSHGUARD_INSTALL_FROM_SOURCE=1 sh scripts/install.sh` |
| Windows PowerShell source checkout | `$env:PUSHGUARD_INSTALL_FROM_SOURCE='1'; powershell -NoProfile -ExecutionPolicy Bypass -File scripts/install.ps1` |

The Unix installer uses `~/.local/bin` and prints the command to add it to the current terminal's PATH. The Windows installer uses `%LOCALAPPDATA%\PushGuard\bin` and updates the user's PATH. Set `PUSHGUARD_INSTALL_DIR` to choose another directory.

For a manual install, download the matching archive and `checksums.txt` from
the release, verify the archive before extracting it, then place `pushguard`
or `pushguard.exe` on PATH. For example on macOS Apple Silicon:

```sh
curl -fLO https://github.com/Codexia-afk/Hackspire_project/releases/download/v0.3.0/pushguard-darwin-arm64.tar.gz
curl -fLO https://github.com/Codexia-afk/Hackspire_project/releases/download/v0.3.0/checksums.txt
grep 'pushguard-darwin-arm64.tar.gz$' checksums.txt | shasum -a 256 -c -
tar -xzf pushguard-darwin-arm64.tar.gz
install -m 0755 pushguard "$HOME/.local/bin/pushguard"
```

Build shareable, dependency-free binary archives for every supported platform using Go 1.24+ (current stable recommended):

```text
go run ./cmd/release
```

The command creates macOS amd64/arm64, Linux amd64/arm64, and Windows amd64/arm64 packages, with `SHA256SUMS`, in `dist/`. Extract the package for the laptop's OS and CPU, then place `pushguard` / `pushguard.exe` in a directory on PATH. These packages can be handed to judges. There is no published module or download URL assumed by this repository.

```text
pushguard version
pushguard doctor
```

PushGuard resolves a working installed Git on macOS when `/usr/bin/git` is an unusable Xcode shim. It does not accept licenses or change system configuration.

If macOS prints `zsh: killed` even for `pushguard version`, inspect the recent
`pushguard-*.ips` reports in `~/Library/Logs/DiagnosticReports`. A report naming
`SIGKILL (Code Signature Invalid)` and `CODESIGNING` identifies an executable
signing failure before PushGuard starts. Reinstall from the source checkout
with `PUSHGUARD_INSTALL_FROM_SOURCE=1 sh scripts/install.sh`; its staged install
avoids rewriting the executable in place. This does not require disabling
Gatekeeper or SIP. `pushguard version` should then show the installed commit and
build date.

To uninstall a user-local installation, remove the executable and its PATH
entry: `~/.local/bin/pushguard` on Unix, or
`%LOCALAPPDATA%\PushGuard\bin\pushguard.exe` on Windows. Repository receipts
and snapshots are stored separately; remove the PushGuard cache directory only
if you also want to delete recorded evidence.

## One command: check, repair, verify, and ask before pushing

Run this inside any Git project (or one of its subdirectories):

```sh
pushguard push
```

The compact CLI discovers the project's actual scripts and package manager,
shows the plan, and asks **Allow / Deny**. Required checks run in order. A
failure immediately shows deterministic evidence and asks permission for AI
investigation. The provider returns a proposal; PushGuard displays the exact
diff and asks a separate **Apply / Deny** question. An approved patch is
snapshotted, applied, and verified by rerunning the failed command. Every new
failure requires fresh approval.

Rejected proposals (including `eslint-disable` suppressions) return to an
explicit corrected-proposal gate. Nothing is applied; a new AI request needs
fresh permission and still counts toward the repair-attempt limit.

Local repair uses bounded context, actual verification feedback between attempts,
and in-memory TypeScript syntax validation when the project compiler is installed.
Provider rate limits offer an explicitly approved, cancellable wait/retry in the
same session. See the [real execution evidence](docs/workflow-verification.md).

Once individual checks pass, the complete pipeline runs again. Git/resource
preflight and a change review follow. If tracked repairs need committing, you
can review and explicitly authorize their local commit **inside the same
session**; PushGuard then re-verifies the committed state. Finally it asks
**ALLOW PUSH / DENY**. No second PushGuard invocation is needed.

Use `pushguard check` for the same interactive repair loop with no commit or
push capability. `allow`, `deny`, `y`, and `n` are accepted. Ask for code, logs,
the diff, an explanation, rollback, or remaining checks at the repair gates.
Empty input, EOF, and unrecognized answers never grant approval.

Only locally executable checks are certified. Hosted deployment jobs, remote
secrets, runner capacity, and billing/quota cannot be proven locally and are
reported as UNKNOWN or SKIPPED. Configure additional project checks in
`.pushguard.json` when discovery does not cover your CI commands.

### Noninteractive verification

```text
pushguard ready --non-interactive
```

This runs **every configured or discovered check**, reports all failures, and returns a nonzero exit code when required verification fails. `check --non-interactive` provides the same read-only automation behavior. A source archive without `.git` can run project checks; its report explicitly contains no verified Git state or push target.

Before installation, from this project folder:

```text
go run ./cmd/pushguard ready --non-interactive
```

Use the following distinction when working with Git:

```text
# Verify everything without changing files or pushing:
pushguard ready --non-interactive

# Run the complete approved workflow, including Git preflight, review,
# final authorization, and the exact verified push:
pushguard push
```

`pushguard push` is the normal command for a guarded push. It never creates
commits automatically: committing reviewed tracked repair files requires its
own human approval. You can also commit using normal Git and choose to
reverify in the current session. If you intentionally
keep the final `git push` outside PushGuard, run `pushguard ready
--non-interactive` immediately before that command; `make pre-push` is an
equivalent source-tree gate.

For this Go project, the pipeline checks formatting without editing files, runs `go vet`, runs all unit and integration tests, and builds every package. This includes real Git workflow tests, approval denial, repair loops, rollback, state drift, bad remotes, large historical objects, and remote rejection. Test fixtures and AI responses are deterministic; no Ollama service or cloud credentials are needed.

CI uses the same command. The workflow checks Linux and Windows with Go 1.22 and stable, Go 1.22 on macOS 15, and stable Go on the latest macOS, runs the race detector on Linux, exercises the demos, and produces all platform archives. Hosted CI also needs its runner and checkout; local verification cannot prove hosted quota, secrets, database availability, or billing state.

For automation:

```text
pushguard ready --json
```

JSON is a single versioned report on stdout. Exit codes remain meaningful. A JSON or noninteractive `push` runs verification and preflight, then stops before human review/authorization; it cannot repair or push, and does not return a successful push result.

## Judge demonstration

Run the complete demonstration on any supported OS with Go and Git:

```text
pushguard demo
```

Or run directly from this folder:

```text
go run ./cmd/pushguard demo
```

The demo creates a temporary repository and a **local bare remote**. It uses scripted demo decisions and deterministic proposal responses, with real Go checks and real Git. It demonstrates:

1. An arithmetic implementation fails a legitimate test; the tool reports the test's exact location.
2. AI analysis identifies a separate likely implementation cause.
3. Investigation/proposal and application each require their own approval; the diff is displayed first.
4. A snapshot is taken and the failed check runs again.
5. A second error appears; investigation and application are approved again.
6. The complete pipeline runs after repair and both diffs are reviewed.
7. The **demo developer** explicitly approves committing the displayed repair diff.
8. The same PushGuard session verifies the committed state and requests separate final push authorization.
9. The remote receives the exact verified commit.

The demo is visibly labeled **DEMO MODE** and uses a mock provider. Production uses the configured real Ollama or OpenAI-compatible HTTP provider. Scripted decisions exist only in `demo`; ordinary `push` reads human input.

Additional safety demonstrations:

```text
pushguard demo --scenario pass
pushguard demo --scenario deny
pushguard demo --scenario repair-deny
pushguard demo --scenario patch-deny
pushguard demo --scenario stale
pushguard demo --scenario all
```

`deny` confirms that declining final authorization leaves the remote unchanged. The repair/patch denial scenarios assert that source stays unchanged and that denial of investigation invokes no provider. `stale` simulates an edit after review, invalidates authorization, reruns checks, and blocks the uncommitted state. Demo repositories and their evidence are removed on completion.

`pushguard demo --scenario live` exercises the configured **real** provider on
the same disposable fixture, with visibly scripted demo decisions. It is
separate from `all` so automated tests/demos never need an AI service. The live
scenario still uses actual checks, patch policy, review, commit approval, and
the real Git push to a local bare remote.

## Push a real project

From a committed Git repository with a configured remote:

```text
pushguard push
```

Or select a project explicitly:

```text
pushguard push --repo /path/to/project
```

The workflow is:

```text
Discover -> Start consent -> Check -> Diagnose -> Investigation consent
-> AI explanation/proposal -> Validate -> Show diff -> Apply consent
-> Snapshot -> Apply -> Verify failed check -> Next check -> Complete verification
-> Git/resource preflight -> State check -> Review -> Final authorization
-> State and remote recheck -> Push verified commit -> Report actual result
```

The CLI prints compact results. Context, raw logs, explanations, diffs, changed files, and preflight details are available through review choices. Terminal output uses textual status markers, supports pipes and screen readers, strips terminal escape commands, and wraps menus. Every command supports `--help`; unknown flags and unexpected arguments fail.

When a repair is applied, review it and explicitly approve the optional local commit, or commit it yourself and continue the same session. The built-in commit is limited to displayed tracked repair paths; unrelated staged work is preserved. New untracked repair files require developer staging. Receipts retain repair reasons, diagnostics, diffs, provider/model, decisions (including denial), patch hashes, verification results, and affected-file hashes. Local edits block push because Git transmits commits; passing checks on uncommitted content cannot certify a different committed tree.

The final operation names a **literal push URL, verified commit hash, and exact destination ref**. This handles a differently named upstream, a separate push URL, and concurrent HEAD changes. Force pushes, remote deletion, arbitrary refspecs, and automatic history rewriting are not exposed by this CLI.

## Commands

| Command | Purpose |
|---|---|
| `pushguard ready` | Noninteractive checks; no repair or push |
| `pushguard check` | Interactive verification/approved repair; never commit or push |
| `pushguard push` | Complete controlled workflow |
| `pushguard init` | Create safe configuration; refuses existing JSON or YAML |
| `pushguard doctor` | Probe Git, project tools, configuration, AI endpoint policy, evidence storage |
| `pushguard ai status` | Show selected provider/configuration and catalog availability; `--test` checks a real structured response |
| `pushguard status` | Branch, destination, outgoing changes, receipt freshness |
| `pushguard review` | Outgoing tree diff, staged/unstaged diff, untracked names, repair provenance |
| `pushguard explain` | Explain the latest recorded diagnostics and repairs |
| `pushguard rollback` | Confirm and restore the latest sealed repair snapshot |
| `pushguard rollback --all` | Restore recorded repair snapshots in reverse order |
| `pushguard config` | Show effective configuration |
| `pushguard version` | Show the installed version |
| `pushguard demo` | Exercise the real workflow in disposable repositories |

Use `--repo PATH` for another project and `--plain` for plain output. `ready`, `check`, and `push` accept `--non-interactive` and `--json`. `doctor`, `status`, `config`, `review`, and `explain` also offer JSON. Rollback in noninteractive mode always refuses modification.

## Configuration and check discovery

`pushguard init` creates `.pushguard.json`. JSON is the canonical format; `.pushguard.yaml` and `.pushguard.yml` support the documented two/four-space scalar/list subset. Unknown fields, malformed commands, duplicate check names, invalid retry counts, unsupported providers, and attempts to disable mandatory safety gates are rejected.

```json
{
  "version": 1,
  "checks": [
    {"name": "lint", "category": "lint", "args": ["npm", "run", "lint"], "required": true},
    {"name": "types", "category": "typecheck", "args": ["npm", "run", "typecheck"], "required": true},
    {"name": "tests", "category": "test", "args": ["npm", "test"], "required": true},
    {"name": "build", "category": "build", "args": ["npm", "run", "build"], "required": true}
  ],
  "repair": {"enabled": true, "requireGenerationConsent": true, "requireApplyApproval": true, "maxAttempts": 5},
  "ai": {"provider": "none", "model": "", "endpoint": "http://127.0.0.1:11434", "localFirst": true},
  "review": {"requiredAfterAIChanges": true},
  "push": {"requireExplicitConfirmation": true, "invalidateConfirmationOnStateChange": true},
  "preflight": {"git": true, "remote": true, "lfs": true, "resource": true, "repositoryPolicy": true},
  "privacy": {"redactSecrets": true, "allowRemoteAI": false}
}
```

Argument arrays are recommended. A `command` string is tokenized without shell expansion; quoted Windows executable paths and empty arguments are preserved. If a project genuinely needs a shell, configure that executable and its arguments explicitly. Models can only return proposals; they cannot add commands to the pipeline. Configured commands and package scripts are project code and execute with the current user's permissions.

For user-wide defaults, create `config.json` under the platform's user
configuration directory: `$XDG_CONFIG_HOME/pushguard/config.json` or
`~/.config/pushguard/config.json` on Linux, `~/Library/Application Support/pushguard/config.json`
on macOS, and
`%APPDATA%\pushguard\config.json` on Windows. A repository-level
`.pushguard.json` takes precedence. `PUSHGUARD_CONFIG_FILE` can point to an
explicit managed configuration file. The same strict schema is used, so
global defaults can define an AI provider, standard checks, and privacy
preferences without copying PushGuard into every repository.

When `checks` is empty, discovery uses actual manifests:

- Node: existing `lint`, `typecheck`, `type-check`, `check:types`, `test`, `build`, `security`, `audit`, `format:check`, `check`, and noninteractive `lint:*`/`typecheck:*`/`test:*`/`build:*` scripts, using the `packageManager` field before npm/pnpm/yarn/bun lockfiles. Watch/fix/update/deploy/publish variants are not auto-selected.
- Go: `gofmt -l .`, `go vet ./...`, `go test -json -count=1 ./...`, `go build ./...`.
- Python: Ruff/mypy tool configuration and pytest configuration/test directories.
- Rust: Cargo check, Clippy, tests, and build; compiler messages use JSON where available.
- Java: Maven verify or Gradle check/build from their manifests.
- .NET: test and build when a root project file exists.
- Make-based projects: safe `lint`, `typecheck`, `check`, `test`, and `build` targets when no stronger language manifest owns the checks.

Workspace metadata such as npm/pnpm/Yarn workspaces, Turborepo, Nx, and
`go.work` is reported. PushGuard does not guess package-specific commands from
changed files; configure those commands explicitly when root verification is
not authoritative. CI workflow files are scanned for local-compatible command
steps for display, while deployment, publishing, production, and other
remote-only operations are reported without executing them.

Unknown Git projects get only Git whitespace sanity; configure their actual checks to certify builds/tests. Source archives without discoverable checks require explicit configuration. Checks run with `CI=true` and `NO_COLOR=1` to avoid interactive watch defaults. Required checks are the default; optional checks must be explicitly marked `required: false`. Optional failures remain visible warnings.

Security scanners can be added as required checks using the scanner installed for that project. PushGuard never claims a security scan ran merely because a language was detected.

## Local AI and privacy

AI is off by default. For an installed Ollama model, edit the AI section:

```json
{"provider":"ollama","model":"your-installed-model","endpoint":"http://127.0.0.1:11434","localFirst":true}
```

For LM Studio or llama.cpp with an OpenAI-compatible local API:

```json
{"provider":"openai-compatible","model":"your-model","endpoint":"http://127.0.0.1:1234/v1","localFirst":true}
```

An explicitly approved remote OpenAI-compatible provider can use an HTTPS
endpoint such as Groq's API. Keep the credential outside the repository and
export it only in the shell that runs PushGuard:

```sh
export PUSHGUARD_AI_API_KEY='your-provider-key'
```

The endpoint must be configured with `privacy.allowRemoteAI: true` and
`ai.localFirst: false`. PushGuard never writes this environment credential to
configuration, reports, or repair patches.

The provider receives a bounded, redacted diagnostic, nearby source window, relevant diff, and a few candidate related definitions. Reported error locations and likely root causes remain distinct. Source reads cannot escape through symlinks or read sensitive files.

Providers can propose a unified diff or typed exact replacements. For local
models, Ollama's structured-output schema names only eligible source paths;
test files remain read-only evidence. Go converts unique `oldText`/`newText`
replacements into the exact unified diff **without writing files**. Ambiguous
anchors, invalid proposals, protected paths, and malformed diffs are rejected.
The same diff validation and human apply gate follow either representation.

Only loopback addresses/localhost are allowed by default. Local requests ignore HTTP proxies; redirects are refused. A remote HTTPS endpoint requires both `privacy.allowRemoteAI: true` and `ai.localFirst: false`, and the analysis prompt explicitly names the remote destination before consent. `PUSHGUARD_AI_API_KEY` is an optional credential for that explicitly configured remote. It is never written to configuration or receipts.

At each repair gate you can request evidence/code/logs, an explanation, the diff, remaining checks, or rollback. Investigation approval authorizes analysis and proposal generation for that failure group only; it does not authorize application. The exact diff needs its own approval. A real model is not needed for automated tests or the labeled demo.

## Trust and recovery

- Investigation/proposal, application, review, optional local commit, and push approvals have separate scopes and one-use bindings. Each new repair cycle asks again.
- The state machine rejects illegal transitions; failed checks cannot lead directly to pushing.
- Patches reject traversal, symlink targets/parents, secrets, tests/fixtures, manifests, CI/configuration/policy files, binary patches, renames, mode changes, malformed hunks, excessive size, and excessive file count.
- Snapshots precede edits, preserve affected-file contents/modes, and seal post-apply hashes. Rollback refuses unrelated subsequent edits. It never runs `git reset --hard`.
- An OS-owned repository lock stops concurrent interactive PushGuard check/push sessions and releases automatically when a process exits, including after a crash. `.git/pushguard.lock` remains as a reusable lock file; its existence alone does not block a session. Interrupted legacy PID locks are recovered only when their process is no longer running. Do not delete the lock file while a session is active.
- Fingerprints cover HEAD, local Git configuration, index, tracked content/modes, untracked content, submodules, and on-disk PushGuard configuration. Missing evidence blocks the workflow. Edits after review invalidate authorization and re-run verification in-session (bounded to three restarts). Changed verification policy or changes made by a running check require a fresh plan/session.
- Preflight inspects the live **push destination**, fast-forward history, every outgoing object including an initial push, estimated uncompressed bytes, unfinished Git operations, working-tree cleanliness, LFS objects/dry-run, local writable storage, and available CI indicators.
- Remote permissions, protected-branch policy, hosted CI capacity, and LFS billing quota are labeled **UNKNOWN** when not observable. They are never fabricated and never trigger source edits.
- Command output is capped at 2 MiB per stream and 16 MiB per verification cycle. Normalized diagnostics are limited to 100 per check; full captured logs remain available in cycle receipts. Incomplete evidence fails verification. Timeouts cancel child processes; source/context/snapshot sizes are bounded.
- Receipts are repository-scoped, versioned, redacted records outside the worktree. They contain session ID, HEAD/fingerprint, configuration hash, PushGuard version, checks/logs/results, repair history, approval events, review completion, preflight, and actual push results. Freshness is recomputed, not assumed.

Historical summaries contain log previews; `logArtifacts` lists immutable cycle receipts with complete captured output. Receipts have a 32 MiB storage limit. Unix evidence directories use mode 0700; Windows uses a protected user ACL through native Windows security APIs.

Evidence defaults to the user's cache directory. Set `PUSHGUARD_CACHE_DIR` to a writable directory for a sandbox or isolated harness. A receipt-storage failure stops a push before execution.

## Optional pre-push hook

In a repository without an existing PushGuard configuration:

```text
pushguard init --hook
```

The installer preserves existing hooks. Its lightweight gate requires a matching verification receipt from the last 15 minutes and restricts pushed source refs to the verified HEAD. Keep `pushguard` on PATH. The primary workflow remains `pushguard push`; the hook checks evidence and grants no push authorization.

## Exit codes

| Code | Meaning |
|---:|---|
| 0 | Requested verification/operation completed |
| 1 | Required verification failed |
| 2 | Configuration, tool, or evidence-storage error |
| 3 | Human authorization missing/denied or operation interrupted |
| 4 | Git/preflight blocker |
| 5 | Repair limit reached |
| 6 | Git push did not report success |
| 7 | AI unavailable or invalid response |
| 8 | Patch/snapshot/rollback rejected |
| 9 | Verified repository or remote state changed |

## Development

```text
go run ./cmd/pushguard ready --non-interactive
go test -count=1 ./...
go test -race -count=1 ./...
go build -trimpath -o bin/pushguard ./cmd/pushguard
go run ./cmd/release
```

`make ready` / `make check` invoke complete verification. `make fmt` is the explicit formatting command; verification does not format source. On a sandboxed machine, set `GOCACHE` and `PUSHGUARD_CACHE_DIR` to writable directories.

See [architecture](docs/architecture.md), [security](docs/security.md), [testing](docs/testing.md), and [master-prompt traceability](docs/requirements.md). Compilation targets are supported; actual native runtime coverage is supplied by CI. Arbitrary project tooling, network services, and AI model quality remain prerequisites of the project being checked.
