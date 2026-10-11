// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The WORKSPACE entity adds to module II (FASE V). A sessions.workspace is a
// HOST FILESYSTEM ROOT (the working directory) bound to one or more operated
// sessions and mounted into their containers — DISTINCT from the FASE X authz
// `workspace_id` scoping dimension (core), which is not a path. It lives in
// the SAME "sessions" namespace as the run lifecycle that consumes it: a
// sessions.run.workspace_ref points to a sessions.workspace.workspace_ref (the
// binding; 1 workspace ↔ N sessions, no separate binding table — minimal data).
const workspaceKind model.Kind = "sessions.workspace"

// Physical table (namespace_snake, ≤40 chars).
const workspaceTable = "sessions_workspace"

// sessions.workspace columns. NO column holds file content, secrets, or bytes
// (minimal-data, docs/SECURITY-HARDENING.md): root_path is the operator-chosen non-secret host path;
// file content never lands here (reads stream hot, writes are anchored by hash).
const (
	colWsRef             = "workspace_ref"
	colWsName            = "name"
	colWsRootPath        = "root_path"
	colWsMountMode       = "mount_mode"        // rw | ro
	colWsContainerTgt    = "container_target"  // e.g. /workspace
	colWsAllowSubpaths   = "allow_subpaths"    // JSON array of relative subpaths (empty = whole root)
	colWsReadOnlyFolders = "read_only_folders" // JSON array of additional host folders (empty = none)
	colWsMaxReadBytes    = "max_read_bytes"
	colWsDLPMode         = "dlp_mode" // label | deny | off
	colWsState           = "state"    // active | disabled
)

// Mount modes (how the workspace root is bound / what the file API permits).
const (
	mountRW = "rw"
	mountRO = "ro"
)

// DLP read postures. Folder registration defaults to off (Root, 2026-10-01).
const (
	dlpLabel = "label" // classify + label + audit, always return
	dlpDeny  = "deny"  // deny-closed: a classified-sensitive read fails without a grant
	dlpOff   = "off"   // no classification (default for an unlabeled folder)
)

// Workspace lifecycle states.
const (
	wsActive   = "active"
	wsDisabled = "disabled"
)

// defaultMaxReadBytes bounds a single governed file read (5 MiB) so a pathological
// file cannot exhaust memory; a workspace may lower it via max_read_bytes.
const defaultMaxReadBytes int64 = 5 << 20

// defaultContainerTarget is where a workspace mounts inside a container when the
// operator does not pick a path.
const defaultContainerTarget = "/workspace"

// registerWorkspaceSchema declares the workspace registry entity. The engine creates
// the table, injects the base columns (id/tenant_id/created_at/updated_at/version)
// and attaches the tenant guards. Called from RegisterSchema (schema.go).
func (m *Module) registerWorkspaceSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  workspaceKind,
		Table: workspaceTable,
		Fields: []model.FieldSpec{
			{Name: colWsRef, Kind: model.KindText, Principal: model.None("an opaque workspace registry id (a UUID) minted by this module: workspace.go:82, workspace_dto.go:27")},
			{Name: colWsName, Kind: model.KindText, Nullable: true, Principal: model.None("an operator-chosen workspace label, shown only: workspace_dto.go:28")},
			{Name: colWsRootPath, Kind: model.KindText, Principal: model.None("the canonical host root path of the workspace: workspace.go:73, workspace_dto.go:29")},
			{Name: colWsMountMode, Kind: model.KindText, Principal: model.None("a mount mode, a closed set: workspace.go:545, workspace_schema.go:41-42")},
			{Name: colWsContainerTgt, Kind: model.KindText, Nullable: true, Principal: model.None("an absolute in-container mount path: workspace.go:554-558, workspace_dto.go:31")},
			{Name: colWsAllowSubpaths, Kind: model.KindJSON, Nullable: true, Principal: pdeclWorkspaceSubpaths},
			{Name: colWsMaxReadBytes, Kind: model.KindInt, Nullable: true},
			{Name: colWsDLPMode, Kind: model.KindText, Principal: model.None("a DLP read posture, a closed set: workspace.go:551, workspace_schema.go:47-49")},
			{Name: colWsState, Kind: model.KindText, Indexed: true, Principal: model.None("a workspace state, a closed set: workspace_schema.go:54-55, workspace.go:199")},
			{Name: colWsReadOnlyFolders, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]string{}, model.ClassEvidence,
				model.Leaf("[]", model.None("an administrator-selected canonical host folder: workspace_read_only_folders.go:24-43")))},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_workspace_ref_uniq",
			Columns: []string{model.ColTenantID, colWsRef},
			Unique:  true,
		}},
	})
}

// Principal declarations of the host-workspace registry. A workspace row names
// a directory, never a principal.
var (
	// pdeclWorkspaceSubpaths is the allowlist the writer marshals as a JSON
	// array of strings (workspace.go:660-675) and the reader decodes back
	// (workspace.go:678-688).
	pdeclWorkspaceSubpaths = model.Nested([]string{}, model.ClassEvidence,
		model.Leaf("[]", model.None("a relative subpath of the workspace root: workspace.go:660-675, workspace.go:678-688")),
	)
)
