# Security and authority

Repository content, configuration scripts, tool logs, diagnostics, and AI responses are separate kinds of input. Repository text and model responses cannot grant authority. Project verification commands execute as the user; approve projects/scripts you intend to run.

AI analysis, candidate generation, exact patch application, repair continuation, review, and push use distinct scopes. A candidate is validated before display, then bound to the reviewed repository fingerprint and exact diff. State changes invalidate the patch decision. Final authorization binds the repository, verified commit, destination URL/ref, outgoing commits, and verified state. Git sends the pinned commit after fresh state and remote-tip checks.

Privacy is enforced in code. Local AI accepts loopback/localhost, ignores proxies, and rejects redirects. Remote HTTPS requires explicit configuration and a destination-specific analysis consent. Sensitive paths are excluded from context. Diagnostic/context reads resolve symlinks, stay inside the repository, and enforce file limits. Known secret patterns are redacted before provider requests and evidence storage. Terminal escape sequences cannot issue terminal commands through displayed logs.

Patch policy rejects tests/fixtures, secret files/assignments, dependencies/manifests, configuration/CI/policy, traversal, absolute/Windows-volume paths, symlink parents, binary patches, renames, file-mode changes, malformed hunks, oversized diffs, and excessive file counts. The exact candidate is shown before application approval. The Go engine revalidates immediately before `git apply`.

Snapshots preserve only affected files, with private repository-scoped metadata and post-apply hashes. Rollback checks current affected-file state before writing and refuses unrelated edits. Snapshot inputs and command/context outputs are bounded. Repository locks prevent concurrent PushGuard push workflows. OS permissions remain the boundary for other programs running as the same user.

Preflight treats infrastructure failures and unobservable hosted quotas as infrastructure, not requests for AI source changes. LFS missing/corrupt objects, Git conflicts, detached HEAD, remote divergence, oversized history, dirty trees, stale verification, and missing evidence stop a push. Git protocols are restricted to file/http/https/ssh/git; arbitrary external remote helpers are not enabled.

No automatic commit, force push, deletion refspec, history rewrite, model-generated command execution, or destructive reset is provided. Git/server-side hooks and the remote remain authoritative. A successful local pipeline cannot guarantee server permission or hosted CI success. A lost/failed push acknowledgement requires inspection of the recorded Git output and current remote state.
