<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Runbook — guided recovery of a corrupt audit ledger (`audit recover`)

**When this fires:** [ledger-verify-failure.md](ledger-verify-failure.md) has classified a **restore/rollback
anomaly or a confirmed tail break** (`prev-mismatch` / `hash-mismatch` / `tail-truncated` / `head-mismatch`)
and you have decided you cannot simply re-attach the correct volume (that path is step 3 of
ledger-verify-failure.md). This runbook is the **guided, dual-controlled** way to resume a verifiable ledger
**without** a full estate restore — when the control plane is UP but one tenant's chain is broken at the tail.

> **What it is NOT.** This is not "repair the chain". The append-only ledger (`audit_events`) is physically
> immutable — SQLite `no_update`/`no_delete` triggers, Postgres `_immutable` trigger + revoked
> `UPDATE/DELETE/TRUNCATE`. **No row is ever moved or deleted.** Recovery *seals a new epoch forward* (semantics
> "A+ fork-forward"): it appends one **off-box-signed `audit.recover` marker** that documents the break as a
> resolved incident and re-anchors trust from the last verified off-box checkpoint. The corrupt tail stays in
> place as evidence. Consequently **`verify` from genesis stays RED at the break — that is the honest scar**;
> the *current epoch* verifies green from the marker forward.
>
> **Boundary:** `audit recover` is an ONLINE, control-plane-UP operation (it needs the governed-approval loop).
> If the engine/DB is down or you are rebuilding bare metal, that is **DR restore** ([DR-RUNBOOK.md](../../docs/DR-RUNBOOK.md)),
> not this.

## Preconditions (all required — the tool is deny-closed and refuses without them)
1. A **real structural break** exists (`audit verify` shows `BreakAt > 0`).
2. The **operator-pinned off-box public key** (the one you exported when healthy: `GET /v1/audit/pubkey`,
   kept OUTSIDE the box). The tool **never** falls back to the engine's own key.
3. At least one **valid off-box checkpoint** whose `LatestAttestedSeq` is **before** the break
   (`cr.Checkpoints > 0`, `LatestAttestedSeq < BreakAt`). No checkpoint ⇒ no anchor ⇒ recovery is refused.
4. The engine is running with an **off-box checkpoint signer** configured (`OLIVARES_LEDGER_SIGNER=aws-kms|
   gcp-kms|azure-kv`, per `cmd/olivares/ledgersigner.go`) — the marker and the re-notarising checkpoint are
   signed off-box.
5. **Two distinct human approvers** via the governed-approval loop (this action is classified CRITICAL with a
   two-person floor and **no break-glass**).

## Detect / diagnose
Run the pinned, attacker-resistant check first (this is the same command the drill and CI cron use):
```
olivares audit verify --tenant <TID> --pubkey "<alg>:<base64 off-box SPKI>" --strict
# JSON: status, chain{OK,BreakAt,Reason}, checkpoints{OK,LatestAttestedSeq}, recovery_markers, event_sigs
```
Read `chain.Reason` and `chain.BreakAt` (see ledger-verify-failure.md §Diagnose). If the class is TAMPER,
declare the SEV1 first (host may be compromised) — recovery re-anchors trust, it does not clear an incident.

## Mitigate — the guided recovery ceremony

**Step 1 — dry-run (default; changes nothing).** This runs every deny-closed check and prints the plan
(break, reanchor_seq, off-box key id, the quarantined range + its SHA-256) without appending anything:
```
olivares audit recover --tenant <TID> \
  --pubkey "<alg>:<base64 off-box SPKI>" \
  --reason "restore anomaly: tail-truncated at <seq>" \
  --requested-by "you@org"
# (add --archive-dir <off-box copy> to also require the WORM archive to verify and cover [1..reanchor_seq])
```
Inspect the JSON: confirm `chain.BreakAt`, `reanchor_seq` (= the last off-box-attested good seq), and the
`quarantined_from/to` range are what you expect.

**Step 2 — obtain the two-person approval.** The dry-run's `gate` block names the plan-hash the approval must
bind. Two distinct humans approve it in the governed-approval console (`/v1/m/governance/approvals`). There is
no emergency bypass for this action.

**Step 3 — execute.** Re-run with `--dry-run=false`; you will be prompted to confirm. The tool, in one
transaction (holding the tenant append lock so a concurrent write cannot shift the marker):
re-checks every plan-bound fact, appends the signed `audit.recover` marker at `reanchor_seq`-adjacent tail,
records the approvers + the SHA-256 of the quarantined range as evidence, **cuts a fresh off-box checkpoint
over the new epoch head**, and prints a post-recovery proof. If the off-box checkpoint cannot be cut it
reports `status: unattested` and fails — re-run `olivares audit checkpoint` and retry; a marker whose epoch
head is not off-box-attested is deliberately **not** honored as recovered.

## Verify (the recovery worked)
```
# Current epoch is green (this is the proof the tool prints):
olivares audit verify --tenant <TID> --from <recover_seq> --pubkey "<alg>:<off-box SPKI>" --strict   # exit 0, status=epoch_ok

# Genesis stays a DOCUMENTED incident, not a bare break:
olivares audit verify --tenant <TID> --pubkey "<alg>:<off-box SPKI>"                                  # status=recovered
#   --strict here STILL exits non-zero on purpose: the genesis scar is real and permanent.
```
Point your on-call `audit verify --strict` cron at `--from <recover_seq>` for this tenant (the clean current
epoch) and record the recovery incident (recover_seq, approvers) in your incident log.

## Prevent
- **Pin and keep the off-box public key** off the box (you cannot recover without it — by design).
- **Off-box checkpoint signing on, cut frequently.** Truncation is only detectable down to the last off-box
  checkpoint; a stale-checkpoint window is an undetectable-truncation window (see the honest limit in
  ledger-verify-failure.md §Prevent and `docs/17 §5`).
- Recovery is a break-glass-grade event: it requires two humans and leaves signed evidence. Treat a
  `status: recovered` ledger as an open, resolved-incident record in your compliance evidence — it is
  *stronger* evidence than a silently re-greened chain, which this product deliberately does not produce.
