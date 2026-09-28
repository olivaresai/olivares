// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package model

// CredentialBinding is one durable binding of a server-selected subject, such
// as a workflow run, to the exact credential revision it continues under. The
// row lives in the system auth partition; TargetTenantID is the business tenant
// of the subject. Only core/auth writes it, and only core/auth interprets it:
// the row names a credential revision, and every use re-derives the credential's
// current authority from the credential's own rows.
//
// A binding is superseded, never edited in place: a successor row is written
// for the same subject at a newer SubjectGeneration and the predecessor records
// it in SupersededBy.
type CredentialBinding struct {
	BaseFields
	// TargetTenantID is the business tenant of the subject.
	TargetTenantID TenantID
	// SubjectKind is the closed subject kind, such as "orchestration.workflow_run".
	SubjectKind string
	// SubjectRef is the subject's id, such as the workflow run id.
	SubjectRef ID
	// SubjectGeneration is the subject owner's state generation this row was
	// written for. The first binding has generation 0; a successor carries the
	// generation its owner reserved for the succession, and a subject has at
	// most one row per generation.
	SubjectGeneration int64
	// SubjectUserID is the account every binding of the subject must belong to.
	// It is copied from the first binding and only ever refuses a successor.
	SubjectUserID ID
	// CredentialKind is "user" (a session) or "token" (an API token).
	CredentialKind string
	// CredentialID is the session or API-token row id.
	CredentialID ID
	// CredentialVersion is the exact row version of that credential.
	CredentialVersion int64
	// CeilingKind, CeilingRole, CeilingWorkspaceID and CeilingAgent are the
	// subject's authority ceiling: the credential kind, tenant role, workspace
	// confinement and agent identity of the credential its first binding
	// pinned. Every successor copies them unchanged, and a successor's
	// credential may not exceed them.
	CeilingKind        string
	CeilingRole        string
	CeilingWorkspaceID ID
	CeilingAgent       string
	// SupersededBy is the successor binding, empty while the row is current.
	SupersededBy ID
	// SupersededAt is when a successor superseded this row.
	SupersededAt *Timestamp
	// AuthorizedByActor is the audit actor of the administrator who authorized
	// this row as a successor. It is empty for a first binding.
	AuthorizedByActor string
	// Seal is SHA-256 over the immutable fields under a domain string. It
	// detects an inconsistent row; it is not what makes a binding unforgeable.
	Seal []byte
}
