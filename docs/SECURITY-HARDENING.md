<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Security hardening & threat-model verification

**Date:** 2026-06-04 · **Status:** pentest-ready (beta)

> **Completion pass (2026-06-04).** After the initial pass an adversarial
> re-audit (multi-agent, refute-by-default) drove a follow-up that **closed five of
> the six tracked residuals in code** (R1 sign-every-append, R2 admin pool, R3
> graph-read editor+, R4 red-team ownership, R6 Docker opt-in) and fixed several
> first-class-integration and documentation honesty findings. §7 carries the
> per-finding status and `file:line` evidence.

This document is the **evidence** that the threat model is
**implemented, not promised** — the Definition-of-Ready guardrail
("secure by design: the guardrails are in place, not promised"). Every claim is
verified against the real code with a `file:line` reference. Where a guardrail was
found missing it was either **fixed** (noted) or **reported** as a tracked
finding with severity and owner (§7).

> **How this was produced.** A multi-agent audit verified each STRIDE component of
> the threat model against the code, then a second adversarial pass tried to *refute*
> each finding (default: "not a gap unless the code proves it"). The surviving
> findings drove the fixes below. This is the methodology a real pentest repeats.

---

## 1. Threat-model verification matrix

Legend: ✅ verified · 🛡️ fixed/hardened · ⚠️ partial → tracked finding (§7).

### Collector — privilege & attack surface (`§2`, `§6`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| The eBPF sensor is **root-permitted but not privileged**, with only `CAP_BPF`+`CAP_PERFMON`; the Olivares connector holds **zero** kernel caps (privilege split) | ✅ | `connectors/ebpf/deploy/tetragon-daemonset.yaml:47-56` (sensor: `runAsNonRoot:false`, `privileged:false`, drop `ALL`, add `[BPF,PERFMON]`) and `:82-99` (connector: drop `ALL`, no add, non-root 65532, read-only rootfs); `connectors/ebpf/deploy/docker-compose.yaml:10-20,34-56` |
| Connector code requires no kernel capabilities — reads Tetragon's export only | ✅ | `connectors/ebpf/ebpf.go` `openReader` (stdin/file/FIFO); no BPF syscalls/ptrace in the package |
| **No inbound listener** on the eBPF backstop (push model, zero ports) | ✅ | eBPF package opens no `net.Listen`; reads the export file only |
| Read-only over sources — never writes observed DBs or the host | ✅ | No `sql.Open`/driver/`exec.Command`/`os.Create` across `pgaudit`/`mysqlaudit`/`s3cloudtrail`/`local`/`ebpf`/`runtime`; `runtime/docker.go` is `http.MethodGet`-only by construction |
| No access to secret **values** — reads identities, not credentials/payloads | ✅ | `s3cloudtrail/parse.go` decodes only IAM principal fields (no AccessKey/Secret); `pgaudit/parse.go` drops STATEMENT/PARAMETER SQL bodies; `ebpf/network.go` carries only the 5-tuple |
| Connector isolation: non-root 65532, dropped caps, read-only rootfs, cpu/mem limits and Kubernetes RuntimeDefault seccomp; the separate sensor is root-permitted, and the Compose sensor uses `seccomp=unconfined` | ✅ | `connectors/ebpf/deploy/tetragon-daemonset.yaml:47-63,82-102`; `connectors/ebpf/deploy/docker-compose.yaml:10-20,34-56` |
| Docker discoverer needs `docker.sock` (root-equivalent) — opt-in | 🛡️ | Docker discovery is **OFF by default** (`connectors/runtime/config.go` `enable_docker=false`); enabling it is an explicit operator opt-in, ideally via a read-only socket proxy (§2). `connectors/runtime/docker.go` is GET-only (cannot abuse it) |

