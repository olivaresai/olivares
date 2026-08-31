<!-- SPDX-FileCopyrightText: 2026 Olivares.AI -->
<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Security gate runbook

This is the automated, repeatable security gate: the checks that turn "manual audit
when someone remembers" into "runs on every push / every release". It exists because
of decision **D22** — no external pen-test yet, but everything automatable is
automated and audited in the pre-release sessions. The re-executable adversarial
campaign runs on top of this scaffolding, starting from `task security:report`.

## What runs where

The hub keeps GitHub Actions **disabled** until reactivation. "CI-REL"
therefore means the step is wired into `.github/workflows/mainline-ci.yml` and runs
**per-release / on reactivation**; today it is verified by running it locally.

| Check | Task | pre-push | CI-REL | Needs |
|-------|------|:--------:|:------:|-------|
| SAST (gosec, high-signal blocking set) | `task lint:sast` | — | ✅ | `gosec` (`task tools`) |
| Fuzz smoke (bounded, every target) | `task fuzz:smoke` | — | ✅ | go toolchain |
| Dependency vulns (fail-closed) | `task vuln:gate` | — | ✅ | `govulncheck` + network |
| Secret scan (full history) | `task lint:secrets` | — | ✅ | `gitleaks` |
| Security invariants (policy tests) | `task test` | ✅ | ✅ | go toolchain |
| Consolidated evidence report | `task security:report` | — | on demand | all of the above |

**Why SAST / fuzz / vuln / secrets are CI-REL, not pre-push.** They need installed
tools (`gosec`, `govulncheck`, `gitleaks`) and — for `vuln`/`secrets` — network and a
full-history checkout, which the rest of the Go-toolchain pre-push gate does not. This
matches the pre-existing placement of `vuln` and `lint:secrets` (already CI-only). The
constrained local pre-push gate already runs ~10 min (up to ~69 min in degraded
serial mode); loading it with tool-dependent, network-bound scans would make every
push brittle. The **security invariants** (below) DO run in `pre-push` because they are
plain Go tests with no extra dependency, so a policy regression is caught on push.

Measured cost (this branch, warm cache): `task lint:sast` ≈ **28 s** wall across the
11 workspace modules (plus the privately-maintained hosted-service module where that
tree is present). It is fast enough to add to `pre-push`
later if `gosec` becomes a standard dev dependency; for now it is CI-REL + on-demand.

## SAST — gosec (E1)

`scripts/sast.sh` runs gosec over every module. The **blocking** gate runs only the
high-signal rules declared in `.gosec.json` `block_rules` and requires **zero**
findings:

- **G201/G202** SQL injection · **G203** template/XSS · **G204** command injection
- **G107** SSRF · **G401/G404** weak crypto / weak RNG · **G402/G403** TLS misconfig
- **G501–G505** blocklisted weak-crypto imports (md5/des/rc4/sha1)

Every other gosec rule (G101 name-matched credentials, G104 unhandled errors,
G115/G703 integer-overflow-on-conversion, G304 file-path-from-variable, G301/G302/G306
file permissions, …) is **high-false-positive** in this tree and is **non-blocking**.
Their real-risk counterparts are covered elsewhere: `gitleaks` + the no-secrets-in-logs
invariant for credentials; the plugin/rich-doc sandboxes for hostile path
handling; `go vet` + review for error discipline. They stay visible in the informational
full scan (`task security:report`), so nothing is hidden — just not release-blocking.

### <a name="justifying-a-finding"></a>Justifying a finding

When gosec flags a line in a blocking rule you have two options:

1. **Fix it.** The correct outcome for a genuine finding.
2. **Annotate a reviewed false positive** with a *dedicated* comment on the flagged
   line (or the line directly above, for a multi-line statement):

   ```go
   client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // #nosec G402 -- opt-in --insecure for local/self-signed endpoints
   ```

   The gate runs gosec with `-nosec-require-rules -nosec-require-justification`, so a
   bare `#nosec` or one without a `-- <reason>` **fails** the gate. A suppression can
   never be a silent blanket. Note: gosec does **not** honor `//nolint:gosec` (that is
   golangci-lint's directive) — use `#nosec`.

## Fuzz smoke (E2)

`scripts/fuzz-smoke.sh` enumerates every `func Fuzz*` and runs each for a short,
bounded `OLIVARES_FUZZTIME` (default 10 s) from its committed seed corpus — a smoke,
not a discovery campaign. `go test -fuzz` fuzzes one target per package invocation, so
targets are run one at a time by anchored name. A crash or invariant failure is a gate
failure. New parser/decoder surfaces should ship a fuzz target (this session added
`FuzzParseManifest` for OTA manifests and `FuzzParseCompactJWS` for SSF/CAEP tokens).

## Dependency vulnerabilities (E4)

`scripts/govulncheck-gate.sh` is the **fail-closed** form of `task vuln`: it fails on
any *called* vulnerability not covered by `.govulncheck-allow.yaml`. Every allowlist
entry MUST carry an `expires` date; an expired entry also fails, so a temporary
exception can never become permanent. A vulnerability **with an available fix is never
allowlisted** — bump the dependency instead.

> **Resolved declared-minimum finding (opened 2026-07-19; directive remediation
> verified 2026-08-22).** The gate reported
> `GO-2026-5037`, `GO-2026-5039` (fixed in Go 1.26.4) and `GO-2026-5856` (fixed in
> Go 1.26.5) as called because govulncheck evaluated the standard library against
> module **`go` directives at 1.26.3**, below the patched toolchain that built the
> binary. The declared minimum is now patched: product-module directives are at least
> Go 1.26.5, while `go.work` declares both `go 1.26.6` and `toolchain go1.26.6`.
> No vulnerability was allowlisted; the stale lower declared minimum is no longer
> present.

## Consolidated evidence (E4)

`task security:report` renders `docs/security/gate-report.md`: the full informational
SAST scan, the vuln gate result, the secret-scan status and SBOM presence. This is the
product-led assurance artifact the trust center links in place of an external audit
(D22). It reports honestly — a tool that did not run is recorded as "not run", never as
a pass.

## Security invariants (E3)

Policy tests that fail if a *ratified* invariant regresses, so a future change cannot
merge one silently. They run in `task test` (hence `pre-push` and CI):

- **D6 — fail-closed enterprise default** — `cmd/olivares/security_invariants_test.go`:
  the enterprise edition defaults a per-control availability dependency to fail-closed.
- **observer never-deny** — `connectors/claude/observer_invariant_test.go`: an
  observe-class hook is never deny-capable, and a gating/unknown event never silently
  degrades to never-deny.
- **DR passphrase floor** — `core/api/dr_passphrase_invariant_test.go`: the ≥12-rune
  floor is enforced on the create paths and deliberately absent on restore (legacy
  recovery).
- **No secrets in logs** — `modules/security/nosecretlog_invariant_test.go`: a
  repository-wide static guard that a known sensitive key is never logged with an
  un-redacted value.

## For the adversarial campaign

Security-gate scaffolding is ready; the adversarial campaign starts on top of
`task security:report`. The vuln allowlist, the `#nosec` justifications, and the
informational SAST scan are the inputs it audits.
