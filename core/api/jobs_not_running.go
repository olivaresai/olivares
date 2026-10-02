// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

// JobNotRunning is a background job this node does not run, and why. server-info
// lists them (Options.JobsNotRunning) so the console says so where an
// administrator expects the job to work.
type JobNotRunning struct {
	// Job is one of the Job* names.
	Job string `json:"job"`
	// Reason is one of the JobReason* codes.
	Reason string `json:"reason"`
}

// Background jobs exposed by server-info. Coverage jobs remain unavailable
// when the store cannot enumerate every tenant; edition jobs report their gate.
const (
	JobRetention                = "retention"
	JobLegalHoldArchive         = "legal_hold_archive"
	JobAuditCheckpoints         = "audit_checkpoints"
	JobAuditArchive             = "audit_archive"
	JobDirectorySynchronization = "directory_synchronization"
)

// JobReasonNoTenantInventory: PostgreSQL with neither the closed tenant inventory
// (olivares db init installs it) nor the BYPASSRLS administration role
// (--admin-dsn). The application role reads one tenant at a time, so a job that
// must cover every tenant cannot see them all and does not claim a pass.
const JobReasonNoTenantInventory = "no_tenant_inventory"

// Optional directory work stays paused while its entitlement or live source is
// unavailable. Historical evidence remains readable.
const (
	JobReasonAddonRequiresLicense = "addon_requires_license"
	JobReasonDirectoryUnavailable = "directory_unavailable"
)
