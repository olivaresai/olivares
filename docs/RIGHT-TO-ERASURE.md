<!--
SPDX-FileCopyrightText: 2026 Olivares.AI
SPDX-License-Identifier: AGPL-3.0-only
-->

# Right-to-erasure (RTBF) — operator runbook

How to fulfill a GDPR Art. 17 / CCPA §1798.105 request over the control plane's own stores
without breaking the append-only ledger.
**Retention/holds:** `docs/RECORDS-MANAGEMENT.md` (the hold ALWAYS vetoes erasure, §6.1).

## 1. Before registering the DSR

1. **Verify the requester's identity** (Art. 12(6)) in your intake process and open a case
   (`case_ref` — a ticket id, never content). The legal clock is **one month** (Art. 12(3)).
2. **Gather the subject's identifiers**: the primary one (`subject_ref`) and every alias under
   which it appears in the stores (e.g. the FinOps ingest email AND the actor form
   `user:<uuid>`). An identifier you do not list is NOT erased — matching is by exact
   equality, and over-length refs are rejected, never truncated.
3. **If the subject lives in an external directory (roster)**: erase/anonymize it in the IdP
   FIRST. The roster converges from the source — otherwise the next sync re-imports its name.
4. **Check for active holds** (`GET /v1/m/compliance/holds?status=active`): if a hold covers the
   subject or its classes, the erasure returns **423** and will NOT proceed until legal releases
   the hold (release = CRITICAL, 2 humans, no break-glass).

## 2. The flow

```bash
# 1) Register the DSR (admin-tier; mint the key-ring + tokenize the subject)
POST /v1/m/compliance/erasure
{"subject_kind":"user","subject_ref":"maria@example.com",
 "aliases":["user:0193…uuid"],"case_ref":"DSR-2026-041","reason":"GDPR Art. 17"}

# 2) Execute (the governed verb). Typical first call ⇒ 202 pending_approval:
POST /v1/m/compliance/erasure/{id}/execute
{"provider_user_ids":["<claude.ai user uuid>", …]}   # optional: the Anthropic-side leg

# 3) Two distinct humans approve "compliance.subject.erase" in governance.
#    Re-execute: runs targets → account → provider → CRYPTO-SHRED → verify → RECEIPT.
POST /v1/m/compliance/erasure/{id}/execute   # ⇒ 200 with the receipt (or 202 provider_pending)

# 4) The receipt (the proof of fulfillment, append-only and anchored to the ledger):
GET /v1/m/compliance/erasure/{id}/receipt
```

- `202 provider_pending`: the Anthropic-side DELETEs EACH carry their own dual-control
  approval (connector PEP). Approve and re-execute; **the crypto-shred waits** —
  the key is never destroyed while any leg remains half-done.
- `completed_with_gaps`: an unwired leg (account/provider) or a verify that was not clean. The gap
  is RECORDED in the receipt — the system never claims an erasure it did not execute.
- `failed` state: re-executable; nothing is shredded until completion.

## 3. What each subject erases

