// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package kmssign provides OFF-BOX checkpoint signers for the evidence ledger
// (R5): AWS KMS, Google Cloud KMS and Azure Key Vault backends
// that implement audit.CheckpointKey by calling each cloud's Sign API over REST.
// The ledger's private key never lives on the host, so a checkpoint — the
// cross-time tamper-evidence anchor an off-box verifier pins — cannot be forged
// even by a host-compromised attacker (docs/SECURITY-HARDENING.md).
//
// It is PURE-GO (CGO_ENABLED=0): AWS auth is a hand-rolled SigV4 (crypto/hmac +
// crypto/sha256), GCP and Azure use a pluggable bearer-token source. Nothing here
// uses cgo, so wiring an off-box signer never drags a native dependency into the
// static engine binary — the same constraint that keeps SQLCipher out of the core
//. A native PKCS#11/HSM device (which DOES need cgo) is reached
// out-of-process or via a KMS that is itself HSM-backed (CloudHSM / Managed HSM /
// a KMS key whose ORIGIN is the HSM), behind this same audit.CheckpointKey seam —
// never compiled in.
//
// Each backend signs the canonical checkpoint preimage by hashing it with the
// algorithm's hash and calling the cloud Sign API in DIGEST mode, returning the
// detached signature. PublicKey fetches (and caches) the key's public half as
// DER SubjectPublicKeyInfo, which an auditor exports once and pins for the off-box
// `audit verify --pubkey --pubkey-alg …` check — with NO cloud dependency in the
// verify path. The default cross-cloud algorithm is ECDSA P-256 (every provider
// supports it; AWS has no Ed25519 over the X9.62 ledger path we use, GCP/Azure
// vary), but a provider-native algorithm can be selected.
//
// It lives in /core (AGPL) because it is part of the ledger SIGNER — integrity
// infrastructure, not a reusable observation connector — and so it may reference
// audit.SigAlg / audit.CheckpointKey directly. The composition root constructs a
// backend from operator config and passes it via audit.WithCheckpointKey; the
// default on-box Ed25519 signer is unchanged when none is configured.
package kmssign
