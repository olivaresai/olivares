<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Day-2 operations drill log

The single evidence page that a control-plane operator (or a procurement reviewer) can point at to see that
day-2 works end to end: **backup/restore, upgrade/rollback, key-rotation posture, corrupt-ledger recovery,
and the redacted support bundle** — each with the command that proves it and the **real numbers a run printed
on this host**, not targets. Cross-links, does not duplicate: DR detail lives in
[DR-RUNBOOK.md](DR-RUNBOOK.md), the runbooks in [../deploy/runbooks/](../deploy/runbooks/).

> **Honesty rules (repo policy):** every number below came from running the command on this container; none
> is invented. "Measured" for a wall-clock drill means seconds; for the upgrade matrix it means
> *paths-proven* (a correctness matrix, not a stopwatch); for key rotation it is a *posture statement*, not a
> number. Where a guarantee is qualitative or bounded, it says so.

**Host of record:** this reference build container — Linux 7.0.14-arch1-1, go 1.26.4, 16 vCPU, 2026-07-15.
Re-run each command on YOUR hardware and record your own numbers for a procurement pack.

## 1. Backup / restore (DR) — measured RTO

`go run ./cmd/olivares dr drill --events N` performs a full round trip in a throwaway dir (seed a signed,
checkpointed ledger → back up → destroy → restore into a clean dir → re-verify chain + per-event signatures +
checkpoints + tip continuity) and prints the **measured RTO (restore + boot + verify)**. Non-zero exit = DR
incident. The restore is verify-bound, so RTO scales ~linearly with the event count on top of a fixed boot
cost:

| Ledger events restored | Measured RTO |
|---|---|
| 500 | 123 ms |
| 2 000 | 211 ms |
| 5 000 | 388 ms |
| 10 000 | 663 ms |
| 20 000 | 1.257 s |

Full RPO/RTO tiers and the procedure: [DR-RUNBOOK.md §5, §7–§8](DR-RUNBOOK.md). Postgres PITR is
CLI/runbook-orchestrated by design (`core/dr/dr.go`).

## 2. Upgrade / rollback / downgrade — correctness matrix (paths-proven, not wall-clock)

`go test -run 'TestUpgradeE2E|TestUpgradeInstallTimer' ./cmd/olivares` — **PASS (12 e2e paths + install-timer)**.
This is a *correctness* matrix: it proves the guarantees hold, not a duration. Paths proven:

- community happy path (no license, no token) · enterprise happy path (license + token via gate)
- air-gap bundle (100% offline) · `--check` verifies without swapping · up-to-date is a no-op
- tampered artifact aborts (binary untouched) · tampered manifest signature aborts · no-key fails closed
- no-license refuses enterprise with guidance · **anti-rollback: blocked, then audited force**
- **broken new binary auto-rolls-back** to the previous · min-version gate refuses too-old a jump

Anti-rollback + audited force-rollback + exec-probe auto-rollback: `cmd/olivares/cmd_upgrade.go`;
expand/contract migrations with a real `Revert`: `core/migrate/migrate.go`. Cross-contract *downgrade* is
manual by design (documented in [UPGRADE-AND-ROLLBACK.md](UPGRADE-AND-ROLLBACK.md)).

## 3. Key / secret rotation — posture (qualitative, honest)

Rotation "measured" is a **posture statement**, not a number — and the honest posture has two distinct paths:

- **Off-box checkpoint key (KMS/HSM) — the only chain-continuous anchor.** `OLIVARES_LEDGER_SIGNER=aws-kms|
  gcp-kms|azure-kv` signs the periodic checkpoints with a key the host never holds; an off-box auditor
  verifies the whole chain across a key change via the pinned public key. This is the control that survives a
  host compromise and is what corrupt-ledger recovery (§5) re-anchors from.
