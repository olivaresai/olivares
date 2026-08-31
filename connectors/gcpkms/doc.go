// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gcpkms is the Olivares AI connector for Google Cloud KMS and Secret
// Manager. It OBSERVES the Cloud Audit Logs those services emit —
// it never calls Cloud KMS or Secret Manager itself, never decrypts, and never
// reads a secret value or key material.
//
// Read-first (docs/SECURITY-HARDENING.md): the operator EXPORTS Cloud Audit Logs (Cloud Logging
// LogEntry records whose protoPayload is a google.cloud.audit.AuditLog) to a file
// — typically NDJSON. For every Cloud KMS entry (serviceName cloudkms.googleapis.com
// — Decrypt, Encrypt, AsymmetricSign, AsymmetricDecrypt, MacSign, MacVerify,
// GetPublicKey, CreateCryptoKey, …) and every Secret Manager entry (serviceName
// secretmanager.googleapis.com — AccessSecretVersion, AddSecretVersion, GetSecret,
// CreateSecret, DeleteSecret, DestroySecretVersion, …) it emits ONE
// model.EdgeObservation:
//
//	OriginKind "identity"  OriginRef <authenticationInfo.principalEmail>
//	ResourceKind "gcp.kms.key" | "gcp.secret"   ResourceRef <resourceName>
//	Mode read|write  Source "gcp_audit"  ToolRef <methodName>
//
// — "who used which key/secret", NOT the material. The read/write Mode is derived
// from the platform's own method: a key/secret USE or get/list is read; a
// create/add/update/delete/destroy is write; a method outside the mapped set is
// ModeUnknown (ARCHITECTURE.md, never guessed). Crypto-use ops (Decrypt/AsymmetricSign/
// AccessSecretVersion) are logged by GCP as DATA_READ — which the operator must
// EXPLICITLY enable (Data Access audit logs are off by default); this connector
// reads only what the exported log contains and never enables anything itself.
//
// Minimal data (docs/SECURITY-HARDENING.md-3): the connector reads ONLY principalEmail, methodName,
// resourceName and the timestamp — never request/response, status, or the
// AccessSecretVersion payload. A test asserts no secret value reaches the store.
//
// Inventory: Snapshot exposes the Cloud KMS and Secret Manager
// custodians it can SEE in the export — one secret_store NHI per (service, project)
// — as the "where secrets live" half of the unified inventory; the keys/secrets are
// resources on the edges. It imports only the SDK, the Apache identitysource helper
// and the standard library — never the engine (/core).
package gcpkms
