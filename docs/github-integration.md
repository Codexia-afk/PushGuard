# GitHub integration

PushGuard carries signed local verification evidence into a PR, observes hosted
CI independently, and publishes a commit-specific GitHub Check through a trusted
hosted publisher. It never merges. Local-only `check` and existing `push` remain
available without GitHub.

## Local setup

Install GitHub CLI and authenticate with `gh auth login`, or provide `GH_TOKEN`
through a secret manager/environment. PushGuard invokes `gh api` with argument
arrays and JSON on stdin. It never reads a token into repository configuration
or passes one in command arguments. Git push uses your Git credential setup.

Commit the effective repository configuration before delivery:

```json
{
  "version": 1,
  "github": {
    "enabled": true,
    "pr": {"enabled": true, "base_branch": "main", "draft": false},
    "checks": {"publish": true, "required": ["ci"]}
  }
}
```

Merge this section into existing configuration; retain your checks, AI settings,
and approvals. Omitted values retain PushGuard defaults. JSON and the documented
restricted YAML form are supported. In YAML, required names use an inline
JSON-style list (`required: ["ci", "tests"]`). Never put credentials here.

`base_branch` defaults to GitHub's default branch when omitted. Use a feature
branch: PR mode refuses detached HEAD and the repository's default/base branch.
It pushes the current feature branch even if an upstream is configured differently.

`checks.publish` tells the local workflow to require the separately published
PushGuard Check before reporting complete delivery. It does not grant the laptop
permission to publish a trusted hosted result. Install the publisher described
in [GitHub checks](github-checks.md). `required` names supplement all observed
checks; missing names stay pending. Branch protection remains authoritative.

## Commands

```sh
pushguard doctor
pushguard pr --title "Fix payment calculation" --draft
pushguard pr --body-file description.md --base main --wait 5m
pushguard pr status --number 42 --json
pushguard pr review 42 --plain
pushguard pr repair 42
pushguard pr key --json
pushguard pr receipt --json
```

`status`/`review` find the current branch's open PR if a number is omitted.
`--repo PATH` selects the local checkout; `--wait` is bounded to 30 minutes.
Create/repair `--json` or `--non-interactive` cannot approve mutations. `key`
exports only the local signing **public** key, in trusted-key JSON format.
`receipt` exports the latest immutable portable delivery receipt.

`doctor` checks authentication and repository accessibility. Repository write
access is a capability hint, not proof that the token has PR/Checks write scopes;
the authorized API operation remains authoritative.

## Permissions

| Actor | Required capabilities |
|---|---|
| Local Git | Push the feature branch; branch protections/hooks still apply |
| Local `gh` | Metadata/contents read, pull requests read/write, checks/actions/status read |
| Hosted publisher | Contents read, pull requests read, actions read, commit statuses read, checks write |
| GitHub App adapter | Same repository permissions via an installation token exposed to `gh` as `GH_TOKEN` |

Fine-grained tokens must explicitly grant these scopes. The hosted publisher
does not require contents write, PR write, or merge privileges. `GITHUB_TOKEN`
is sufficient in a maintainer-installed Actions workflow with the declared
permissions. Fork workflows often lack these permissions; this implementation
explicitly rejects cross-repository/fork PRs rather than silently misattributing
their commit state. GitHub Enterprise endpoints are not yet supported.

## Provider boundary

`internal/codehost.CodeHostProvider` owns repository detection, PR metadata,
commit/config retrieval, changed files, hosted checks and check publication.
`GitHubProvider` implements it via `gh`; `MockCodeHostProvider` is test-only.
GitLab/Bitbucket/Azure adapters can implement the interface without changing the
local repair engine. The interface has no commit, push, branch repair, or merge
method.

Typed errors distinguish authentication, permission, not-found, rate-limit,
network, invalid request and incomplete evidence. Failed writes are not silently
retried. An ambiguous PR-create failure is resolved by rerunning and finding the
existing PR, then explicitly authorizing an update.
