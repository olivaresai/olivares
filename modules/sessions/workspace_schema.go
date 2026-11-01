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
	colWsSetupCommand    = "setup_command"
	colWsSetupHash       = "setup_approved_sha256"
	colWsSetupActor      = "setup_approved_by"
	colWsSetupAt         = "setup_approved_at"
	colWsRef             = "workspace_ref"
	colWsName            = "name"
	colWsRootPath        = "root_path"
	colWsMountMode       = "mount_mode"        // rw | ro | write_no_delete
	colWsContainerTgt    = "container_target"  // e.g. /workspace
	colWsAllowSubpaths   = "allow_subpaths"    // JSON array of relative subpaths (empty = whole root)
	colWsReadOnlyFolders = "read_only_folders" // JSON array of additional host folders (empty = none)
	colWsMaxReadBytes    = "max_read_bytes"
	colWsBackup          = "backup"
	colWsDataClass       = "data_class"
	colWsDLPMode         = "dlp_mode" // label | deny | off
	colWsState           = "state"    // active | disabled
	// colWsDefaultPermission is the permission name (permission_modes.go) a session
	// started in this folder is offered by default; empty means the folder has none.
	colWsDefaultPermission = "default_permission"
)

// Mount modes (how the workspace root is bound / what the file API permits).
// write_no_delete is read-write without the right to remove a name: a native session
// may create and change files in the folder and never unlink, rmdir, rename or move
// one out (the operating system enforces it, so a host that cannot confine refuses
// the launch).
const (
	mountRW       = "rw"
	mountRO       = "ro"
	mountNoDelete = "write_no_delete"
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
			{Name: colWsSetupCommand, Kind: model.KindText, Nullable: true, Principal: model.None("operator-written setup command text, never interpreted as an identity: workspace_dto.go:39-40")},
			{Name: colWsSetupHash, Kind: model.KindText, Nullable: true, Principal: model.None("SHA-256 of the approved setup command text: workspace_setup.go:32-36")},
			{Name: colWsSetupActor, Kind: model.KindText, Nullable: true, Principal: pdeclActorRef},
			{Name: colWsSetupAt, Kind: model.KindText, Nullable: true, Principal: model.None("time the operator approved the setup command: workspace_setup.go:32-36")},
			{Name: colWsRef, Kind: model.KindText, Principal: model.None("an opaque workspace registry id (a UUID) minted by this module: workspace.go:82, workspace_dto.go:27")},
			{Name: colWsName, Kind: model.KindText, Nullable: true, Principal: model.None("an operator-chosen workspace label, shown only: workspace_dto.go:28")},
			{Name: colWsRootPath, Kind: model.KindText, Principal: model.None("the canonical host root path of the workspace: workspace.go:73, workspace_dto.go:29")},
			{Name: colWsMountMode, Kind: model.KindText, Principal: model.None("a mount mode, a closed set: workspace.go:545, workspace_schema.go:41-42")},
			{Name: colWsContainerTgt, Kind: model.KindText, Nullable: true, Principal: model.None("an absolute in-container mount path: workspace.go:554-558, workspace_dto.go:31")},
			{Name: colWsAllowSubpaths, Kind: model.KindJSON, Nullable: true, Principal: pdeclWorkspaceSubpaths},
			{Name: colWsMaxReadBytes, Kind: model.KindInt, Nullable: true},
			{Name: colWsBackup, Kind: model.KindBool, Nullable: true},
			{Name: colWsDataClass, Kind: model.KindText, Nullable: true, Principal: model.None("operator-selected folder classification metadata, a closed enum: data_class.go:9-16")},
			{Name: colWsDLPMode, Kind: model.KindText, Principal: model.None("a DLP read posture, a closed set: workspace.go:551, workspace_schema.go:47-49")},
			{Name: colWsState, Kind: model.KindText, Indexed: true, Principal: model.None("a workspace state, a closed set: workspace_schema.go:54-55, workspace.go:199")},
			{Name: colWsReadOnlyFolders, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]string{}, model.ClassEvidence,
				model.Leaf("[]", model.None("an administrator-selected canonical host folder: workspace_read_only_folders.go:24-43")))},
			{Name: colWsDefaultPermission, Kind: model.KindText, Nullable: true, Principal: model.None("a permission name, a closed set: permission_modes.go:84-93, workspace_dto.go:55")},
			{Name: colWsProjectConfigHash, Kind: model.KindText, Nullable: true, Principal: model.None("SHA-256 of the approved project agent configuration: workspace_project_config.go:482-487")},
			{Name: colWsProjectConfigActor, Kind: model.KindText, Nullable: true, Principal: pdeclActorRef},
			{Name: colWsProjectConfigAt, Kind: model.KindText, Nullable: true, Principal: model.None("time the operator approved the project agent configuration: workspace_project_config.go:482-487")},
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