### Transport — collector↔core & mTLS (`§1` STRIDE, `§3`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| TLS on by default, **no plaintext fallback** (fail closed) | ✅ | `cmd/olivares/cmd_serve.go` — gRPC server refuses to construct without TLS unless `--insecure`; default binds `127.0.0.1` |
| In-host connector↔engine channel authenticated & encrypted | 🛡️ | enabled go-plugin **AutoMTLS** (`core/runtime/loader.go` `AutoMTLS:true`) — per-launch cert pair pinned both ends on the localhost subprocess gRPC channel |
| **mTLS for remote collector→core** (verified client cert) | 🛡️ | `core/secure/tls.go` `ServerTLSConfig` sets `ClientAuth: RequireAndVerifyClientCert` + `ClientCAs` when `--grpc-client-ca` is configured (`cmd_serve.go`); `MinVersion` TLS1.2; adversarial handshake test `core/secure/mtls_test.go`. Default single-node keeps server-TLS + bearer-token auth (`core/api/grpc.go` interceptor) |
| Cooperative OTLP/hook ingest is not an unauthenticated open port | 🛡️ | `connectors/claude/claude.go` `Open` refuses a **non-loopback bind** unless `allow_public_bind=true`; the eBPF backstop is the supported off-host capture path |

> **Honest scope (corrected in the threat model):** in the shipped topology connectors run
> **in-process or as local go-plugin subprocesses over loopback** — there is no
> remote network collector peer yet. So "mTLS collector→core" describes the posture
> for a remote-collector deployment (now *available*, opt-in) plus AutoMTLS on the
> local channel; the operator API uses TLS + bearer token. The doc was reworded to
> match the code rather than overclaim.

### Data minimization & redaction (`§3`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| Persist **edges only** (agent→resource, mode, source, ts) — no SQL/body/secrets | ✅ | `modules/access-map` edge model; connectors emit refs, not payloads |
| Redaction + secret detection on `tool_input`/`full_command` before persist | ✅ | `connectors/claude/resource.go` `sanitizePath`/`redact.Scrub` on every file path; `connectors/internal/redact/redact.go` `Scrub` (cloud keys, JWTs, key=value, private keys) |
| eBPF file path scrubbed before it becomes a persisted `resource_ref` | 🛡️ | (was a gap) `connectors/ebpf/observations.go` `fileEdge` now runs the path through `redact.Clean`; test `observations_test.go:TestFileEdgeScrubsSecretInPath` |
| Engine-level enforcement of `FieldSpec.Redact` on the write path | 🛡️ | (was advisory-only) `core/internal/store/sqlstore/generic.go` `redactField` hashes a Redact field (SHA-256) before INSERT/UPDATE; test `redact_test.go:TestRedactFieldNeverPersistsRaw` |
| Encryption at rest (option) | ⚠️ | Not a product feature with the pure-Go driver; deployment-provided (LUKS/FS/PG cluster TDE). Doc corrected; tracked (§7) |
| Strict file perms (0600) on keys/secrets | ✅ | `core/secure/files.go` `writeSecret` — stages under a RANDOM name, chmods, and **verifies the mode on the installed file** (`assertPerm`) before returning. Proved by `core/secure/files_custody_test.go` and by its mutant: restoring the previous `path + ".tmp"` write installs the secret at **0644**. Same discipline in `olivares license keygen` (`cmd/olivares/license_keygen_custody_test.go`), which refuses to overwrite key material without `--force` |

### Tamper-evidence ledger (`§5`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| Append-only + hash-chain; alteration detectable | ✅ | `core/internal/store/dialect/postgres.go` immutability trigger + `pgRevokeMutations` on `audit_events`; SQLite append-only triggers; chain `Verify` |
| Ed25519 signed checkpoints | ✅ | `core/audit/checkpoint.go` (domain-separated, length-prefixed preimage; `VerifyCheckpoints`) |
| **Checkpoints actually produced** (anchor live by default) | 🛡️ | (was inert) `cmd/olivares/checkpoint.go` schedules `CheckpointAll` on a default interval + a graceful-shutdown checkpoint (`--checkpoint-interval`, default 1h); test `checkpoint_test.go` |
| Off-box / externally-pinned checkpoint verification | 🛡️ | `olivares audit verify --pubkey <b64>` verifies against an external key; on-box verify is labeled `advisory_only` (`cmd/olivares/cmd_audit.go`) |
| WORM/SIEM export (CEF/LEEF/syslog/OTLP/OCSF) | ✅ | `core/audit/export.go`; `olivares audit export --format` |
| **Per-event Ed25519 signatures** (tail cannot be rewritten without the key, even between checkpoints) | 🛡️ | (was a residual) the engine signs every event over its chain hash at write time (`core/store/audit.go` `AuditEventSigner`; `core/internal/store/sqlstore/audit.go:46-121` `Append`; `core/audit/eventsig.go` `SignEvent`/`VerifyEvents`); the mutable head is no longer load-bearing. Verified off-box by `audit verify --pubkey` |

