# Local Ollama repair

PushGuard uses Ollama's native HTTP API as a **proposal-only** provider. The Go
engine supplies bounded evidence, validates proposals, requires independent
investigation/apply approvals, and reruns the project's actual tools. A locally
installed model needs no cloud API key. Repair quality depends on the model and
the failure; fifty diagnostics are not a promise of fifty fixes.

## Setup and Pi

Start the Ollama application, or `ollama serve`. Install a model explicitly:

```sh
ollama pull qwen2.5-coder:7b
ollama list
pushguard ai status --test
```

`qwen2.5-coder:7b` was selected for the measured run below on a 16 GiB Mac. The
model download is approximately 4.7 GB; inference needs additional memory for
the context window and runtime. Model names are configurable. PushGuard never
downloads a model, changes models automatically, or falls back to cloud AI.

Pi is a separate coding agent, not an LLM or PushGuard provider. To use it
independently with the same local model:

```sh
ollama launch pi --model qwen2.5-coder:7b
```

The model-specific Pi launch/help was exercised on this machine. Pi installation
and its optional web-search extension are separate from PushGuard. Pi's file and
shell tools are not used by the repair engine; they do not inherit PushGuard's
approval policy. PushGuard talks directly to `http://127.0.0.1:11434/api/chat`.

## Configuration

Merge these fields into the selected configuration, preserving its actual check
commands. Repository `.pushguard.json`/YAML overrides user defaults as a whole.

```json
{
  "version": 1,
  "repair": {
    "enabled": true,
    "requireGenerationConsent": true,
    "requireApplyApproval": true,
    "maxAttempts": 6,
    "maxDiagnosticsPerGroup": 10
  },
  "ai": {
    "enabled": true,
    "provider": "ollama",
    "model": "qwen2.5-coder:7b",
    "endpoint": "http://127.0.0.1:11434",
    "localFirst": true,
    "maxOutputTokens": 2048,
    "requestTimeoutSeconds": 300,
    "context": {
      "max_input_tokens": 6000,
      "reserved_output_tokens": 2048,
      "safety_margin_tokens": 512
    }
  },
  "privacy": {"redactSecrets": true, "allowRemoteAI": false}
}
```

The context defaults are 6000/2048/512; the default group limit is 10 and the
default session limit remains 5. The acceptance fixture explicitly uses 6.
`maxOutputTokens` and `reserved_output_tokens` must agree when both are set.
Nested `ai.context` also works in the restricted YAML configuration.

`pushguard ai status` reports LOCAL/REMOTE, configured budget, service and model
availability. `--test` additionally generates a small structured response without
repository source. A native local request allows five minutes by default for
cold loading/generation; `requestTimeoutSeconds` overrides that (10–900 seconds).
Ctrl+C cancels. Catalog probes are shorter. Native responses record actual model
load duration, prompt/output counts and request duration when Ollama supplies them.

Local-only mode refuses remote endpoints, redirects, cloud model tags and
catalog entries advertising a remote model. Local requests bypass HTTP proxies.
The configured local daemon remains part of the trusted local environment.

## Grouping, budgeting and verification

- Real tool diagnostics receive request-scoped `D-` IDs and check attribution.
  Check/file/rule/message identities omit line/column, so shifting lines alone
  does not invent progress. Repeated equivalent diagnostics use occurrence
  ordinals; progress compares their multiplicity, not a claim of bug identity.
- The deterministic planner separates checks, groups nearby locations in the
  same file/rule family, and can join directly importing files with the same
  family. Groups contain at most the configured diagnostic limit (maximum 50)
  and at most three directly related files. IDs and ordering are reproducible.
- Context includes reported regions and bounded containing declarations,
  related implementations, test observations and read-only project rules.
  It is not the whole repository. Diagnostic capture is bounded at 1000; capped
  failures require narrowing the real check before automatic investigation.
- Before inference, serialized instructions, evidence and the native response
  schema are conservatively estimated at `ceil(bytes / 3) + 128`. This is an
  estimate, not an exact tokenizer. The independent safety margin helps with
  tokenizer/template variation. Ollama `num_ctx` is input + reserved output +
  margin: **8560** for the example; `num_predict` is **2048**.
- Oversized context loses duplicate logs/diffs and optional context first,
  narrows windows while retaining selected locations, then splits diagnostics.
  If it still cannot fit, no inference request is sent. Both preparation and the
  final transport enforce the bound.
