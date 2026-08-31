<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Runbook — evidence-ledger verification failure

**Severity:** SEV1 if tamper is suspected; SEV2 for a restore/rollback anomaly. The audit ledger is the product's tamper-evidence guarantee — treat a genuine failure as a security incident.

## Symptom
The per-tenant append-only hash chain, its signed checkpoints, or the per-event signatures fail to verify.

## Detect
Run verification. **The exit code matters:** by default `audit verify` prints a JSON report and **exits 0 even on a tampered chain** — so an on-call check MUST use `--strict` or parse the JSON.

```bash
# Cron / CI gate: non-zero exit on any failed check.
olivares audit verify --tenant <TENANT_ID> --data-dir <DIR> --strict
# or gate on the JSON (note the keys are capitalized — VerifyReport has no json tags):
olivares audit verify --tenant <TENANT_ID> --data-dir <DIR> \
  | jq -e '.chain.OK and .checkpoints.OK and .event_sigs.OK'
```

**`status` has three answers, not two.** `"corrupt"` is an accusation; `"unattested"` is not.
A ledger reports `"unattested"` when the chain, the per-event signatures and every marker
verified but **no signed checkpoint exists yet** (`checkpoints.Reason: "no-checkpoints"`) — a
freshly installed engine before the checkpoint scheduler first fires, or one running with
`--checkpoint-interval 0`. It is **not** a tamper finding and must not page anyone as one.
`--strict` still exits non-zero for it (the anchor an automation gate asked for does not exist),
with a message that says so explicitly instead of implying tampering. The `jq` gate above has
the same shape as the old two-answer verdict: `.checkpoints.OK` is `false` on a young ledger, so
on a new install prefer

```bash
olivares audit verify --tenant <TENANT_ID> --data-dir <DIR> \
  | jq -e '.status == "ok" or .status == "unattested"'
```

and switch back to the strict gate once the first checkpoint has been written.

**Advisory vs attacker-resistant.** With no `--pubkey`, verification uses the engine's **own** key (`"advisory_only": true` in the report) — meaningless against a host that holds that key. The real check pins an **off-box-retained** key:

```bash
# Pin the key you exported when healthy (GET /v1/audit/pubkey), kept OUTSIDE the box:
olivares audit verify --tenant <TENANT_ID> --data-dir <DIR> --strict \
  --pubkey <BASE64_ED25519>            # or --pubkey-alg ecdsa-p256-sha256|… for an off-box KMS key
```
(Postgres deployments: add `--engine postgres --dsn <DSN>`.)

## Diagnose — read the `Reason` and the first-bad sequence

| Section | `Reason` value | Means | Class |
|---|---|---|---|
| `chain` | `hash-mismatch` / `prev-mismatch` | a row's content or its link was altered | **TAMPER** |
| `chain` | `seq-gap` | an event was deleted | deletion / bad restore |
| `chain` | `tail-truncated` / `head-mismatch` | the tail is shorter than the recorded head | truncation / DB-write attacker |
| `checkpoints` | `checkpoint-sig-invalid` | a checkpoint signature doesn't verify against the key | **TAMPER** / wrong key |
| `checkpoints` | `checkpoint-link-mismatch` | history was rewritten before a checkpoint | **TAMPER** |
| `checkpoints` | `no-checkpoints` | nothing has been attested yet (`status: "unattested"`) | **NOT tamper** — young ledger or scheduler off. See the caveat below |
| `event_sigs` | `event-sig-invalid` | a per-event signature is bad | **TAMPER** / wrong key |
| `event_sigs` | `event-sig-missing` | an event has no signature | legacy pre-signing events **or** stripping |

`BreakAt` / `FirstBadSeq` is the first bad sequence; `LatestAttestedSeq` (checkpoints) is the highest seq a valid checkpoint attests.

> **Caveat on `no-checkpoints` — it is calm, not proven safe.** A ledger TRUNCATED below its
> first checkpoint also lands here: zero checkpoints, and the surviving prefix still verifies.
> Verification cannot tell that apart from a young ledger from the report alone (this is not new
> — the endpoint has always answered `ok: true` for it). If a ledger you know was attested now
> reports `no-checkpoints`, treat it as **truncation** and go to step 3 of Mitigate: the check
> that settles it is a pinned **off-box** checkpoint with a higher `LatestAttestedSeq`.

## Mitigate
1. **Do not delete or "repair" the chain.** Snapshot the DB and the report — they are the evidence.
2. **TAMPER class (hash/prev/sig-invalid/link-mismatch):** declare SEV1 security incident (`docs/STATUS-AND-INCIDENT-COMMS.md`). The host may be compromised — rotate operator/host credentials, preserve the box, and reconcile against your off-box checkpoint: a tamper that also rewrote the unsigned `audit_heads` row passes a naive chain walk, so the **off-box checkpoint with a higher `LatestAttestedSeq`** is what proves rollback/rewrite.
3. **Restore/rollback anomaly (`seq-gap`/`tail-truncated`/`head-mismatch`):** a *consistent older snapshot* passes chain verify silently — only a pinned off-box checkpoint with a higher attested seq detects it. If you restored an old DB or failed over to a lagging replica, re-attach the correct volume and re-run verify; re-checkpoint once consistent. **If you cannot re-attach the correct volume** (the tail is genuinely broken and the control plane is up), use the guided, dual-controlled quarantine + off-box re-anchor: **[ledger-recovery.md](ledger-recovery.md)** — it never deletes a row; it seals a documented recovery epoch forward.
4. **`event-sig-missing` only:** distinguish legacy pre-signing events from stripping — verify from the known signing-enablement sequence with `--from`/compare against `FirstBadSeq`. Legacy events below that seq are expected; missing signatures *above* it are stripping → treat as tamper.

## Verify
`olivares audit verify --tenant <id> --strict --pubkey <off-box-key>` exits 0 and the report is all-`OK: true`.

## Prevent
- **Pin off-box now:** fetch `GET /v1/audit/pubkey` while healthy and store the key + the current `LatestAttestedSeq` outside the box; that pair is what makes verification attacker-resistant and rollback-detecting.
- **Off-box checkpoint signing:** set `OLIVARES_LEDGER_SIGNER=aws-kms|gcp-kms|azure-kv` (+ the provider vars in `cmd/olivares/ledgersigner.go`) so the anchor is signed by a key the host doesn't hold. Note a KMS outage makes checkpoints silently fail (logged WARN, retried) — the anchor goes stale without a metric today (instrumentation roadmap, `docs/17 §5`).
- **Schedule the strict check** (cron) and alert on its non-zero exit.
- **Back up `<data-dir>/audit-signing.key`** off-box at provisioning — see [key-rotation.md](key-rotation.md); losing it makes history unverifiable.