### Panel auth & secure defaults (`§4`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| **No default credentials**; single-use setup token on first boot | ✅ | `cmd/olivares/cmd_serve.go` `announceSetup`; `core/secure/setup.go`; no seeded admin/password anywhere |
| RBAC on the access graph; every panel action self-audited | ✅ / 🛡️ | `core/auth/authorizer.go`, `permission.go`; `handlers_core.go` self-audits graph reads. Viewing the graph is now an **editor+** privileged read (never `viewer`), core + module surfaces, via `permission.go` `accessGraphReadPerms` (§8) |
| Passwords hashed; brute-force throttled | ✅ | `core/auth/credential.go` `HashPassword`; `throttle.go` |
| TLS on, telemetry-home off, binds to localhost by default | ✅ | `cmd_serve.go` defaults `127.0.0.1:8443/8444`, `--insecure` default false; no outbound analytics call in the tree |
| `--seed-demo` cannot expose a public-password superadmin off-host | 🛡️ | (was a gap) `cmd_serve.go` refuses `--seed-demo` on a non-loopback bind; tests `serve_guard_test.go` |

### License key (`§1` STRIDE, `§4`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| Ed25519 signature, offline validation; forged license rejected | ✅ | `core/license/license.go` `Verify`; `license_test.go` forgery cases |
| No **production** signing key in the repo/build | ✅ | A release embeds two PUBLIC anchors: `OLIVARES_LICENSE_PUBKEY` and `OLIVARES_OTA_PUBKEY`. The dedicated license private key is online only in the narrow fulfilment Worker; the OTA private key remains off-box/HSM and signs only during the release ceremony. The build validator rejects an empty, malformed, or equal pair. `-tags release` also drops the dev license seed. |
| Build self-reports both keys | ✅ | `olivares version` prints independent `license-key=<origin>/<fingerprint>` and `ota-key=<origin>/<fingerprint>` fields. They are build-provenance aids, not attestations; trust comes from the signed release pipeline and comparison with `docs/RELEASE-VERIFICATION.md`. |
| Revocation by expiry | ✅ | `Claims.Status` checks `exp`/`NotAfter` against the **local clock** (a clock rollback defeats expiry — an honest offline limit, never an enforcement signal) |

> **⚠️ Clarification 2026-06-07 (dev-keypair-seed):** the only signing key *derivable from the tree* is the **development/seed key** — `core/license/embedded_dev.go` carries a fixed `devSeed` from which `DevPrivateKey()` derives a full pair, used **only** for demos, the `license sign` CLI in development builds, and the tests. It is **deliberately public and signs nothing of value**, because license validation is **attestation-only and governs no feature, request or boot** (`core/license/license.go`: "MUST NOT disable, degrade or block any feature"). Do not confuse this development seed with production signing material: the production private key is **never in the tree**, and a **`-tags release` build excludes `embedded_dev.go` entirely** so the dev seed is physically absent from a release binary (`license sign` there requires `--key`).

> **⚠️ Self-attested status:** the license status the engine surfaces (`server-info`, `version`) is **self-reported by a customer-controlled binary** against whatever key its build embedded. **No server-side entitlement, billing, metering, support or cloud-plan decision may trust it.** This is a **binding design rule, not a description of a running service**: no managed cloud is deployed today, and when one is, it must derive a tenant's plan **solely** from its own Merchant-of-Record billing record, never from what a deployment reports about itself.

