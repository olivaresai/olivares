// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package kmswrap implements the secure.KeyWrapper seam against real cloud
// KMS KEKs: AWS KMS (Encrypt/Decrypt), Google Cloud KMS (encrypt/decrypt on a
// symmetric key) and Azure Key Vault / Managed HSM (wrapKey/unwrapKey,
// RSA-OAEP-256). It is the CMEK half of the key-custody layer: the KEK
// these backends talk to wraps only DEKs (32 bytes), never payloads — see
// core/secure/envelope.go for the envelope shape and the honest threat model.
//
// Like core/audit/kmssign (whose patterns this package deliberately mirrors —
// injectable Doer, per-request bearer TokenSource, hand-rolled SigV4 from
// core/internal/sigv4, no response-body logging), it is pure-Go REST: wiring a
// KEK never adds cgo or a cloud SDK to the engine binary. It is custody
// infrastructure inside the AGPL core, NOT a reusable observation connector —
// the connectors/{awskms,gcpkms,azurekeyvault} packages are read-only log
// observers and never call a KMS.
//
// A PKCS#11/native HSM KEK is the same out-of-process story as the ledger
// signer's: a sidecar behind the secure.KeyWrapper seam, or a KMS that is
// itself HSM-backed (CloudHSM-backed custom key stores / Managed HSM) — never
// compiled in.
package kmswrap