- **On-box per-event Ed25519 signing key — NO chain-continuous rotation.** `olivares keys rotate`
  (`cmd/olivares/cmd_keys.go`) mints a new sealed signing key carrying the prior public keys as history, so
  *verification* stays continuous (the verifier pins current + prior pubkeys). **Honest limit:** it does NOT
  revoke trust in the retired key and has no per-sequence fencing — so "rotation exists" (envelope history)
  and "there is no chain-continuous on-box rotation" (revocation/fencing) are both true depending on the
  property meant. `keysRotateCmd`'s own help states this; `deploy/runbooks/key-rotation.md` is the SEV1/planned
  procedure. Sealed-secret value rotation: `olivares secrets rotate` (`cmd_secrets.go`); KEK re-wrap:
  `olivares keys rewrap` (`RewrapSealed`).

## 4. Security / advisory pipeline drill

`go run ./cmd/olivares security drill` (PSIRT pipeline: producer → signed feed → offline affected/patched/
boundary checks → tamper + wrong-key refusals) — **PASS, measured end-to-end 63 ms** (produce 11 / affected 9
/ patched 10 / below-introduced 11 / tamper 10 / wrong-key 10 ms). Detail: `docs/PSIRT-RUNBOOK.md §7`.

## 5. Corrupt-ledger recovery — `olivares audit recover` (NEW)

Guided, dual-controlled recovery of a corrupt audit tail, **A+ sealed-fork-forward**: it never mutates the
append-only ledger — it appends one off-box-signed `audit.recover` epoch marker and re-anchors from the last
verified off-box checkpoint; `verify` from genesis stays a documented incident (honest scar), the current
epoch verifies green from the marker. Runbook: [../deploy/runbooks/ledger-recovery.md](../deploy/runbooks/ledger-recovery.md).

**Test evidence** (`cmd/olivares/cmd_audit_recover_test.go`, `core/audit/recover_test.go`): chain-broken →
recovered → current-epoch green; deny-closed refusals (no checkpoint / wrong pin / no break / on-box-only
signer); reserved-action rejection of a forged marker; epoch-aware `verify` distinguishing `epoch_ok` /
`recovered` / `corrupt`. **Security hardening:** two adversarial-review rounds (7 findings — replay-forward,
truncation-to-marker, forged markers, Postgres TOCTOU, …) closed, each with a regression test reproducing the
attack and asserting rejection.

## 6. Redacted support bundle — `olivares support bundle` (NEW)

Assembles a redacted diagnostic bundle (effective config / status / logs / manifests / verify reports) and a
signed manifest into a 0600 tarball, for sharing with support/IR. **No-leak by construction:** a strict
diagnostic allowlist (key/signing files and data-dir blobs can never enter), secret references
(`file:`/`env:`/`store:`) recorded verbatim and never resolved, secret inventory limited to name/hint
metadata, and a **fail-closed final guard** that refuses to emit any entry still carrying a catalog-shaped
secret/PII. Runbook: [../deploy/runbooks/support-bundle.md](../deploy/runbooks/support-bundle.md).

**Test evidence** (`cmd/olivares/cmd_support_test.go`): a sentinel seeded into every channel (inline config
value, `store:` reference, log line, status, verify report — plus planted `*-signing.key`/`secret-store.key`/
TLS keys) never appears in any tar entry; the reference stays literal; the manifest records the redactions.
**Security hardening:** adversarial review drove fixes to the shared redaction/classifier catalogs
(`modules/security`, `core/secret`) — PEM block redaction, bare `*_KEY` classification, and a guard that is a
true fixed-point of the redactor — benefiting the egress DLP paths too. *Honest limit:* redaction is
shape-based; an opaque high-entropy secret with no `key=value` structure in free-text, or PII outside the
catalog, cannot be detected without false-positiving on legitimate hashes/IDs — see the runbook's limits
section.

## Reproduce
```
go run ./cmd/olivares dr drill --events 5000        # RTO
go test -run 'TestUpgradeE2E' ./cmd/olivares          # upgrade matrix
go run ./cmd/olivares security drill                  # PSIRT pipeline
go test -run 'TestAuditRecover' ./cmd/olivares        # ledger recovery
go test -run 'TestSupportBundle' ./cmd/olivares       # support bundle no-leak
```