Summary: `user` → cost samples (the actor's email + a duplicate in
cost_records), voice sessions opened by that person, the engine account (anonymized
in-place: email/name/credentials destroyed, panel sessions with IP erased, tokens
revoked and renamed) and the Anthropic-side content; `agent` → the agent's memory, live/voice
sessions; `session` → live/timeline/voice; `document` → document + chunks (the embeddings live
in the chunk's row) + discovery/DLP labels, with the KB counters adjusted; `identity` → name and
roster metadata + owner/sponsor refs of the NHI overlay.

**The engine account is REFUSED** (and the receipt says so) if: it is superadmin (a deliberate
manual ceremony) or it has memberships in OTHER tenants (one tenant's DSR cannot erase a shared
principal — coordinate a DSR per tenant or operate from system).

## 3bis. What the receipt says the verification examined

The residual scan runs after the erase pass and before the crypto-shred, and it is the only
thing on the receipt that speaks about how the verification looked and over how much. Four
fields carry it:

| field | what it means |
|---|---|
| `residual_scan_depth` | the METHOD the scan used, never a coverage claim. `registry-scoped` = the in-code erasure-target catalog restricted to the subject kind AND to this request's `data_classes`, matched by exact equality on the mapped identifier columns in the live store, plus the document row, the roster identity anchor and the cost ledger. **Absent** when the scan opened nothing: no depth was reached, so none is stated. |
| `residual_scan_applicable` | the targets this scan's METHOD covers for the request's data-class scope, as structural labels — the catalog named above restricted to the subject kind and that scope, which is not every store the erasure touched. A subject kind whose erasure CASCADES destroys stores this scan does not re-open (a document takes its chunks and its current labels with it), so a clean result here is a clean result over the targets named here. |
| `residual_scan_opened` | the targets the scan could actually open here. Always a subset of `applicable`; a target whose owning module is not registered in this deployment is applicable and NOT opened. |
| `data_classes` | the scope the two sets above are read against — narrowing a request shrinks BOTH, so a narrowed erasure can never look like a broader one. |

All four are covered by `manifest_hash`, by LABEL and not by a count: a third party re-reading
the receipt can tell a full sweep from a scan that opened two of four. Receipts sealed before
these fields existed carry them empty and keep the hash they were sealed under — nothing
recomputes a stored manifest.

**A declaration, and what it costs.** When the scan has something to say about itself it says it
once, in `verify_reason`, on a substring a consumer can match:

- `residual-scan-depth=unestablished` — the scan opened NO target for this subject kind and
  data-class scope, so no surviving identifier could have been observed. This is reachable
  today: a request may name classes that no target of its subject kind carries.
- `residual-scan-coverage=partial` — the scan opened fewer targets than the scope required, and
  the sentence names the ones it could not open.

The two conditions exclude each other, so a receipt never carries both, and a receipt that
states a depth never also denies one. **Either declaration makes `verify_ok` false and closes
the erasure `completed_with_gaps`** (CLI exit 7). That is deliberate: `verify_ok` is the only
machine-readable verification signal the receipt has, and an erasure whose verification could
not look where the request said to look is not a verified erasure. The erasure itself still
ran, and the per-target outcomes above say exactly what it did.

A third substring, `residual-rescan=unverified`, is a DIFFERENT claim: it belongs to the
optional coordinator's post-shred re-scan (§7), not to this scan, and it can appear beside a
stated depth without contradicting it.

**Where a marker is matched: on `verify_reason`, and nowhere else.** The two substrings above are
reserved for that FIELD, and a receipt carries one there exactly when the scan's own report says
so — which is why a string an optional coordinator supplies is withheld from that field when it
contains either. No other part of the receipt is a marker channel, and two in particular are not:
`account_outcome` and `provider_outcome` carry the account and provider erasers' OWN detail
verbatim, through the public seams that supply them, and are neither filtered nor reserved. So a
consumer matches the FIELD, never the receipt as a document: text that reads like a declaration
anywhere else in it is not one.

## 4. Why the ledger is not touched (and keeps verifying)

The ledger references people ONLY by pseudonyms (`user:<id>`) and hashes; direct PII never
enters by contract (docs/SECURITY-HARDENING.md). The erasure destroys all the mappings (account, key-ring, mutable
rows) ⇒ those pseudonyms become anonymous (Recital 26). The events are retained under Art.
17(3)(b)/(e) — the exact table travels in each receipt (`retained`). `/v1/audit/verify` runs
WITHIN the post-erasure flow and its result goes in the receipt (`verify_ok/checked`).

**Honest limits:**
- **Backups**: a backup taken before the shred contains the key; effective crypto-erasure
  completes when the last backup containing it expires. Align your backup window (DR
  runbook) with your Art. 12(3) deadline commitment and document it in the case.
- **Legacy PII in ledger meta:** the IP from `auth.login.failed` is retained for
  security (Art. 6(1)(f), Recital 49); `knowledge.kb.create` used to write the KB name into meta
  — already fixed (meta no longer carries the name, not even hashed: a keyless hash of a
  low-entropy name is dictionary-confirmable; the old events remain — document them if
  a DSR reaches them).
- **Provider**: erasing our copy does not erase the provider's — Covered Models forces ≥30 days
  without ZDR. The receipt cites `provider_floor_days`; it promises only `effective_disclosure_days`.
- **FinOps `sample_key`**: the scrub keeps the sample's natural key (a keyless SHA-256
  whose inputs include the erased email) so that a re-ingest dedupes onto the scrubbed row
  instead of re-creating the email — a deliberate tradeoff: whoever knows the other dimensions could
  CONFIRM a candidate email by dictionary, but not recover it. The alternative (scrubbing the
  key) re-introduces the PII on the next re-delivery. A documented decision; revisable if the
  natural key is re-keyed with HMAC (follow-up).
