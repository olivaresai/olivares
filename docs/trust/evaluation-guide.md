<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Evaluation guide — proof of value in 10 business days

> For platform engineering, security and compliance teams evaluating Olivares AI.
> Every step runs against the real binary serving real data. The demo estate is
> synthetic — the engine, the enforcement and the audit trail are not.

## Prerequisites

| Requirement | Detail |
|---|---|
| Hardware | 2 CPU cores, 4 GB RAM (evaluation; production sizing in `docs/SIZING-AND-CAPACITY.md`) |
| OS | Linux amd64 or arm64 |
| Go | 1.26+ (build from source) |
| Task | [taskfile.dev](https://taskfile.dev) (the build runner) |
| pnpm | for the embedded web console build |

Optional: `cosign` (release signature verification), Docker (container path),
Postgres (HA topology evaluation).

---

## Phase 1 — Day 1-2: Deploy and verify supply chain

Build from source, boot the demo estate, and confirm that the supply chain is
verifiable before anything else. If the binary does not boot or the signatures do
not check, stop here.

| # | Criterion | Action | Expected result | Reference |
|---|---|---|---|---|
| 1.1 | Build from source | `task build` | Binary appears at `./bin/olivares`; exit 0 | [README — Install](../../README.md#install) |
| 1.2 | Boot the demo estate | `./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"` | `=== DEMO MODE (SYNTHETIC DATA) ===` banner with the public demo credentials; console at `http://127.0.0.1:8901` (loopback-only; `--insecure` turns TLS off — demo only, refused on non-loopback addresses) | [README — Quickstart](../../README.md#quickstart) |
| 1.2b | Secure first run (no demo) | `./bin/olivares quickstart` | TLS on at the printed URL (default `https://127.0.0.1:8443`), **one-time setup token** printed, no default credentials — this is the credential-free-first-run property; it belongs to a fresh estate, not the seeded demo | [README — Quickstart](../../README.md#quickstart) |
| 1.3 | Verify cosign signature | `scripts/verify-release.sh --key cosign.pub` | "PASS" for all 5 checks (signature, SBOM attestation, SLSA provenance, OpenVEX, checksum) | [RELEASE-VERIFICATION.md](../RELEASE-VERIFICATION.md) |
| 1.4 | Check SBOM attestation | Inspect the release attestation bundle | CycloneDX and SPDX SBOMs present and parseable | [RELEASE-VERIFICATION.md](../RELEASE-VERIFICATION.md) |
| 1.5 | Inspect security.txt | `curl -fsSL https://olivares.ai/.well-known/security.txt` (this repo's copy: `docs-site/public/.well-known/security.txt`, a docs-site asset that the engine does not serve; the apex is served by the web site) | RFC 9116-valid `security.txt` with contact, policy and encryption fields; consistency with `SECURITY.md` is CI-checked by `scripts/check-security-txt.sh` | [SECURITY.md](../../SECURITY.md) |
| 1.6 | FIPS build (optional) | `GOFIPS140=v1.0.0 task build` | Binary boots with FIPS module loaded (CMVP cert #5247, Level 1); `olivares version` reports FIPS | [SCP-09-FIPS-STIG.md](../SCP-09-FIPS-STIG.md) |

**Pass/fail:** the binary builds, boots with TLS and no default credentials, and
the supply-chain artifacts verify offline. If 1.3 fails on a pre-release build
(no tagged release yet), verify the source commit signature instead.

---

## Phase 2 — Day 3-6: Governance loop

Walk through the observe-map-govern-audit cycle end to end. Every step below runs
on the demo estate; substitute your own sources when ready.

| # | Criterion | Action | Expected result | Reference |
|---|---|---|---|---|
| 2.1 | Explore the access map | Open the console, navigate to the access map | Permitted-vs-observed drift visible: the demo estate seeds 20 nodes, 13 edges and 8 unexpected accesses in the drift panel | [Architecture](../../ARCHITECTURE.md) |
| 2.2 | Write a Cedar deny policy | Create a `forbid` policy via the console or `POST /v1/m/governance/policies` (`PUT /policies/{id}` is update, not create) | The denied tool returns 403 on the next governed request; the deny event appears in the audit ledger | [Cedar policies how-to](../../docs-site/src/content/docs/how-to/cookbook/deny-closed-policies.md) |
| 2.3 | Configure Claude Code hooks PEP | Wire a `PreToolUse` hook pointing at the engine (see the Claude Code tutorial) | Hook fires deny on a configured tool; the governed session shows the block in the console | [Claude Code governed operation](../../docs-site/src/content/docs/how-to/run-claude-code-with-olivares.md) |
| 2.4 | Trigger HITL approval flow | Submit a request that hits a `require_approval` policy | Approval request visible in the console; approve/deny completes the flow; the decision is hash-chained in the ledger | [HITL approvals](../../docs-site/src/content/docs/how-to/cookbook/hitl-approvals.md) |
| 2.5 | Test estate kill switch | Activate the estate kill switch from the console (requires AAL3 step-up) | All governed sessions stop; re-enabling requires a second superadmin (structural two-person control) | [Kill switch](../../ARCHITECTURE.md) |
| 2.6 | Export audit ledger | `olivares audit export --tenant <tenant-id> --format cef` (`--tenant` is required; add `--data-dir` when the estate does not use the default data directory; formats: `cef\|leef\|syslog\|otlp\|otlp_envelope\|otlp_log_record\|ocsf` — `olivares audit export --help` prints the list from the engine registry; `otlp` is a complete, postable OTLP/HTTP export request (`otlp_envelope` is an exact alias, and `otlp_log_record` is the bare one-LogRecord-per-line projection); `otlp`/`otlp_envelope`/`otlp_log_record`/`ocsf` stream as NDJSON, one JSON document per line, while the segmented `*.jsonl` files are a different artifact that exists only in the audit *archive* path) | CEF event lines; the ledger behind them is append-only and hash-chained with Ed25519-signed checkpoints; `olivares audit verify --tenant <tenant-id>` passes | [SECURITY-HARDENING.md](../SECURITY-HARDENING.md) |
| 2.7 | Wire a SIEM sink | Configure a syslog or OTLP push forwarder in the console | Events flow to the external collector within seconds; formats: CEF, LEEF, syslog, OTLP, OCSF | [SIEM integration](../../docs-site/src/content/docs/how-to/cookbook/push-to-siem.md) |
| 2.8 | Create a FinOps budget | Set a spend ceiling on a workspace or model surface | Budget hit triggers deny (hard cap) or throttle (soft cap); the event is recorded with cost evidence | [FinOps](../../docs-site/src/content/docs/how-to/cookbook/budgets-and-finops-guardrails.md) |
| 2.9 | Run a guardian loop | Enable a guardian finding type and trigger it | Finding reported; subsequent duplicates are suppressed (anti-spiral dedup); the finding appears in the console | [Guardian loops](../../docs-site/src/content/docs/reference/glossary.md) |
| 2.10 | Configure managed settings for Claude Code | Upload a managed-settings payload via the console | Settings delivered to the governed Claude Code session on next attach; the console shows the delivery status | [Managed settings](../../docs-site/src/content/docs/how-to/connectors/claude-code-hooks-pep.md) |

**Pass/fail:** every step produces verifiable evidence in the audit ledger. If
any step silently succeeds without an audit event, that is a bug — report it.

---

## Phase 3 — Day 7-10: Compliance and enterprise readiness

Validate the evidence, compliance and identity surfaces. Steps marked
"(enterprise)" require evaluation entitlements for the corresponding commercial
add-ons — contact enterprise@olivares.ai to arrange them.

| # | Criterion | Action | Expected result | Reference |
|---|---|---|---|---|
| 3.1 | Export OSCAL evidence bundle | Seal an evidence package (console, or `POST /v1/m/compliance/frameworks/{id}/evidence`), then `GET /v1/m/compliance/evidence/{id}/export?format=oscal` | Valid OSCAL JSON (component-definition + assessment-results + control-mapping); parseable by an OSCAL validator | [machine-readable-evidence.md](./machine-readable-evidence.md) |
| 3.2 | Review compliance dashboard | Open the compliance section of the console | 26 framework catalogs visible (EU AI Act, NIST AI RMF, ISO 42001, SOC 2, ISO 27001, GDPR, OWASP Agentic, and others) with per-control status and evidence pointers | [Compliance catalogs](../../docs-site/src/content/docs/reference/modules/xiii-compliance.md) |
| 3.3 | Wire identity source (OIDC) | Configure an OIDC provider (Okta, Entra, Google, etc.) via the console | SSO login works; the identity source appears in the roster; user attributes are reconciled | [SSO how-to](../../docs-site/src/content/docs/how-to/connectors/sso-scim-identity.md) |
| 3.4 | Backup/restore cycle | `olivares dr backup --passphrase-file <file>` then `olivares dr restore` (exactly one of `--passphrase-file` or `--kek-key-file`; the passphrase is never passed inline) | `.drbundle` created; restore succeeds; `olivares audit verify` passes post-restore (chain continuity preserved) | [DR-RUNBOOK.md](../DR-RUNBOOK.md) |
| 3.5 | Verify there is NO user cap | Create a 4th, 10th and 50th active user account, with and without a licence installed; then let a licence lapse (or remove it) and create another | Every creation succeeds — user accounts are unlimited in every edition and a licence lapse never caps or deletes accounts. The console's Edition & license tab shows active-user USAGE with no quota | [LICENSING.md](../../LICENSING.md) |
| 3.6 | SSO enforcement (enterprise) | Enable require-SSO and block password login | Password login blocked; SSO remains the only login path; the SSO config API reports `enforced_by: "enterprise"` for the deployment-wide primary IdP (the open build honestly reports `"unavailable"` — it stores the posture but never enforces it). Set the posture on the deployment-wide primary: a per-tenant IdP, or a second IdP under another alias, reports `"out_of_scope"` — the engine resolves the deployment-wide primary's posture only, so a posture stored elsewhere is definitively not the one applied | [SSO enforcement](../../docs-site/src/content/docs/explanation/open-core-and-licensing.md) |
| 3.7 | Content firewall (enterprise) | Enable the content firewall and send a prompt with a known injection pattern | Injection detected and blocked across message, retrieval and MCP render channels; the event shows the detection detail | [Content firewall](../../docs-site/src/content/docs/explanation/open-core-and-licensing.md) |
| 3.8 | Multi-IdP federation (enterprise) | Configure a second active IdP (OIDC or SAML) | Both IdPs active; login routes to the correct IdP by tenant or domain | [Multi-IdP federation](../../docs-site/src/content/docs/explanation/open-core-and-licensing.md) |
| 3.9 | Review honesty-and-limits page | Read `docs-site/src/content/docs/start/honesty-and-limits.md` | Every listed gap and limitation matches real product behavior — no overclaimed capability | [Honesty & limits](../../docs-site/src/content/docs/start/honesty-and-limits.md) |

**Pass/fail:** the compliance evidence is machine-readable, the backup restores
with cryptographic verification, and the honest limits page matches observed
behavior. Enterprise criteria require the corresponding add-on evaluation
entitlements.

---

## After the evaluation

- **Feature matrix:** see [feature-matrix.md](./feature-matrix.md) for the
  complete open-core vs commercial add-ons comparison.
- **Why the commercial add-ons:** see [why-enterprise.md](./why-enterprise.md) for the
  commercial value proposition and what each add-on entitlement includes.
- **Reference architecture:** see [reference-architecture.md](./reference-architecture.md)
  for deployment topologies, HA/DR numbers and sizing.
- **Vendor viability:** see [vendor-viability.md](./vendor-viability.md) for the
  single-vendor risk mitigation (AGPL survival guarantee, escrow, no operational
  dependency).

Questions? enterprise@olivares.ai
