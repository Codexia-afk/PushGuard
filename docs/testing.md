# Verification and demonstration

From the source folder on macOS, Linux, or Windows:

```text
go run ./cmd/pushguard ready --non-interactive
```

After installation, from a project:

```text
pushguard ready --non-interactive
```

For a source checkout, `make pre-push` runs the same verification gate. For
the complete guarded operation, use `pushguard push`; it adds Git/resource
preflight, human review, final authorization, and the pinned push.

The same pipeline drives CI. Run `pushguard ready --json` for one versioned machine-readable report. Source archives are supported without inventing Git evidence. Configure required checks explicitly when discovery cannot identify the project's tools.

## Meaningful safety tests

Tests use real temporary Git repositories and local bare remotes. They cover:

- initial outgoing commits and historical large objects, not just current file sizes;
- NUL rename paths with spaces, different upstream branches, and worktree operation states;
- good, bad, detached, divergent, and rejecting push destinations;
- missing LFS and unknown resource classification;
- all separate permission gates and denial without source/remote mutation;
- approved repair, failed repair, a second error, continued repair, retry limit;
- actual failed-check reruns and complete pipeline reruns;
- review before the commit blocker and retained repair provenance after a human commit;
- concurrent edits during verification, patch review, and final authorization;
- changed configuration bytes and changed remote branch tips;
- successful remote push and denial leaving the remote untouched;
- exact JSON output, noninteractive refusal, strict flags, safe init, staged review;
- sealed snapshot rollback, modes, unrelated state drift, repository identity;
- mandatory privacy, endpoint redirects, malformed AI responses, Ollama and compatible JSON;
- bounded logs, exit codes, timeout/cancellation, secret redaction without modifying approved data;
- TypeScript/ESLint/Go/Ruff/Cargo and stack-frame diagnostic evidence;
- hook receipt freshness and verified-ref restriction;
- receipt failure preventing push.

LLM HTTP tests use an in-memory HTTP transport, so tests never require a service or opening a network port. A fake provider supplies controlled proposals only inside tests/demo; it is not a production configuration provider.

```text
go test -count=1 ./...
go test -race -count=1 ./...
```

The race detector needs a suitable C toolchain. CI runs it on Linux. The platform matrix runs normal verification and real demos on Windows/Linux with Go 1.22/stable, macOS 15 with Go 1.22, and current macOS with stable Go. macOS 26 requires the LC_UUID emitted by Go 1.24+; the legacy macOS job stays on a compatible OS. Release compilation covers macOS amd64/arm64, Linux amd64/arm64, and Windows amd64/arm64. Cross compilation alone is not a claim of native runtime testing.

## Judge script

```text
pushguard doctor
pushguard demo
pushguard demo --scenario deny
pushguard demo --scenario stale
pushguard ready --non-interactive
```

The demos use disposable local remotes and scripted demo decisions. No credentials, public push, cloud account, or real model is required. `demo --scenario all` runs repair/pass/deny/stale demonstrations together.

## Limits

Hosted GitHub Actions cannot execute until the project is uploaded to a configured repository. Local success is recorded separately from that external execution. Actual project toolchains, remote service availability, secrets, containers, databases, quota, and AI model quality cannot be certified by a platform build.

Source files use LF through `.gitattributes`, including Windows checkouts. Windows vet checks remain enabled; the native ACL test retains the pointer returned by Windows as a typed pointer. Failed CI jobs also print direct test output for diagnosis.
