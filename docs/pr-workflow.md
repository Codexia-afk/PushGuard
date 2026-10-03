# PR-native verified delivery

Authority: **AI proposes. Human authorizes. PushGuard controls. Tools verify.
Git pushes. GitHub hosts review. Humans merge.**

Run `pushguard pr` on an existing feature branch with committed PushGuard config.

1. Discover project, Git target and GitHub repository; display commands.
2. Ask permission to execute local verification.
3. Stop on a required failure and display real diagnostics/classification.
4. Offer AI investigation where editable implementation context exists.
5. Require independent investigation and exact-diff application approvals.
6. Snapshot, apply the approved patch, rerun the failed check, then continue.
7. Rerun the complete pipeline against a stable state.
8. Run Git/resource preflight and mandatory local review. Review supports full
   diff, files, AI changes, tests, preflight, explanation, undo, continue and exit.
9. If tracked repairs are uncommitted, separately offer a reviewed local commit
   using the existing `commit --only` gate. Reverify the committed state. Other
   developer changes must be committed by the developer and reverified.
10. Persist a signed receipt binding commit, tree, configuration, local state,
    tool results and approved AI-repair provenance.
11. Separately ask for feature-branch push authorization. The displayed actual
    operation uses `git push -- REMOTE_URL COMMIT_SHA:refs/heads/FEATURE` to pin
   the reviewed commit, rather than resolving a moving branch at execution.
12. Recheck local state and remote tip immediately before Git push.
13. After push succeeds, show PR metadata and ask separately to create/update it.
    Details show the complete body. Edit changes title, description and base;
    a subsequent confirmation is still required. `--body-file` supports multiline
    descriptions. Existing PR metadata changes are explicit updates, not duplicates.
14. Read PR HEAD back from GitHub. Observe its hosted checks and signed receipt.
15. With a trusted publisher installed, wait for its commit-specific check. Pending
    means pending; unknown/missing checks never mean success. `--wait 5m` polls
    with a bounded deadline. `status` continues observation later.

No merge action is implemented or authorized at any step. Final success says
“Required PushGuard checks passed. Ready for human review.”

## Read-only PR inspection

`pushguard pr status [NUMBER]` shows local receipt integrity, observed hosted
checks, the published PushGuard Check and PR URL. `--json` returns structured
evidence; `--plain` avoids color dependence. Pending observation exits zero with
an explicit non-success state; consumers must inspect `overall`, not equate
command execution success with verification success. Failed verification exits 1;
API/config errors exit 2.

`pushguard pr review [NUMBER]` additionally lists changed files and AI-assisted
files. It does not require opening GitHub in a browser or grant mutation rights.
Hosted observations never modify the historical local receipt.

## Hosted failure and local repair

`pushguard pr repair 42` requires the PR's open, same-repository feature branch
to be checked out. It displays available hosted failure evidence and re-enters
the **local** verification/repair/review/push/PR-update gates. There is no bot
commit loop. A hosted-only environmental failure may require local environment
setup; PushGuard does not invent a source patch if local tools cannot establish
the failure.

New commits invalidate a prior receipt. A new `pr`/`pr repair` session verifies
and signs the new commit, then explicitly updates the PR's evidence block.

## Failure behavior

- Deny investigation: no provider request.
- Deny apply: no proposed source write.
- Finish review: no push permission yet.
- Deny push: no branch mutation.
- Deny PR: approved branch push remains, but no PR mutation is performed.
- Local state changes after review: authorization invalidated; verification
  runs again before a fresh review/approval.
- Remote advances: stop; no force push.
- GitHub PR indexing lags the successful Git push: bounded read-only polling
  while the actual remote tip still equals the authorized commit; no retry push
  or unapproved PR mutation.
- PR HEAD changes during creation or hosted inspection: stop/report stale.
- Authentication/network/check-publication failure: surface the actual error;
  do not report a hosted PASS.

The existing local session receipt records structured events including
verification, repair, review, push, PR authorization/creation, hosted outcomes and
receipt invalidation. Events contain bounded, redacted metadata rather than
credentials or prompts.
