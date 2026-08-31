// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gcsaudit is the gcs-audit source connector: object-store R/RW capture
// for Google Cloud Storage, the GCP parity of s3-cloudtrail. It parses
// Google Cloud Audit Logs (Cloud Logging entries for the storage.googleapis.com
// service, the Data Access "data_access" audit log) and emits one minimal-data
// EdgeObservation per object/bucket access.
//
// # The platform
//
// Google Cloud Storage emits Cloud Audit Logs as Cloud Logging LogEntry objects
// whose protoPayload is a google.cloud.audit.AuditLog. The operator EXPORTS those
// entries to a file the connector reads — typically a Cloud Logging sink to a GCS
// bucket or a `gcloud logging read --format=json` / log-router export, written as
// newline-delimited JSON (NDJSON), one LogEntry per line. The connector reads that
// exported file; it is the honest ingest path. It does NOT call the Cloud Logging
// API, open a GCS connection, authenticate to GCP, or use any cloud SDK.
//
// # Security posture (docs/SECURITY-HARDENING.md)
//
//   - Read-first: the connector only READS the operator-exported audit file
//     (read-only, via logtail). It never connects to GCS or Cloud Logging, never
//     authenticates to GCP, and never writes anywhere.
//   - Minimal-data: it emits ONLY the access edge — origin identity, resource,
//     R/RW mode, source, confidence, tool ref, timestamp. The record struct
//     declares only the fields it reads; request/response bodies, object contents,
//     query parameters and any payload are never parsed, never emitted, never
//     logged. A NoRawLeak test asserts no body fragment reaches an emitted field.
//   - Identities, not credentials: OriginRef is the principal's email
//     (authenticationInfo.principalEmail) — an identity, never a credential or
//     token. OriginKind is always "identity": the audit attributes the access to a
//     principal/service account, not to a resolved agent (the identity↔agent
//     bridge is module VI / not this connector).
//
// # R/RW mapping (verbatim from methodName)
//
// The mode is derived from the platform's own methodName, never guessed:
//
//	storage.objects.get     -> read
//	storage.objects.list    -> read
//	storage.buckets.get     -> read
//	storage.buckets.list    -> read
//	storage.objects.create  -> write
//	storage.objects.delete  -> write
//	storage.objects.update  -> write
//	(any other methodName)  -> unknown   (explicit, never guessed; ARCHITECTURE.md)
//
// ResourceKind is "gcs.object" (ResourceRef "gs://BUCKET/OBJECT", parsed from
// resourceName "projects/_/buckets/BUCKET/objects/OBJECT") or "gcs.bucket"
// (ResourceRef "gs://BUCKET"). ToolRef is the raw methodName. ObservedAt is the
// entry's own top-level timestamp, normalized to UTC (never time.Now()). A
// principal declared in shared_accounts yields Confidence=approximate (the raw
// identity is still emitted; only the trust drops); otherwise attributed.
package gcsaudit
