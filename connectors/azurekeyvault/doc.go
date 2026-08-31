// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package azurekeyvault is the Olivares AI connector for Azure Key Vault and Azure
// Managed HSM. It OBSERVES the diagnostic AuditEvent logs those
// services emit — it never calls Key Vault or the HSM itself, never decrypts, and
// never reads a secret value or key material.
//
// Read-first (docs/SECURITY-HARDENING.md): the operator routes the Key Vault / Managed HSM
// diagnostic setting (category "AuditEvent") to a storage account / Event Hub /
// Log Analytics and EXPORTS those records to a file. For every audit record —
// operationName in ObjectVerb form (VaultGet, KeyGet, KeySign, KeyDecrypt, KeyWrap,
// KeyUnwrap, SecretGet, SecretSet, KeyCreate, SecretDelete, …) — it emits ONE
// model.EdgeObservation:
//
//	OriginKind "identity"  OriginRef <caller appid / objectidentifier / upn>
//	ResourceKind "azure.keyvault.key" | ".secret" | ".certificate" | ".vault"
//	ResourceRef <object URI, else the vault/HSM resourceId>
//	Mode read|write  Source "azure_diagnostic"  ToolRef <operationName>
//
// — "who used which key/secret", NOT the material. The read/write Mode is derived
// from the platform's own operationName: a get/list/crypto-use is read; a
// create/import/update/set/delete is write; an operationName outside the mapped set
// (and the Authentication op, which names no object) is skipped or ModeUnknown
// (ARCHITECTURE.md, never guessed). Both Key Vault and Managed HSM share this one
// AuditEvent plane.
//
// Minimal data (docs/SECURITY-HARDENING.md-3): the connector reads ONLY the caller identity, the
// operationName, the object/vault id and the timestamp — never the request URL
// query string (it can carry a SAS token), the request/response bodies, or any
// credential. The caller identity arrives either as a nested object
// (identity.claim, Key Vault) or as a stringified JSON blob (Managed HSM); both are
// handled. A test asserts no secret value reaches the store.
//
// Inventory: Snapshot exposes the vaults and Managed HSMs seen in the
// export as secret_store NHIs (Ref = the resourceId) — the "where secrets live"
// half. It imports only the SDK, the Apache identitysource helper and the standard
// library — never the engine (/core).
package azurekeyvault
