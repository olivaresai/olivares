<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Manual three-package race profile

This is a **manual diagnostic workflow**. It is not `race-full`, not a release
acceptance check, and not a duration fix. It measures three packages one at a
time under the existing Postgres-backed `-race` recipe so CPU, scheduler and
per-test times can be read from artifacts.

## Scope

| Artifact id | Repository path | `go test` directory (`cwd` = `core/`) |
|---|---|---|
| `core-api` | `core/api` | `./api` |
| `core-auth` | `core/auth` | `./auth` |
| `core-sqlstore` | `core/internal/store/sqlstore` | `./internal/store/sqlstore` |

Each job runs **once**: `-race -json -count=1 -timeout=60m`, the normal
application/admin/maintenance/vector Postgres roles, and
`scripts/with-pg-env.sh`. Timeouts stay at Go 60 minutes, the measure step 90
minutes, the job 175 minutes. Parallelism, `GOMAXPROCS`, race detector
settings and test selection are not changed to shorten the run.

The workflow is `workflow_dispatch` only. It requires `expected_sha` (40 hex).
HEAD, `GITHUB_SHA` and that input must be identical before roles are
provisioned or tests start. A moved default branch fails visibly; the SHA is
not substituted.

## Who may dispatch

Operators dispatch from the development repository **after** this workflow
definition is on the default branch of that repository. Use a repository
parameter, not a private slug in this document:

```sh
gh workflow run race-package-profile.yml \
  --repo <development-repository> \
  --ref main \
  -f expected_sha=<FULL_COMPOSITE_VERIFIED_SHA>
```

Do not dispatch `mainline-ci` or `race-full` to obtain these three profiles.
Do not launch three independent dispatches of this workflow at once. Locate
the run by workflow name, SHA, event and time window; do not take “the latest
run” blindly.

`max-parallel: 1` sequences the three jobs of **one** run. It does not reserve
an exclusive host.

## Artifacts

Each job uploads a directory named

`race-package-profile-<sha>-<run_id>-<attempt>-<artifact>`

Retention is 7 days. The directory is unique per attempt; a measure will not
overwrite an existing one. Expected files include:

- `source.json` / `inventory.json` — commit/tree and working-byte hashes before/after;
  validated `go list -json` inventory of all three explicit paths (no test execution)
- `invocations.json`, `go.identity.json`, `binary.identity.json` — separate wrapper,
  post-probe Go and test PIDs/starttimes, argv, cwd, effective validated flags and RCs
- `prepare.json` / `prepare-*.receipt.json` — allocation and preparation stages,
  PID/UTC/monotonic clocks/RC; PG output payloads are redacted at capture, DSNs are
  **presence only**. Two sanitized streams and their byte lengths survive failures.
- `wrapper.stdout.raw.jsonl` / `wrapper.stderr.raw` — wrapper streams (stderr redacted)
- `go.stdout.raw.jsonl` / `go.stderr.raw` — Go streams after the probe
- `binary.stdout.raw` / `binary.stderr.raw` — test binary before test2json
- `resources.jsonl` — samples; missing `/proc` reads are errors, not zeros
- `runtime-scheduler.jsonl` — `gomaxprocs` from **binary stderr** SCHED lines
- `exit.json` / `tests.jsonl` / `summary.json`
- `cpu.pprof`, `package.test`, `pprof-top.txt`, `pprof-cumulative.txt` when the
  profile finished
- `SHA256SUMS`

`GOMAXPROCS` unset stays unset. The effective runtime value is taken from the
test binary’s stderr SCHED lines, never from `go env GOMAXPROCS`. Child text
quoted on the testing stream is not parent scheduler data. Unknown,
probe-not-started, censored and failure stay distinct columns.

OUT is allocated immediately after checkout, before the SHA guard, Go setup,
role provisioning and inventory. The existing SHA guard still precedes all PG
role operations and tests. `run --phase allocate|resolve|provision|inventory`
records those closed preparation stages; `run` then measures once. The workflow's
finalization step records missing or interrupted preparation without inventing a
Go/test exit. Job service startup and failures before checkout cannot create an
artifact; their evidence remains in the Actions job record.

