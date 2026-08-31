// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package audit builds on the append-only, hash-chained evidence ledger
// (store.AuditLog) with three integrity controls:
//
//   - Per-event Ed25519 signing (SignEvent / VerifyEvents). The engine signs
//     EVERY event over its chain hash at write time (wired via
//     store.Config.SignEvent / store.AuditEventSigner). The signature is OUTSIDE
//     the chain-hash preimage (it lives in the event's sig column), so signing
//     cannot perturb the hash it attests. This makes the tail un-rewritable
//     without the private key even BETWEEN checkpoints (or after the checkpoints
//     are deleted): every event is its own anchor.
//
//   - Ed25519 checkpoint signing. A checkpoint is itself an audit event
//     (action "audit.checkpoint") whose detached signature notarizes the chain
//     tip as of immediately before it (the event's PrevHash at sequence Seq-1).
//     Because an attacker cannot re-sign, any rewrite of history before a
//     checkpoint invalidates it. VerifyCheckpoints validates every present
//     checkpoint; per-event signatures are verified separately by VerifyEvents.
//
//   - Export. CEF (ArcSight), RFC5424 syslog and an OTLP-logs JSON projection,
//     each carrying the chain-integrity fields (seq, prev_hash, hash, sig) so an
//     external WORM/SIEM can hold an independently verifiable copy (docs/SECURITY-HARDENING.md).
//     Export is a manual/operator-scheduled action, NOT auto-enabled (that would
//     violate the no-telemetry-home default); it only compensates once the copy
//     actually leaves the host AND the verifier pins the expected latest-attested
//     state off-box. Email/PII is never exported.
//
// Honest limits (docs/SECURITY-HARDENING.md): a local-disk signing key gives NO protection
// against a full-data-dir (root/host) compromise, which holds both the database
// and the key and can re-sign. Per-event and checkpoint signatures defend the
// database-ONLY attacker (SQL injection, a stolen backup/replica, an RLS-bypass
// role) and checkpoint deletion. Against the host-compromise attacker the
// controls are off-box public-key verification (`audit verify --pubkey`) of a
// RETAINED off-box copy, plus a KMS/HSM/offline-signer seam (the Signer takes any
// ed25519.PrivateKey, so an HSM-backed key drops in).
package audit
