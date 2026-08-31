// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package awskms is the Olivares AI connector for AWS KMS and AWS Secrets Manager
//. It OBSERVES the audit trail those services already emit to
// AWS CloudTrail — it never calls KMS or Secrets Manager itself, never decrypts,
// and never reads a secret value or key material.
//
// It is read-first (docs/SECURITY-HARDENING.md): the operator EXPORTS CloudTrail to a file or
// directory (the same S3/CloudTrail export feed s3-cloudtrail consumes) and
// this connector parses it. For every KMS cryptographic/management event
// (eventSource kms.amazonaws.com — Decrypt, Encrypt, GenerateDataKey, Sign, Verify,
// ReEncrypt, CreateKey, ScheduleKeyDeletion, …) and every Secrets Manager event
// (eventSource secretsmanager.amazonaws.com — GetSecretValue, PutSecretValue,
// CreateSecret, UpdateSecret, DeleteSecret, RotateSecret, DescribeSecret, …) that
// names a key or a secret, it emits ONE model.EdgeObservation:
//
//	OriginKind "identity"  OriginRef <IAM principal ARN/id from userIdentity>
//	ResourceKind "aws.kms.key" | "aws.secret"   ResourceRef <key/secret ARN>
//	Mode read|write  Source SignalCloudTrail  ToolRef <eventName>
//
// — "who (which NHI) used which key/secret", NOT the material. The read/write Mode
// is taken VERBATIM from the record's own readOnly flag (KMS sets it: crypto ops
// are readOnly=true, CreateKey/ScheduleKeyDeletion are readOnly=false) and, when a
// Secrets Manager record omits it, from the operation verb (Get/Describe/List =
// read; Create/Put/Update/Delete/Rotate = write). An event AWS does not classify
// yields ModeUnknown — never a guess (ARCHITECTURE.md).
//
// Minimal data (docs/SECURITY-HARDENING.md-3): AWS itself excludes SecretString/SecretBinary and
// the KMS Plaintext from CloudTrail, and this connector reads ONLY the identity,
// the operation name, the key/secret ARN and the timestamp — never a payload, a
// request/response body, a grant token or any credential. There is a test that the
// store never receives a secret value.
//
// Inventory: Snapshot exposes the AWS KMS and Secrets Manager
// custodians it can SEE in the export — one secret_store NHI per (service,
// account, region) — as the "where secrets live" half of the unified secret-manager
// inventory (the keys/secrets themselves are resources carried on the edges). It is
// existence (the store exists) distinct from use (the OBSERVED edge); a store seen
// only in inventory and never in an edge means the audit trail did not record a use
// of it, which the contract states honestly.
//
// It imports only the SDK, the Apache identitysource/internal helpers and the
// standard library — never the engine (/core).
package awskms
