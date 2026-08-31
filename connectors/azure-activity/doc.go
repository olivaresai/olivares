// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package azureactivity is the read-only Azure management-plane SourceConnector
// for Olivares AI. It discovers two disjoint provenance signals across an Azure
// tenant's subscriptions and emits them as minimal-data observations, never
// importing the engine (Apache-2.0 boundary). It is the Azure half of the
// tri-cloud management-plane parity with the AWS connector: where
// connectors/aws reaches an AWS account for IAM inventory + CloudTrail, this
// reaches an Azure tenant for Resource Graph inventory + the Activity Log
//.
//
// Inventory (signal "azure"). A Bearer-authorized Resource Graph projection query
// (Resources | project id, subscriptionId) across the scoped subscriptions, plus
// the tenant→subscription mapping, emits only TOPOLOGY edges (tenant⊳subscription,
// subscription⊳resource) whose refs name the discovered resources; the
// consumer materializes entities from those refs. These are containment, not an
// access, so Mode is unknown and Confidence attributed (directly observed via the
// API). The pass is METADATA ONLY: ARM resource ids and subscription ids,
// lower-cased for stable convergence. It NEVER reads a resource's properties,
// tags, an RBAC role assignment document, or any secret.
//
// Activity Log feed (signal "azure_activity"). A Bearer-authorized Azure Monitor
// management-events read per subscription, over the lookback window, emits one
// edge per COMPLETED operation: identity⊳azure.api (the operationName, e.g.
// "Microsoft.Compute/virtualMachines/write"), with the caller as the origin and
// the resource provider as the tool. Mode comes verbatim from the operation's
// RBAC action: write/delete → write, read → read, the ambiguous "action" suffix
// (and anything else) → unknown, never guessed. Only Succeeded events are emitted:
// a Started event is the duplicate half of a Started/Succeeded pair, and a Failed
// operation changed nothing (a blocked attempt is not an observed access) — the
// honest cost is that a long-running operation not yet Succeeded inside the window
// is not emitted.
//
// Identity convergence. The caller is resolved to the Entra object id (the
// objectidentifier claim) when present, else the application id, else the caller
// string — the same object-id ref the entra-agent roster keys on, so the
// governance roster fuses them by external_id. The connector never
// resolves an identity to an agent — that bridge is module VI's job.
//
// Boundary vs the service-level Azure observers. azure-blob-audit and
// azurekeyvault own the per-resource DATA plane — per-blob/secret edges from one
// resource's diagnostic logs. This connector stays on the tenant MANAGEMENT plane
// under the disjoint "azure.api" resource namespace, so the two never
// double-ingest the same fact.
//
// Least privilege. The connector needs only read-only management access: the
// Reader role at the tenant root (or per subscription) covers Resource Graph,
// subscription listing and the Activity Log; the OAuth scope is the single
// management.azure.com/.default (which grants exactly the SP's assigned roles).
// Authentication is the OAuth2 client-credentials flow (tenant + client id +
// client secret) or a pre-issued ARM token (managed identity / ADC). With a
// MISSING or PARTIAL credential the connector is offline: Open succeeds and
// Gather emits nothing. It issues no write/create/delete call and touches no
// secret, key or payload.
package azureactivity