### Multi-tenant isolation (`§3`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| App-layer tenant predicate on every read/write (primary control) | ✅ | `core/internal/store/sqlstore/generic.go` unconditional `tenant_id = ?`; debug tripwire `guard()` |
| Postgres FORCE RLS + per-tenant policy (backstop) | ✅ | `dialect/postgres.go` `pgTenantGuard`; boot self-test refuses tables missing the guard (`selftest.go`) |
| **The connecting role cannot silently bypass RLS** | 🛡️ | (was CRITICAL) boot guard refuses a superuser/BYPASSRLS Postgres role unless `--allow-privileged-db-role` (`sqlstore/store.go` + `dialect` `ConnRolePosture`); role provisioning in `deploy/postgres/01-app-role.sql`; tests `roleposture_test.go`, `postgres_test.go:TestPostgresRefusesPrivilegedRole`/`TestPostgresRLSFunctionallyDenies` |
| SQLite tenant isolation (no RLS) via closed query layer + tripwire triggers | ✅ | `dialect/sqlite.go` scope triggers; `isolation_test.go` raw-insert backstop |
| Cross-tenant System reads do not silently return empty on a secured Postgres | 🛡️ | (was a residual) dedicated BYPASSRLS admin pool (`--admin-dsn`, validated non-superuser, exempt from the app-pool guard) for `ListOrgs` / multi-tenant checkpoint coverage (`sqlstore/store.go` `openAdminPool`, `system.go`); boot warns when absent. The app-pool RLS guard is unchanged |

### Anti-evasion (`§6`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| Collector is a separate service outside the agent's control | ✅ | Privilege split in the deploy manifests; engine launches connectors as subprocesses it controls |
| eBPF backstop captures kernel-level access even when cooperative telemetry is off | ✅ | `connectors/ebpf/*` reads kernel events independent of the agent's OTEL config |
| Telemetry **gap** is treated as a signal | ✅ | `connectors/claude/watchdog.go` silence detection → finding; `connectors/ebpf/evasion.go` gap finding |
| Forged cooperative heartbeats can't defeat the watchdog from off-host | 🛡️ | OTLP receiver refuses non-loopback bind (above); eBPF backstop is authoritative |

### Dual-use / responsible design (`§8`)

| Mitigation claimed | Status | Evidence |
|---|---|---|
| Viewing the graph is privileged (editor+), tenant-scoped and fully self-audited | ✅ / 🛡️ | `handlers_core.go` `accessgraph:read` is tenant-bound + audited; granted from editor up, never `viewer` (`permission.go` `accessGraphReadPerms`) |
| No offensive capability (no C2, no third-party credential scanning) | ✅ | Red-team module defaults to an **offline** sandbox that reaches no network (`modules/redteam/redteam.go`, `wire.go` wires no live sandbox); no scanner of others' credentials in the tree |
| Red-team target ownership is enforced | 🛡️ | (was a residual) `modules/redteam/ownership.go` `resolveOwnedAgent` resolves `agent_ref` against the tenant's own agent inventory at registration (rejects a target not owned) and re-checks at launch; cross-tenant refs do not resolve (tenant-pinned scope). The live offensive sandbox itself is a separate, deferred capability |

---

## 2. Attack surface (documented for pentest)

**Network listeners (engine, default):**

| Port | Proto | Bind (default) | Auth | Notes |
|---|---|---|---|---|
| 8443 | HTTPS (REST + web UI) | `127.0.0.1` | bearer token (session `olvs_…` / API key `olvk_…`); setup token first-boot only | TLS on; `--insecure` (dev only) disables it, and is **refused on a non-loopback bind** unless `--insecure-allow-public-bind` is also given |
| 8444 | gRPC (ControlPlane API) | `127.0.0.1` | bearer token; **opt-in mTLS** via `--grpc-client-ca` | fail-closed (no plaintext unless `--insecure`) |

