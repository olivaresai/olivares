// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package s3content is the object-storage knowledge DATA connector: it ingests
// object CONTENT (S3 / R2 / GCS objects) for module VIII, read-only, with
// the bucket/prefix, object ACL grants and tags as provenance and source
// permissions.
//
// It is DISTINCT from the s3-cloudtrail connector, which audits R/RW ACCESS
// to S3 from CloudTrail logs: this one ingests the object CONTENT itself for
// knowledge (Kind == ClassDocument), not access edges. It implements
// contentsource.Source (NOT sdk.SourceConnector); it parses an object-listing +
// content native shape (exported to a JSON file/directory) or reads live through
// the S3-compatible XML API with SigV4-signed GET requests only. Credentials are
// held in memory only; the object body is returned raw for the knowledge module
// to redact (docs/SECURITY-HARDENING.md-4). Imports only the SDK + connector-internal helpers,
// never /core.
//
// Live mode requires the read-only IAM permissions s3:ListBucket, s3:GetObject,
// s3:GetObjectAcl and s3:GetObjectTagging on the configured bucket/prefix. A
// credential missing the ACL/tagging reads fails the ingest of the object
// (deny-closed): a document is never indexed without its true source ACL and
// classification, so a minimal-privilege credential must include all four.
package s3content
