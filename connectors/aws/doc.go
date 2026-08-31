// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package aws is the read-only AWS SourceConnector for Olivares AI. It discovers
// two disjoint provenance signals from one AWS account and emits them as
// minimal-data observations, never importing the engine (Apache-2.0 boundary).
//
// IAM inventory (signal "aws"). A SigV4-signed Query/XML pass over the global IAM
// endpoint lists roles, users and policies and the policy attachments per role.
// It emits only TOPOLOGY edges (account⊳role, account⊳user, account⊳policy,
// role⊳attached-policy) whose refs name the discovered identities; the
// consumer materializes entities from those refs. These are containment, not an
// access, so Mode is unknown and Confidence attributed (directly observed via the
// API). The pass is METADATA ONLY: names, ARNs, paths, ids and dates. It NEVER
// reads or emits a policy DOCUMENT (no GetRolePolicy/GetPolicyVersion), an access
// key, a secret, or any credential material — those are out of scope by design.
//
// CloudTrail feed (signal "cloudtrail"). A SigV4-signed JSON LookupEvents pass
// reads recent MANAGEMENT events as an account-level control-plane audit feed and
// emits one edge per event: identity⊳aws.api (eventSource:eventName), with Mode
// derived from the event's readOnly flag and Confidence from whether a distinct
// principal ARN is attributable. LookupEvents returns only management events by
// AWS design; any Data-category record is defensively skipped.
//
// Boundary vs the datastores connector. Owns the S3 DATA plane —
// per-object read/write edges from S3 data events. This connector stays on the
// CONTROL plane: account-level management activity under the "aws.api" resource
// namespace, which is disjoint from per-object data edges. The two never
// double-ingest the same fact; a Data-category CloudTrail record here is dropped.
//
// Least privilege. The connector needs only read-only IAM list metadata plus
// CloudTrail lookup: iam:ListRoles, iam:ListUsers, iam:ListPolicies,
// iam:ListAttachedRolePolicies and cloudtrail:LookupEvents. The AWS-managed
// SecurityAudit or ReadOnlyAccess policies cover it; a custom policy with exactly
// those five actions is preferred. It issues no write/create/delete/exec call and
// touches no secretsmanager, kms or s3 data API.
package aws