**Cooperative collector (Claude connector, optional):** OTLP/gRPC `127.0.0.1:4317`, OTLP/HTTP + hooks `127.0.0.1:4318` — loopback-only by default; a non-loopback bind is refused unless `allow_public_bind=true` (ingest is unauthenticated by design — same-host agent).

**eBPF backstop:** **no listeners.** Push model. The root-permitted Tetragon sensor holds `CAP_BPF`+`CAP_PERFMON`; the non-root Olivares connector holds **no** caps. Kubernetes applies RuntimeDefault seccomp to both examples, while the Compose sensor explicitly uses `seccomp=unconfined` and needs a deployment-specific profile for tighter confinement.

**Host privileges required:**
- eBPF backstop: `CAP_BPF`+`CAP_PERFMON` for the *sensor* only (separate container).
- Docker discovery (optional): **read access to `docker.sock` is root-equivalent** — use a read-only/GET-allowlisted socket proxy in production; the connector itself is GET-only.

**On-disk secrets (data dir, 0600):** TLS key (`tls.key`), audit signing key (`audit-signing.key`), setup token (`setup.token`), the store (`olivares.db` for SQLite). **Protect the data dir (0700)** — it co-locates the DB and the audit signing key; for attacker-resistant ledger verification keep an **off-box** copy of the public key and use `audit verify --pubkey`.

### 2.1 Anonymous surface (login/setup) — the engine's posture and YOUR edge's job

The unauthenticated endpoints are `/v1/auth/login`, `/v1/setup`, `/v1/server-info`, the SSO
callbacks and the operational probes. What the engine does, what it deliberately does NOT do, and
what that delegates to the deployment edge:

- **Login lockout (built in):** dual-key, per-account AND per-IP — 5 consecutive failures lock the
  key for 15 minutes (`core/auth/authenticator.go:56,64`; `core/auth/throttle.go`). Failures are
  audited (`auth.login.failed`, with the peer IP in Meta) and counted
  (`olivares_auth_login_attempts_total{outcome}` — `success|failed|locked_out`, see
  [`docs/17-PRODUCTION-READINESS-SLO.md`](17-PRODUCTION-READINESS-SLO.md) §5).
- **The IP leg uses `RemoteAddr`, never `X-Forwarded-For` — by design.** XFF is attacker-writable;
  honoring it unauthenticated would let a brute-forcer rotate fake IPs to dodge the lockout, or
  lock out a victim by spoofing their address (`core/api/handlers_auth.go:151-163`). The honest
  consequence: **behind a reverse proxy/LB the `ip:` key collapses to the proxy's address** — one
  attacker can trip the shared IP lock for everyone behind that proxy, and the per-IP leg stops
  distinguishing sources. The per-ACCOUNT leg keeps working regardless. There is deliberately no
  trusted-proxy XFF knob in v1; if one is ever added it must be opt-in with an explicit
  trusted-CIDR list.
- **Setup (`/v1/setup`):** guarded by the one-time 256-bit token alone (constant-time compare,
  single-use, consumed on success — `core/secure/setup.go`). There is **no throttle** on token
  attempts: entropy is the control (an online brute force of 2^256 is not a credible threat), and
  the surface exists only until first setup completes.
- **No IP-keyed anonymous rate limiter — deliberately.** Behind a proxy,
  `RemoteAddr` is one address, so an IP-keyed bucket collapses to a SINGLE shared bucket: one
  attacker exhausts it and the "fairness" control self-DoSes all login/setup for everyone — strictly
  worse than delegating that edge (`core/api/middleware_ratelimit.go:23-31`). The limiter meters the
  AUTHENTICATED surface; the bad-bearer flood is 401'd before metering and is likewise an edge concern.