- **Append-only governance trails** (NHI events, approval decisions, break-glass uses):
  they reference historical owner/sponsor/actor ids and are not erasable (Art. 17(3)(b)/(e), the
  `retained` table of the receipt); after the scrub of the roster and the account, those ids no longer
  resolve to the person. The `recording_session` entries reference `subject_user` by a stable id (a
  pseudonym; the frames redact emails by design) — out of v1 scope, documented.
- **Read-tier detokenization**: while the key lives, `GET /erasure*` shows the subject in the clear
  to `compliance:erasure:read` (the DSR operator needs to know whose case it is — the same
  criterion under which holds show `subject_ref` to `hold:read`). After the shred, only `[ERASED]`.

## 5. Provisioning the Anthropic-side leg (optional)

`OLIVARES_CLAUDE_ERASER_CONFIG=/path/eraser.json` (an operator secret-file, 0600):

```json
{"delete_key": "sk-ant-api01-… (delete:compliance_user_data)",
 "read_key":   "sk-ant-api01-… (read:compliance_user_data)",
 "allowlist":  [{"target":"chat","subjects":["*"]},{"target":"project","subjects":["*"]}]}
```

Without the file (or without an approvals bridge) the leg stays `not_wired` — honest, never
silent. The DELETEs are PERMANENT and IMMEDIATE (no recovery window); a project
with attached chats returns 409 (the orchestrator erases chats first). Shared rate
600 req/min per parent org (250ms pacing included).

## 6. Auditing the fulfillment

- `GET /erasure/{id}/events` — append-only custody anchored to the ledger (who, when, under what
  approval, with what quorum).
- The receipt is the evidence for the DPO/auditor: outcomes per target with counts, legs, floor,
  shred, verify and reconciliation. The `rtbf_erasure` capability (GDPR `art_17`) moves to
  *satisfied* ONLY when ≥1 real receipt exists — an unexecuted workflow is an honest *gap*.

## 7. Enterprise depth: the crypto-shred coordinator (optional)

The enterprise build can wire an RTBF-depth coordinator (`OLIVARES_RTBF_DEPTH_CONFIG=/path.json`)
that adds policy readiness, WORM-sink coordination and an independent completeness verdict on top
of the open workflow above — which is complete on its own and unchanged without it.

What it changes, honestly:

- **Readiness (pre-shred)**: the coordinator re-checks the LIVE legal-hold plane and, with
  `worm_coordination` enabled, requires a configured WORM archive sink
  (`OLIVARES_AUDIT_ARCHIVE_SINK`) BEFORE the irreversible key destruction. A blocked readiness
  returns 423 with the blockers.
- **Key-invalidation attestation**: on shred, every WORM sink receives a durable, checksummed
  `rtbf/key-invalidations/<key>.json` object and must acknowledge it (the sink's write receipt).
  A sink that does not acknowledge FAILS the erasure transaction — the shred rolls back.
- **Verification with evidence**: the coordinator's verdict comes from executed checks — the
  destroyed key row is re-probed and the residual scan re-runs inside the shred transaction.
  Anything it cannot verify appears in the receipt as an explicit `unverified: [...]` reason and
  the erasure closes with `gaps`, never a rounded-up "complete".
- **It says nothing about depth.** A coordinator reports on its own post-shred re-scan and
  never writes the receipt's `residual_scan_*` fields; a depth it sets is not published. Those
  fields come from the pre-shred scan of §3bis in every build, so the same input seals the same
  depth and the same two label sets with or without a coordinator wired.
- **Deny-closed**: a missing/invalid config file blocks shreds entirely (a deny-all coordinator);
  an unset env keeps the community behavior byte-identical.
