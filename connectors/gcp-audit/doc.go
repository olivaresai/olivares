// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gcpaudit is the read-only GCP management-plane SourceConnector for
// Olivares AI. It discovers two disjoint org-level provenance signals and emits
// them as minimal-data observations, never importing the engine (Apache-2.0
// boundary). It is the GCP half of the tri-cloud management-plane parity with the
// AWS connector: where connectors/aws reaches an AWS account for IAM
// inventory + CloudTrail, this reaches a GCP organization for Resource Manager
// inventory + Cloud Audit Logs.
//
// Inventory (signal "gcp"). Bearer-authorized Resource Manager v3 and IAM list
// passes walk the organization hierarchy (org → folders → projects, bounded
// depth) and list each project's service accounts. They emit only TOPOLOGY edges
// (org⊳folder, folder⊳project, project⊳service-account) whose refs name the
// discovered resources; the consumer materializes entities from those refs.
// These are containment, not an access, so Mode is unknown and Confidence
// attributed (directly observed via the API). The pass is METADATA ONLY: resource
// names, project ids, service-account emails and lifecycle state. It NEVER reads
// an IAM policy binding document, a service-account key, or any credential.
//
// Cloud Audit Logs feed (signal "gcp_audit"). A Bearer-authorized Cloud Logging
// entries:list pass reads recent Admin Activity and Data Access audit logs across
// the org/projects as a control-plane audit feed and emits one edge per entry:
// identity⊳gcp.api (serviceName:methodName), with the principalEmail as the
// origin and the serviceName as the tool. Mode is honest, never guessed: an Admin
// Activity entry is an administrative WRITE by the log type's own definition;
// a Data Access entry is read or write per the method verb (standard AIP-136
// naming), and an unrecognized verb is ModeUnknown. System Event and Policy
// Denied entries are skipped — a Google-initiated event is not a principal's
// action, and a denied attempt is not an observed access. Confidence is
// approximate for a declared shared principal, attributed otherwise. entries:list
// is a POST that carries the query body (resourceNames + filter + pagination); it
// performs NO mutation — the single POST-for-read exception, exactly like AWS
// CloudTrail LookupEvents.
//
// Boundary vs the service-level GCP observers. gcs-audit, gcpkms and
// bigquery-audit own the per-service DATA plane — per-object/key/table edges from
// one service's Cloud Audit Logs export. This connector stays on the org
// MANAGEMENT plane under the disjoint "gcp.api" resource namespace, so the two
// never double-ingest the same fact. (The broken reference to "a GCP audit
// feed from" is corrected by this session: Built runtime/cloudflare/aws
// only — this is the GCP plane it implied.)
//
// Identity convergence. The principalEmail is emitted verbatim as OriginKind
// "identity": for a service account it is the same name@project.iam.gserviceaccount.com
// ref google-agent emits, so the governance roster fuses them by external_id
//. The connector never resolves an identity to an agent — that bridge
// is module VI's job.
//
// Least privilege. The connector needs only read-only management metadata plus
// audit-log read: resourcemanager.{organizations,folders,projects}.get/list,
// iam.serviceAccounts.list and logging.logEntries.list. The roles
// roles/resourcemanager.organizationViewer + roles/iam.serviceAccountViewer +
// roles/logging.viewer cover it; reading Data Access entries additionally needs
// roles/logging.privateLogViewer. The OAuth scope is the single read-only
// cloud-platform.read-only. With NO credential configured the connector is
// offline: Open succeeds and Gather emits nothing. It issues no
// write/create/delete call and touches no secret, key or payload.
package gcpaudit