- **What YOUR ingress/WAF must therefore own** (the chart ships no Ingress on purpose — BYO edge,
  `deploy/helm/olivares/values.yaml`): per-client-IP rate limits on `POST
  /v1/auth/login` and `POST /v1/setup` at the layer that still SEES real client IPs. Reference
  knobs: NGINX ingress `nginx.ingress.kubernetes.io/limit-rps` (or `limit_req` with a dedicated
  zone keyed on `$binary_remote_addr` scoped to those two paths), HAProxy `stick-table type ip` +
  `http-request deny if { sc_http_req_rate(0) gt N }`, Traefik `rateLimit` middleware, or your
  WAF's credential-stuffing ruleset. Pair it with the engine signals: alert on
  `rate(olivares_auth_login_attempts_total{outcome="locked_out"}[5m])` spikes and audit
  `auth.login.failed` bursts.

---

## 3. Secure-defaults audit (factory posture)

Boot the product with no flags and you get: **no credentials** (setup token printed once to stdout), **TLS on**, **no telemetry-home**, **localhost binds**, **append-only audit + per-event signatures + scheduled signed checkpoints**, **Docker discovery OFF** (root-equivalent socket), and — on Postgres — a **hard refusal to start against an RLS-bypassing role**. Each dangerous departure is an explicit, named opt-in: `--insecure` (loopback-gated — a non-loopback bind is refused unless the operator ALSO passes `--insecure-allow-public-bind`, because plaintext off-host puts the console, every bearer token and the first-boot setup token on the wire in clear), `--allow-privileged-db-role`, `allow_public_bind`, `--seed-demo` (loopback-gated), `enable_docker`. Tests blind these defaults (`serve_guard_test.go`, `serve_insecure_bind_test.go`, `roleposture_test.go`, `claude_test.go`, `mtls_test.go`, `docker_test.go`).

---

## 4. Supply-chain & build integrity

| Control | How | Verify |
|---|---|---|
| **Reproducible static build** | `CGO_ENABLED=0`, `-trimpath`, `-buildid=`, build date pinned to the commit (`SOURCE_DATE_EPOCH`). Pure-Go SQLite → fully static, memory-safe | `task build:repro` twice → identical SHA-256 (demonstrated 2026-06-04) |
| **SBOM** | syft, SPDX-JSON, per release artifact | `task sbom` / `.goreleaser.yaml` `sboms` |
| **Checksums** | SHA-256 over every artifact | `checksums.txt` (goreleaser `checksum`) |
| **Signed releases** | cosign — keyless/Sigstore by default (GitHub OIDC → Fulcio/Rekor); key-based path documented for air-gap | `.goreleaser.yaml` `signs`/`docker_signs`; user verifies with `scripts/verify-release.sh` |
| **Distroless images** | `gcr.io/distroless/static-debian12:nonroot`, non-root 65532, no shell/pkg-mgr | `Dockerfile`, `Dockerfile.release`, `Dockerfile.ebpf-source` |
| **Dependency CVE gate** | `govulncheck` per module, blocking | CI `govulncheck` job |
| **Secret scanning gate** | gitleaks (pinned v8.30.1) over full history, blocking | CI `secrets` job; `task lint:secrets`; config `.gitleaks.toml` |
| **No `curl\|bash` without checksum** | dev tools installed via `go install @version` (go.sum-checksummed) | `Taskfile.yml` `tools`; CI lint/secrets jobs |
| **Minimal pinned deps** | go.work `toolchain go1.26.4`; small direct dep set (chi, pgx, cobra, grpc, modernc/sqlite, go-plugin) | `core/go.mod` |

**Image tag pinning:** the privileged eBPF path no longer references `:latest`; deploy manifests carry a pin-a-digest note (`connectors/ebpf/deploy/*`).

> **Reproducibility depth:** the target is a *stable, verifiable
> digest* (same source → same bytes), demonstrated above. Full bit-for-bit across
> heterogeneous toolchains is a documented future step, not a release blocker.

### Verifying a release (end user)

```sh
# in the directory with the downloaded artifacts + checksums.txt(.sig/.pem)
scripts/verify-release.sh                  # keyless (Sigstore) — default
scripts/verify-release.sh --key cosign.pub # key-based (air-gap)
```

The script verifies the cosign signature over `checksums.txt`, then verifies each
artifact's SHA-256 — refusing if either step fails.

---

