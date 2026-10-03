# Set up real AI repair

**Daily command: `pushguard push`.** It discovers checks, explains failures,
asks before AI investigation, shows the exact proposed diff, asks before
application, reruns real tools, reviews repairs and separately asks to push.
`pushguard check` uses the same repair loop but cannot commit or push.

For native local setup, enforced context budgets, grouping, Pi, and measured
large-error results, see **[Local Ollama repair](ollama.md)**.

## Why “AI provider unavailable” appeared

The failing project's `.pushguard.json` contained:

```json
"ai": { "provider": "none", "model": "" }
```

That explicitly selects the disabled provider. The real Ollama HTTP adapter
was already present; this message did **not** mean it had contacted Ollama and
failed. Repository configuration takes precedence over user-wide configuration,
including when the repository says `none`. PushGuard respects that setting.

The updated CLI shows the selected configuration and provider at startup, in
`pushguard doctor`, and in `pushguard ai status`. New `pushguard init` files
inherit the currently selected user configuration instead of replacing a
configured provider with `none`.

## 1. Install and start a model service once

Install [Ollama](https://ollama.com/download) and open the application, or start
its service with `ollama serve`. Use `ollama list` to see installed model names.
If needed, download a code-capable model with `ollama pull MODEL_NAME`.

Use the **exact installed name and tag**. Model choice is configurable: Gemma,
Qwen coder models and other Ollama-supported models are not special-cased.
A smaller model uses less memory but may produce invalid or incomplete repairs;
PushGuard rejects unsafe proposals and verifies every applied change.

On the machine used for this implementation, Ollama already runs at
`http://127.0.0.1:11434` and `qwen2.5-coder:7b` is installed. No API key is needed for
this local setup.

## 2. Enable it in the configuration actually selected

Add or replace only the `ai` section of your project's `.pushguard.json`,
preserving existing checks and other settings:

```json
{
  "ai": {
    "enabled": true,
    "provider": "ollama",
    "model": "qwen2.5-coder:7b",
    "endpoint": "http://127.0.0.1:11434",
    "localFirst": true
  },
  "privacy": {
    "redactSecrets": true,
    "allowRemoteAI": false
  }
}
```

Replace `qwen2.5-coder:7b` with your installed model. `base_url` is accepted as an alias
for `endpoint`; specify one, not both. Omitting `enabled` retains compatibility
with older configurations. `enabled: false` or `provider: none` disables AI.

For a default shared across projects, place the same JSON in:

| Platform | User configuration |
|---|---|
| macOS | `~/Library/Application Support/pushguard/config.json` |
| Linux | `$XDG_CONFIG_HOME/pushguard/config.json` or `~/.config/pushguard/config.json` |
| Windows | `%APPDATA%\pushguard\config.json` |

`PUSHGUARD_CONFIG_FILE` selects another user-default configuration file. A
repository `.pushguard.json`, `.pushguard.yaml` or `.pushguard.yml` wins over
that default as a whole; configurations are not implicitly merged. Explicit
repository disabling is never overridden automatically.

Earlier repair work used the Groq profile below on a separate broken project.
The implementation machine's user default now selects local Qwen; repository
overrides still apply.

**Verification in that intentionally broken fixture:** `scripts/run-linter.js`
originally printed 11 hard-coded errors and always exited 1. It has been replaced
with actual ESLint checks against current TypeScript source, preserving the
existing rules with TypeScript-aware unused-variable analysis and Prettier.
It emits structured diagnostics without full source-file copies and never
auto-fixes source. Its first real run found 71 current ESLint/Prettier errors;
the failure count now follows the source rather than a demonstration list.

The project's typecheck and build wrappers now invoke TypeScript with the
existing strict options. Its test wrapper executes all four existing assertion
functions, and the server test observes actual HTTP rather than a hard-coded
response. The explicit local plan runs lint, typecheck, test and build once each.
Security/Docker/deployment scripts remain demonstration-only; see that project's
`VERIFICATION.md`. The repair agent cannot change check launchers or test
expectations to claim success. `testdata/ai-node` is a smaller real-tool fixture.

## 3. Confirm readiness once

```sh
pushguard ai status --test
```

This reports the selected configuration, endpoint and model. It verifies the
service's model catalog, then explicitly requests and parses a small structured
response **without sending repository source**. It only says generation passed
after that real request succeeds.

`pushguard ai status` and `pushguard doctor` perform catalog checks without
generation; generation is correctly labeled UNKNOWN until exercised. Both
`ai status` modes support `--json` and `--repo PATH`.

## 4. Use one command for the project workflow

```sh
pushguard push
```

Example transitions (not a guarantee of model repair quality):

```text
FAIL lint — src/example.ts:1:1 — no-var
Allow Fix? > allow
AI provider connected: Ollama / configured-model
Analyzing verified diagnostics...
Analysis complete

- var message = "hello";
+ const message = "hello";

No proposed changes applied. Apply? > allow
PATCH APPLIED
Re-running npm run lint
PASS lint
Continuing verification...
```

If a later test fails, PushGuard asks again. If all checks pass, the complete
pipeline reruns. Git/resource preflight and the full repair review follow.
Tracked repairs can be committed locally **only with a separate displayed-diff
approval**, then reverified, within this command. Finally, **ALLOW PUSH / DENY**
controls the actual push. Denying any gate stops the session.

If a proposal adds `eslint-disable`, changes protected verification files, or
fails patch validation, it is rejected before application. PushGuard records
the reason and asks whether to request a corrected source repair. That request
requires fresh investigation approval and receives the rejection reason as
context; the corrected diff still needs its own apply approval. Denial or EOF
stops without another request. Rejected attempts count toward the session limit.

### Reliable local application

- Large lint groups use small batches within the configured output budget.
  Lint requests include affected files and read-only rules rather than unrelated
  imported services. Test failures also trace imports from secondary reported
  locations to find the implementation behind a test runner.
- The provider receives the previous applied diff and its actual failed-check
  result. It can revise an ineffective repair rather than treating every attempt
  as a fresh, unrelated problem.
- For replacement proposals in TypeScript projects with a locally installed
  compiler, candidate source is parsed **in memory before the apply gate**.
  Syntax-invalid code is rejected without writing it to the working tree.
  This is a syntax check; real lint, types, tests and build still run afterward.
- Typed function parameters such as `token: string` retain their exact source
  bytes. Credential values continue to be redacted.
- A proposal may only edit files supplied as editable context. Package-script
  verification launchers and dependency directories are protected. If no editable
  implementation is available, the CLI explains that before spending a request.
- HTTP 429 preserves the session and already-approved local changes. PushGuard
  asks before waiting for the provider's retry window and sending one new
  request. Ctrl+C cancels the wait; there is no hidden retry loop. Attempts still
  count toward `repair.maxAttempts`.

For local edits without Git commit/push gates, run `pushguard check` interactively.
`--non-interactive` and `--json` intentionally perform verification only.

## What is sent to the model?

- Check name, actual command and the grouped, verified diagnostics.
- Small affected files, or bounded windows around **each** reported location
  in larger files (including distant errors in the same file).
- Candidate related implementation/type/symbol context and a relevant Git diff.
- Read-only ESLint, Prettier and TypeScript configuration and package scripts,
  when present, without executing those configurations to gather context.
- Project language/framework metadata and the analysis when preparing a patch.

Source and configuration have separate budgets. Unreadable, sensitive,
out-of-repository or oversized context is withheld. Likely secrets are redacted
again at the HTTP boundary. Configuration and tests are evidence, never repair
targets. Diagnostics from a failed check are planned into bounded related groups;
the tool makes one built-in provider request per approved attempt, not one request
per diagnostic. Input, output and safety budgets are configurable; see `ollama.md`.

## Proposal protocol and adding providers

The existing interface remains `llm.Provider` (`Analyze`, `ProposeFix`).
`llm.RepairProvider` adds `Name` and `Available`; production construction goes
through `llm.NewRepairProvider`. This preserves injected test providers and lets
future adapters reuse the verification engine.

The real adapters support Ollama `/api/chat` and OpenAI-compatible
`/v1/chat/completions`. Responses are structured analysis or a `RepairProposal`:

```json
{
  "summary": "Use block-scoped variables",
  "rootCause": "ESLint prohibits var",
  "confidence": "high",
  "diagnosticsAddressed": ["D-SUPPLIED_ID"],
  "files": ["src/example.ts"],
  "edits": [
    {"file": "src/example.ts", "oldText": "var message", "newText": "const message"}
  ],
  "risks": [],
  "verification": ["Rerun the configured lint check"]
}
```

Exact replacements must match once in the named file. The Go engine generates
the unified diff **in memory**; the provider cannot write files or run Git.
Existing diff proposals remain supported. All proposals pass the same
path/protected-file/size/secret checks, clean-apply check, human apply gate,
state check and snapshot before application. Verification recommendations are
descriptive text, never model-generated executable commands.

To add another real provider, implement this interface, register it in the
factory/config validation, and add request/response and denial tests. IDE
extensions are not APIs: Claude/Codex/Gemini require an explicit supported API
adapter or compatible endpoint. No unrestricted shell-agent bridge is provided.

## Optional OpenAI-compatible service

### Lower-cost Groq profile

Earlier cloud acceptance used Groq `openai/gpt-oss-20b`, with its API key stored in
macOS Keychain under `pushguard.groq`. New zsh sessions export `GROQ_API_KEY`
from Keychain; the credential itself is not in the repository or shell config.
Repository and user-wide provider settings both select Groq.

```json
"ai": {
  "enabled": true,
  "provider": "openai-compatible",
  "model": "openai/gpt-oss-20b",
  "endpoint": "https://api.groq.com/openai/v1",
  "localFirst": false,
  "api_key_env": "GROQ_API_KEY",
  "maxOutputTokens": 2048,
  "reasoningEffort": "low"
}
```

Set `privacy.allowRemoteAI: true` and retain secret redaction. Approved repair
context is sent to Groq. The built-in HTTP adapters now use **one generation
request per repair cycle**, returning both analysis and the patch proposal.
There is no automatic paid retry or automatic upgrade to a larger model. A new
cycle still needs human approval. Model-catalog checks do not generate tokens.

`maxOutputTokens` defaults to 2048 and is configurable from 256 to 16384. It
limits each response, including reasoning where the provider counts it. A
response cut off at this limit is rejected, not applied. For larger repairs,
increase the limit explicitly. `reasoningEffort` is optional and should only be
set for models supporting that parameter. Low reasoning is a cost/quality
tradeoff; real checks and exact-diff review remain mandatory.

Groq's published model pages currently list 20B at $0.075 input / $0.30 output
per million tokens, versus 120B at $0.15 / $0.60. Verify current pricing in your
account; this is a token-cost reduction strategy, not a guaranteed bill or fix.

Sources: [20B model](https://console.groq.com/docs/model/openai/gpt-oss-20b),
[120B model](https://console.groq.com/docs/model/openai/gpt-oss-120b).

### Other compatible endpoints

```json
{
  "ai": {
    "enabled": true,
    "provider": "openai-compatible",
    "model": "YOUR_PROVIDER_MODEL",
    "endpoint": "https://YOUR_PROVIDER/v1",
    "localFirst": false,
    "api_key_env": "PUSHGUARD_AI_API_KEY"
  },
  "privacy": {"redactSecrets": true, "allowRemoteAI": true}
}
```

Set that environment variable through your shell or secret manager. Never put
the key itself in repository configuration. `apiKeyEnv` is the camelCase alias.
Local compatible services can instead use loopback, `localFirst: true` and
`allowRemoteAI: false`. An explicit key environment name also supports local
services that require authentication; cloud credentials are not sent to local
services implicitly. Redirects are refused and remote endpoints require HTTPS.

## Troubleshooting

| Report | Meaning / action |
|---|---|
| Provider disabled | Inspect the **selected configuration path**. Replace `provider: none` or `enabled: false` if you want AI. |
| Cannot connect | Start Ollama/service and verify endpoint/port. Source verification still works. |
| Model unavailable | Run `ollama list`; select the exact tag or download that model. An empty catalog is not success. |
| HTTP 401/403 | Check the configured API-key environment variable and provider permissions. |
| HTTP 429 | Approve the displayed rate-limit wait/retry or stop. Each retry is explicit; reducing unrelated context lowers token pressure but does not change the provider's quota. |
| Invalid catalog | The endpoint returned a different API shape or invalid JSON; check the base URL/protocol. |
| Invalid structured response | The real request ran but the response was unusable; use a model with reliable structured output. No patch is applied. |
| `oldText` does not match | The proposal does not match current source exactly; PushGuard refuses fuzzy application. |
| Candidate TypeScript syntax check failed | The proposed code does not parse. It was not applied; authorize a corrected proposal to pass this feedback to the model. |
| No editable implementation context | The failure points to protected/unavailable files, or the implementation could not be located. Inspect paths/imports and make any required developer-owned configuration changes. |
| Protected file / suppression rejected | No patch is applied. Review the reason, then explicitly allow one corrected-proposal request or deny to stop. Never approve disabling checks as a repair. |
| Lint incorrectly classified as environment | Rebuild/reinstall PushGuard. Structured ESLint messages and embedded source are code evidence on either output stream; actual process errors outside the JSON remain runtime evidence. A lint failure should reach Allow Fix before any AI request. |
| Missing environment variable | Supply the legitimate environment requirement; AI is not asked to remove it from application code. |
| Repair limit | Keep local changes or restore PushGuard snapshots. Never treat an incomplete repair as verified. |

## Verification and demonstrations

Normal Go tests use deterministic mock providers and HTTP fixtures. They verify
no provider call after denial, no edit after patch denial, grouped context,
availability errors, exact application, actual rechecks and separate push gates.

`pushguard demo --scenario live` runs the configured real provider on a
disposable Go project. Decisions are visibly scripted **DEMO MODE**, while AI,
checks and the local Git push are real. Production `push` reads human input.

See [workflow verification](workflow-verification.md) for recorded test evidence
and the Node/ESLint real-provider acceptance run. Hosted jobs and remote quotas
that cannot be observed locally are never represented as verified.
