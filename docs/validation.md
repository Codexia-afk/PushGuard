# Implementation validation

Validated locally on 3 October 2026 with Go 1.26.5 on macOS ARM64.

| Check | Observed result |
|---|---|
| `pushguard ready --non-interactive` | PASS: formatting, vet, all unit/integration tests, all-package build |
| `go test -race -count=1 ./...` | PASS with the installed Apple Command Line Tools C compiler |
| `pushguard ready --json` | PASS: one schema-2 JSON document, four passing checks, persistent cycle evidence |
| `pushguard doctor` | PASS: actual Git, Go, formatter, configuration, writable evidence storage |
| Every command's `--help` | PASS |
| Invalid CLI flag | Refused with exit code 2 |
| `pushguard demo --scenario all` | PASS: two approved repairs, fresh-session review and push, clean pass, denied push, stale-state refusal |
| Unix source installer | PASS in an isolated installation directory; installed executable reports version 0.3.0 |
| Six release targets | Built macOS/Linux/Windows amd64 and arm64 packages, plus stable installer aliases |
| Archive contents and SHA-256 hashes | PASS for every versioned and stable release package |
| Extracted macOS ARM64 release executable | PASS: executable reports version, commit, build date, Go, and platform metadata |
| Windows security test executable | Cross compiled successfully; native ACL assertions run in Windows CI |

The supplied master prompt is mapped to implementation and evidence in
[requirements.md](requirements.md). Installation, normal usage, privacy,
configuration, and judge commands are documented in [README.md](../README.md).

## Scope of the evidence

Native runtime execution here was on macOS ARM64. The GitHub Actions matrix is
configured for Windows, Linux, and macOS with Go 1.22 and stable; those hosted
runs require uploading the project to a Git repository. Cross compilation is
recorded separately from native execution.

Demos use real Go checks and real Git with disposable local bare remotes. Their
AI proposals and human choices are explicitly scripted demonstration inputs.
Live Ollama/compatible services and model quality were not exercised here.

This folder is a source archive without `.git`; local verification works in
this form. Actual project pushes require a committed Git repository and a
configured remote. Remote authorization, protected branch policy, hosted CI
resources, and billing quotas remain subject to the actual server's result.