## 5. Compliance control mapping (light — design-to-audit)

The control plane is itself a compliance-evidence engine (module XIII, `modules/compliance`).
This maps the guardrails above to the controls auditors check. **No certification is claimed**
(SOC2 Type 2 is a later step); the design is built to *pass* the review.

| Guardrail (this doc) | SOC 2 (TSC) | ISO/IEC 27001:2022 Annex A | EU AI Act |
|---|---|---|---|
| Append-only + hash-chained audit, signed checkpoints, WORM export | CC7.2/CC7.3 (monitoring), CC4.1 | A.8.15 (logging), A.8.16 (monitoring) | Art. 12 (record-keeping / logs) |
| RBAC, no default creds, setup token, self-audit | CC6.1/CC6.2/CC6.3 (logical access) | A.5.15/A.5.16/A.5.18 (access, identity) | Art. 14 (human oversight) |
| TLS by default, opt-in mTLS, AutoMTLS on the plugin channel | CC6.7 (transmission) | A.8.20/A.8.21 (network, transit) | Art. 15 (accuracy, robustness, cybersecurity) |
| Multi-tenant FORCE-RLS + boot guard against bypass | CC6.1 (logical access) | A.8.3 (information access restriction) | Art. 10 (data governance) |
| Data minimization (edges only; redaction; engine-enforced Redact) | C1.1 (confidentiality) / P-series | A.8.10/A.8.11 (deletion, masking) | Art. 10 (data governance, minimization) |
| Signed releases + SBOM + checksums + pinned deps + CVE/secret gates | CC8.1 (change mgmt) | A.8.25–A.8.30 (secure development), A.5.23 | Art. 15 (cybersecurity by design) |
| Self-hosted: no mandatory telemetry, no control-plane egress by default (what crosses the perimeter is what the operator configures — model API calls, the SIEM/webhook outputs they wire, an external embedding provider if provisioned) | C1.1 / privacy | A.5.34 (privacy/PII) | Art. 10 + GDPR data residency |
| Coordinated disclosure, CVE/patch process (`SECURITY.md`) | CC7.4/CC7.5 (incident) | A.5.24–A.5.28 (incident mgmt) | Art. 15 / post-market monitoring |

Live, per-tenant evidence for these is produced by `modules/compliance` (`frameworks.go`,
`capabilities.go`) and honestly reports *absent* where no evidence exists.

---

## 6. Pentest-readiness checklist

- [x] Threat model verified point-by-point against code with `file:line` evidence (§1).
- [x] Adversarial security tests (real, not smoke): ledger tamper / checkpoint anchor, cross-tenant RLS deny + privileged-role refusal, insecure-default refusal, forged license rejected, mTLS no-cleartext + client-cert required, secret-in-path / Redact-field not persisted, telemetry-gap alerted.
- [x] Secure defaults blinded by tests; every dangerous mode is an explicit named opt-in.
- [x] Supply chain: reproducible build (demonstrated), SBOM, checksums, cosign signing, distroless, blocking `govulncheck` + gitleaks, no `curl|bash`, pinned deps.
- [x] Attack surface documented (ports, processes, privileges, on-disk secrets).
- [x] `SECURITY.md` + coordinated-disclosure policy (channel, timelines, safe harbor) published.
- [x] Compliance control mapping (SOC2 / ISO 27001 / EU AI Act).
- [ ] **External pentest** (the maintainer contracts it) — this document is the package the auditor starts from.
- [x] Residual findings (§7): R1–R4 and R6 **fixed** in the completion pass; R5 (encryption at rest) is open by design (deployment-provided).

---

## 7. Residual findings — status after the completion pass (2026-06-04)

The first pass tracked six residuals (R1–R6) as deferred. A follow-up
completion pass (same date, after an adversarial re-audit) **fixed five of them**
rather than leave them deferred; only R5 remains open, and it is a deliberate
architecture decision, not an unfixed guardrail. Each ✅ row carries the new
`file:line` evidence and tests.