The unchanged `with-pg-env.sh` executes `go-exec` in this assignment's scratch
after its probe. That small adapter uses the existing `exec --producer go` path to
record effective flags and launch the actual Go executable once. `test-exec` uses
`exec --producer binary`. The shell is replaced by the Go adapter; its process RC
and the Go child's RC are recorded separately, and a shell-only phase exit is
unknown after an exec transition. Only the test adapter sets
`schedtrace=5000,scheddetail=0`, replacing even inherited `schedtrace=0`. Other
runtime settings pass through; metadata records only validated numeric settings.

Upload runs for failed measures too. Cleanup requires upload success **and** a
nonempty artifact id; upload failure, cancellation or missing files retains OUT.
No shared cache is removed. Summarization refreshes the artifact checksum manifest.

## Reading the summary

`summary.json` has three independent columns:

1. **observation** — instrument integrity (`complete`, `censored`,
   `probe_not_started`, `prepare_failed`, `incomplete`, `unknown`)
2. **tests** — package result (`pass`, `fail`, `not_started`, `censored`)
3. **inspection_useful** — whether partial evidence is available to inspect

`observation_complete` and its compatibility alias `integrity_ok` require every
listed component: valid producers, expected package terminal, intact streams, parser,
resources within their declared visible scope, attributable scheduler, two successful
pprof analyses of the same inputs, coherent source/inventory, and no cancellation or
censorship. `summarize --require-complete` fails for `prepare_failed`,
`probe_not_started`, `censored`, and any incomplete component. A test failure remains
a test failure independently of this gate.

A failing suite with complete streams is useful evidence. A passing suite with
lost runtime data does not satisfy the resource measurement. CPU profile time
is not wall time; nested symbols are not disjoint categories.

Cgroup membership is resolved through the target PID's mountinfo and filesystem
root, with separate v1 controller memberships and every visible ancestor. The
reported CPU bound is the minimum of fractional quotas and the intersection of
affinity/cpusets. Absent, unreadable and unlimited controls remain different facts.
`host_limit_complete` is always false: a visible namespace root does not prove
that external ancestors are unconstrained. Incompatible namespace paths remain
unresolved instead of falling back to fixed `/sys/fs/cgroup` paths.

CPU/throttling/PSI deltas require matching process/cgroup identities, matching
counter sets and monotonic counters. Resets and migrations are discontinuities;
there is no synthetic zero delta. Process CPU and aggregate cgroup CPU stay separate.
Resource completeness additionally requires a compatible adjacent sampling window
for each producer: positive monotonic interval, recorded deltas matching the raw
snapshots, and the declared process CPU, cgroup counters and host CPU/PSI metrics.
An initial point followed only by a final `gone` is incomplete even if tests pass.
Point samples and each window's missing/reset/migration facts remain in the summary.
Internal gaps make sampled coverage incomplete. A final `gone` after a valid window
is an explicit unmeasured tail; completeness of sampled windows never means coverage
of the entire process lifetime. The sampler interval and run duration are unchanged.

The wrapper records hashes of its saved stdout and already-redacted stderr when
capture closes. Summarization checks those producer hashes, like Go/test stream
hashes, before updating derived checksums. Missing or mismatched hashes make the
streams incomplete. Historical receipts lacking them stay incomplete: a later
summary or checksum manifest cannot supply an authoritative capture hash.
Event records retain run/pause/cont/terminal timestamps and source lines. A reported
`Elapsed: 0` may be rounded; it is not a claim of zero physical duration. Parked
intervals use event wall clocks and are not CPU or setup cost.

Finite local checks use `python3 scripts/test-ci-race-package-profile.py` (fixtures
only). The optional `--microfixture` mode runs one own finite Go package, using
local PG stand-ins, and retains its binary, profiles, streams and receipts under
`RPP_EVIDENCE_DIR` when set. It does not run a product package. `--corrections`
selects the pure regressions and finite process fixtures alone.

## Limits

- This workflow does not prove host exclusivity.
- One sample per package is not an A/B against a previous overlapping run.
- `scripts/cpu-quota.sh` may be recorded as an auxiliary integer; it is not
  the certified PID ceiling.
- PostgreSQL service limits are not the job cgroup.
- A skipped test from an optional historical fixture remains a SKIP; this
  increment does not provision extra producers.
- Duration of `core/api`, `core/auth` and `sqlstore` remains unresolved until
  an authorized runner run exists and is inspected.

Root decides integration, dispatch, and any later repartition.
