# PushGuard verified-delivery security model

## Authority and credentials

AI providers are proposal-only. The Go engine owns validation, approvals,
snapshots, execution and Git. Review, patch, commit, branch push and PR creation
are distinct one-use scopes. State-machine transitions independently enforce
those gates. There is no merge state/API or hosted branch-writing repair loop.

`check`, JSON and noninteractive local runs cannot authorize external writes.
Installing a trusted hosted workflow with explicit `--publish` delegates only
Check API publication to that workflow. Publishing checks is distinct from
authorizing code changes or merge.

GitHub credentials live in `gh` authentication/secret-manager environment, never
`.pushguard.json`, receipts or command arguments. Provider errors and terminal
output redact token formats (including OAuth, installation and fine-grained PATs).
Receipt-signing private keys live in private local evidence storage; only public
keys are enrolled with maintainers.

## State protection

The existing local fingerprint covers HEAD, Git configuration, index, tracked
content/modes, untracked content, submodules and on-disk PushGuard config. PR
delivery adds a receipt-bound fingerprint over commit, tree, clean diff, config,
local-state hash and receipt ID. The push grant binds this evidence and exact
destination. Local and remote state are rechecked before sending the pinned
commit. State changes invalidate authorization; no force push is substituted.

PR creation/update checks the actual pushed remote tip and reads back PR HEAD.
Hosted publication re-fetches HEAD and receipt metadata. The remote can still
change immediately after any API call, but published checks are attached to the
specific inspected commit, never a floating “latest verified” identity.

## Untrusted input

- GitHub repository parsing accepts canonical github.com HTTPS/SSH remotes;
  credentials in URLs, extra paths, queries and control characters are rejected.
- Git/`gh` receive argument arrays. PR titles/bodies travel as bounded JSON stdin,
  never shell scripts. No generated command or repository instruction authorizes
  anything.
- Existing patch/path policy protects tests, check launchers, dependencies,
  configuration, secrets and suppressions. Exact anchors, snapshots and real
  reverification remain mandatory.
- Portable receipt extraction requires one bounded versioned block. Hashes,
  signatures and remotely obtained tree/config/commit must agree. PR-author
  keys or boolean “passed” claims are not trust anchors.
- Hosted check summaries escape Markdown/HTML-sensitive content and redact
  credentials. Annotation paths must be relative and nontraversing; locations
  must come from tool evidence.
- Hosted publisher builds only maintainer-controlled source, does not execute PR
  scripts, and receives minimal repository permissions. Webhook adapters must
  verify HMAC signatures before routing; the included helper does not implement
  a public service or replay database.

## Trust limits

A trusted local signer can still lie about their machine. Signed receipts are
attributable issuer claims, not hardware-backed execution proofs. Independent
hosted CI is required. Compromised GitHub check-writing credentials, trusted
publisher code or maintainer-controlled key enrollment are outside what a receipt
hash can prevent. Configure branch protection to require the correct integration.

Project verification commands are repository-owned executable code. Local users
approve the displayed plan; run untrusted repositories in an appropriate sandbox.
The hosted publisher does not need to run those commands at all.

Hosted failures never rewrite local history/evidence or trigger automatic repair.
Developers return to `pushguard pr repair NUMBER` and the local approval flow.