| # | Finding | Sev | Status | Evidence / resolution |
|---|---|---|---|---|
| R1 | `audit_heads` is a mutable tip; a raw-DB attacker who also deletes the signed checkpoints could rewrite the tail | medium | ✅ fixed | **Sign-every-append**: the engine signs each event over its chain hash at write time (`core/store` `AuditEventSigner`, `core/internal/store/sqlstore/audit.go` `Append`, signer `core/audit/eventsig.go` `SignEvent`/`VerifyEvents`), threaded through every audit chain (`scope.go`, `system.go`). Verified off-box via `olivares audit verify --pubkey` (now checks `event_sigs` too). The mutable head is no longer load-bearing. Append-only-head was evaluated and rejected (conflicts with the upsert tip and adds no cryptographic resistance — a raw-DB attacker truncates an append-only head identically). Honest limit: an on-box key (full data-dir compromise) can re-sign; per-event signatures defend DB-only compromise + checkpoint deletion, off-box/HSM key defends the host-compromise case |
| R2 | Postgres `System`/`ListOrgs` cross-tenant reads need a separate BYPASSRLS admin pool (`AdminDSN` declared, unwired); with a correct app role they returned empty | medium | ✅ fixed | Dedicated admin pool wired: `core/internal/store/sqlstore/store.go` `openAdminPool` (opened from `--admin-dsn`, validated BYPASSRLS-non-superuser, **exempt** from the app-pool RLS guard which is unchanged); `ListOrgs` runs there (`system.go`); `cmd_serve.go --admin-dsn`; role in `deploy/postgres/01-app-role.sql`; boot **warns** when Postgres runs without it. Fixes transitively the silent `CheckpointAll`-only-system-tenant degradation. PG-gated tests `postgres_test.go` |
| R3 | Access-graph read was granted to the lowest `viewer` role (the recon map must be privileged) | medium | ✅ fixed | Viewing the graph is now an **editor+** privileged read, never `viewer` — both the core surface (`accessgraph:read`) and the access-map module surface (`accessmap:graph:read`/`accessmap:drift:read`), via `core/auth/permission.go` `accessGraphReadPerms` + `RoleGrants`. Still tenant-scoped + every read self-audited (defense in depth). Tests `auth_test.go`, `modules/access-map/api_test.go` |
| R4 | Red-team `Target.AgentRef`/`Endpoint` ownership not enforced in code | medium | ✅ fixed (defense-in-depth) | `modules/redteam/ownership.go` `resolveOwnedAgent` resolves `agent_ref` against the tenant's own agent inventory (`sc.Agents()`) at registration (422 if not owned) and re-checks at launch (`consent.go`, `scorecard.go`); cross-tenant refs do not resolve (tenant-pinned scope). Tests `redteam_test.go`. *(The live offensive sandbox itself is a separate, deferred capability; this is the ownership half, correct to enforce now.)* |
| R5 | Encryption at rest is not a product feature (pure-Go SQLite has no cipher) | medium | ⬜ open by design | Deployment-provided (LUKS / FS-level / Postgres cluster TDE) + 0600 key perms + 0700 data-dir. A CGO SQLCipher build target would **break** the pure-Go, fully-static, reproducible, memory-safe binary that §4 sells — so this is kept a deployment concern, not retrofitted. Owner: ops/deploy docs |
| R6 | Docker discovery defaulted ON; `docker.sock` read is root-equivalent | low | ✅ fixed | Docker discovery is now **opt-in, default OFF** (`connectors/runtime/config.go` `enable_docker` default `false`); test `docker_test.go` `TestDockerDiscoveryOffByDefault`. Linux procfs (unprivileged) and the scoped-token K8s path stay on. Socket-proxy guidance in §2. Connector is GET-only |

---

## 8. References

`ARCHITECTURE.md` (architecture trust, topology) ·
`SECURITY.md` (disclosure policy) · `deploy/postgres/01-app-role.sql` (role provisioning)
· `.goreleaser.yaml` / `Dockerfile*` / `.gitleaks.toml` (supply chain) ·
`scripts/verify-release.sh` (release verification).
