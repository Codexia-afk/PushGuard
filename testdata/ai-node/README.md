# Intentionally broken real Node fixture

Used for PushGuard AI acceptance testing in a **disposable copy**. Requires
Node 22.18+ (native type stripping), npm and the pinned dependencies in
`package.json`. It has two real ESLint `no-var` errors across two TypeScript
files, then an independent arithmetic test failure. TypeScript type checking
and build are real. No check prints fabricated diagnostics or fixed outcomes.

Configure a real provider in the disposable repository's `.pushguard.json` and
run `pushguard push`. Approve investigation, review the diff, and approve apply.
After lint passes, the legitimate test still fails and must cause another
investigation/apply cycle. Never change the test's expected value to make it pass.

See `docs/ai-repair.md` for the setup guide and
`docs/workflow-verification.md` for observed acceptance results.
