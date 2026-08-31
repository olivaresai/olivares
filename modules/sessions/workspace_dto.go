// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "github.com/olivaresai/olivares/core/model"

// workspaceDTO is the API shape of one registered workspace. It carries no file
// content and no secret — only the operator-chosen configuration and references.
type workspaceDTO struct {
	WorkspaceRef    string   `json:"workspace_ref"`
	Name            string   `json:"name,omitempty"`
	RootPath        string   `json:"root_path"`
	MountMode       string   `json:"mount_mode"`
	ContainerTarget string   `json:"container_target,omitempty"`
	AllowSubpaths   []string `json:"allow_subpaths,omitempty"`
	MaxReadBytes    int64    `json:"max_read_bytes"`
	DLPMode         string   `json:"dlp_mode"`
	State           string   `json:"state"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
}

func toWorkspaceDTO(rec model.Record) workspaceDTO {
	return workspaceDTO{
		WorkspaceRef:    rec.String(colWsRef),
		Name:            rec.String(colWsName),
		RootPath:        rec.String(colWsRootPath),
		MountMode:       rec.String(colWsMountMode),
		ContainerTarget: rec.String(colWsContainerTgt),
		AllowSubpaths:   decodeSubpaths(rec),
		MaxReadBytes:    workspaceMaxRead(rec),
		DLPMode:         rec.String(colWsDLPMode),
		State:           rec.String(colWsState),
		CreatedAt:       rec.String(model.ColCreatedAt),
		UpdatedAt:       rec.String(model.ColUpdatedAt),
	}
}

// workspaceMaxRead returns the configured per-read cap, or the default when NULL/0.
func workspaceMaxRead(rec model.Record) int64 {
	if rec.IsNull(colWsMaxReadBytes) {
		return defaultMaxReadBytes
	}
	if v := rec.Int(colWsMaxReadBytes); v > 0 {
		return v
	}
	return defaultMaxReadBytes
}

// createWorkspaceRequest is the POST /workspaces body.
type createWorkspaceRequest struct {
	Name            string   `json:"name"`
	RootPath        string   `json:"root_path"`
	MountMode       string   `json:"mount_mode"`
	ContainerTarget string   `json:"container_target"`
	AllowSubpaths   []string `json:"allow_subpaths"`
	MaxReadBytes    int64    `json:"max_read_bytes"`
	DLPMode         string   `json:"dlp_mode"`
}

// moveRequest is the POST /workspaces/{ref}/files/move body.
type moveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// fileListResponse is the paginated directory listing.
type fileListResponse struct {
	Path    string      `json:"path"`
	Entries []fileEntry `json:"entries"`
	Cursor  string      `json:"cursor,omitempty"`
	HasMore bool        `json:"has_more"`
}

// fileReadResponse is the governed read of one file. Content is UTF-8 text when the
// file is text; binary content is base64. sensitivity carries the DLP labels (never
// the matched values). Truncated marks a read clipped at the size cap.
type fileReadResponse struct {
	Path        string           `json:"path"`
	Size        int64            `json:"size"`
	Encoding    string           `json:"encoding"` // utf-8 | base64
	Content     string           `json:"content"`
	Truncated   bool             `json:"truncated,omitempty"`
	Sensitivity []SensitivityHit `json:"sensitivity,omitempty"`
	SHA256      string           `json:"sha256,omitempty"`
}

// writeResponse confirms a write (the content is anchored by hash, never echoed).
type writeResponse struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Created bool   `json:"created"`
}