- One built-in generation request returns explanation and edits for an approved
  attempt. The native schema requests `summary`, `rootCause`, `confidence`,
  `diagnosticsAddressed`, `edits`, `risks` and `verification`. Unknown JSON fields,
  invented/repeated supplied diagnostic IDs, truncated responses, unsafe paths,
  protected-file edits, suppressions and invalid/nonunique anchors are rejected.
  Verification text is advisory, never a command to execute.
- Go/JSON syntax is parsed in memory where applicable; JavaScript/TypeScript
  edits use the project's installed TypeScript parser when available. Real
  lint/typecheck/test/build still determine the result after approved application.
- Each applied patch reruns the failed command. The report records observed
  resolved/persisting/new diagnostics, groups, estimates, inference metrics,
  request/attempt counts, rejected/applied proposals, changed files and commands.
  Counts from incomplete output cannot certify resolution. The interactive
  summary's per-check counts are first/last observations, not necessarily a
  simultaneous baseline of all checks.
- Every further attempt needs fresh consent. The attempt cap includes rejected
  proposals. Existing repeated/inverse patch and repository-state checks, safe
  snapshots and rollback remain in force. Reaching the limit offers keep/rollback
  and returns failure. Only a complete clean rerun plus review can finish PASS.

## Reproducible acceptance

Normal `go test ./...` uses mocks and **skips** the real-model test. Explicitly opt
in after starting/installing Ollama and Node/npm:

```sh
PUSHGUARD_OLLAMA_ACCEPTANCE=1 \
PUSHGUARD_OLLAMA_MODEL=qwen2.5-coder:7b \
PUSHGUARD_OLLAMA_ARTIFACTS=/existing/artifact/directory \
go test ./internal/app -run '^TestOllamaLargeAcceptance$' -count=1 -v -timeout=17m
```

The optional artifact parent must exist. The test copies `testdata/ollama-large`
into a disposable Git repository and installs pinned ESLint/TypeScript versions
using npm (`--ignore-scripts --no-audit --no-fund`). It never downloads a model.
Scripted test decisions exercise the independent investigation/apply/review
gates; they are not an unattended product feature. `RunCheck` cannot commit or
push. Protected fixture files and local/remote Git tips are checked afterward.

### Measured run: 2026-10-03

- Native **qwen2.5-coder:7b**, Ollama **0.35.0**, loopback endpoint above.
- **189 source lines**; **50 genuine ESLint `no-var` diagnostics** initially.
  A separate arithmetic assertion also failed (one failing test, two captured
  tool diagnostics). These are deliberately seeded failures, not a benchmark
  representing arbitrary production code.
- First plan: **5 groups of at most 10 diagnostics**. After the approved patch,
  actual lint output contained **35 diagnostics**, regrouped into 4 groups.
- **6 attempts / 6 generation requests / 1 approved applied patch / 5 rejected
  proposals / 1 file modified / 0 new lint diagnostics**.
- Request input estimates **4307–5882**, all below 6000. Ollama-reported input
  counts **3109–4497**, output counts **692–1818**, all below the output cap.
  First model load: **2.854 s**. Repair interval: **461.6 s**; entire harness:
  **467.5 s**.
- The five rejected proposals reused stale `var` anchors after the first patch.
  Exact-match validation prevented application. No test/config changes, commit,
  push, or cloud requests occurred.

| Real command | Baseline | Final independent rerun |
|---|---|---|
| `npm run lint` | FAIL, 50 diagnostics | FAIL, 35 diagnostics |
| `npm run typecheck` | PASS | PASS |
| `npm run test` | FAIL, one test / two diagnostics | FAIL, one test / two diagnostics |
| `npm run build` | PASS | PASS |

**Repair outcome: FAIL — attempt limit, exit 5.** Across the separately measured
four checks, captured diagnostics decreased **52 → 37**. The interactive repair
session stopped at lint, so it did not reach complete verification or final
review. The harness ran all four checks afterward to report their real outcomes;
its safety assertions passing do not turn this into a successful repair.

Artifacts on the implementation machine:
`/var/folders/6b/nz7pl6_95kgdt5y9rgsqrsmw0000gn/T/omnirush/ollama-acceptance-1641690204/`
contains `baseline.json`, `final.json`, `session.json`, `outcome.json`,
`transcript.log` and `source-final.js`. A later machine/model may produce different
results. The guaranteed behavior is bounded, approved, verified execution—not a
guaranteed number of model fixes.
