# PushGuard GitHub Check

`cmd/pushguard-hosted` is a separate executable for maintainer-controlled CI or a
future GitHub App adapter. It reads signed local evidence, independently fetches
PR HEAD/commit/config and hosted results, and publishes **PushGuard Verification**.
It never executes checkout code, calls an AI provider, repairs files, pushes, or
merges. Without `--publish`, it is read-only.

```sh
go build -o pushguard-hosted ./cmd/pushguard-hosted
pushguard-hosted --repository acme/project --pr 42 --require ci --json
pushguard-hosted --repository acme/project --pr 42 --require ci --publish
```

`--require` is repeatable and comes from maintainer-controlled publisher policy,
not the PR's configuration. `--trusted-keys FILE` or `PUSHGUARD_TRUSTED_KEYS`
provides trusted public keys. GitHub authentication uses `gh`, normally with
`GH_TOKEN` set to the workflow's short-lived `github.token`.

## Install beside existing workflows

Existing project CI is preserved. Install this separate workflow on the default
branch after reviewing the publisher code/binary. This example is for a checkout
containing PushGuard source; other projects should install a pinned, verified
publisher binary or check out a maintainer-controlled pinned PushGuard revision.
Never build the publisher from a PR head or restore untrusted PR artifacts into
the credential-bearing publisher job.

```yaml
name: PushGuard receipt integration
on:
  pull_request_target:
    types: [opened, reopened, synchronize, edited]
  workflow_run:
    workflows: [CI] # replace with your existing verification workflow name(s)
    types: [requested, completed]
permissions:
  contents: read
  pull-requests: read
  actions: read
  checks: write
  statuses: read
concurrency:
  group: pushguard-${{ github.event.pull_request.number || github.event.workflow_run.head_branch }}
  cancel-in-progress: false
jobs:
  publish:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          ref: main # trusted default branch only; never PR head
          persist-credentials: false
      - uses: actions/setup-go@v5
        with:
          go-version: stable
          cache: false
      - run: go build -o "$RUNNER_TEMP/pushguard-hosted" ./cmd/pushguard-hosted
      - name: Validate receipt and observe CI
        env:
          GH_TOKEN: ${{ github.token }}
          PUSHGUARD_TRUSTED_KEYS: ${{ vars.PUSHGUARD_TRUSTED_KEYS }}
        run: '"$RUNNER_TEMP/pushguard-hosted" --publish --require ci'
```

The workflow deliberately has no dependency installation or tests from PR code.
`pull_request_target` carries elevated credentials: only this read-only-inspection
publisher should run there. Use normal `pull_request` workflows for PR tests with
restricted tokens. Pin third-party Actions to reviewed SHAs under your organization's
policy. The repository variable contains **public** keys only; maintainers must
control who can edit it. Use `pushguard pr key --json` during issuer enrollment.

Keep the workflow name `PushGuard receipt integration`: it identifies publisher
jobs so aggregation cannot wait on or recursively include the publisher itself.
The executable supports `pull_request`-shaped and `workflow_run` event files via
`--event-file`/`GITHUB_EVENT_PATH`. It re-fetches all conclusions and ignores stale
events whose head no longer matches. No network listener is installed. A future
server can use `delivery.VerifyWebhook` before routing authenticated webhook data.

## States and aggregation

Supported domain states: QUEUED, IN_PROGRESS, SUCCESS, FAILURE, NEUTRAL, CANCELLED,
UNKNOWN. Missing expected checks, queued workflow runs and active checks prevent
success. A skipped/neutral required check is not a PASS. Unknown evidence remains
unknown/pending. All observed non-publisher checks participate, plus every named
required check. The API paginates within explicit bounds and rejects incomplete
evidence instead of treating a partial response as success.

The Check API maps unknown to neutral, pending to queued/in-progress, and final
results to completed conclusions. PR HEAD and body are rechecked immediately
before publication. Every check is pinned to an immutable SHA; a new commit never
inherits old receipt success. Repeated observations update the existing
PushGuard Check instead of creating one per poll.

## Annotations

For failed hosted check runs, existing GitHub annotations become bounded,
redacted PushGuard annotations with exact relative path, reported line/column,
tool/rule when provided, severity and message. No location is invented. Absolute,
traversing, malformed paths and unknown line locations are omitted. Up to 50
annotations are published; the source check retains complete evidence. Plain
logs/commit-status failures without structured annotations remain summary-only.
The publisher does not download and feed arbitrary logs into an LLM.

## Trust and final result

The laptop may validate its own key for local display; it cannot write a trusted
hosted success through `pushguard pr`. The trusted publisher performs final
issuer validation and check publication. Require the PushGuard Check from the
appropriate App/integration in GitHub branch protection. A status label alone
cannot protect against another actor with check-writing privileges.

This initial implementation supports same-repository PRs on github.com. It
observes checks on PR HEAD (including Actions workflow runs for that head), not
GitHub merge queues, deployment approvals, every branch-ruleset condition, or
all possible future workflows. Missing configured expected names stay pending.
Maintainers retain the merge decision and GitHub remains the final policy gate.
