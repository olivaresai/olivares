// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A share link is a revocable, expiring grant to read one file or one folder of a
// registered workspace without an account. Business writes and serves the rows; the
// table is declared here, in every edition, because the open and the commercial
// builds must register the same schema (the in-place edition upgrade keeps one data
// directory), exactly as the tool-pin and circuit-breaker tables are.
const shareKind model.Kind = "sessions.workspace_share"

// shareTable is the share row's physical table.
const shareTable = "sessions_workspace_share"

// sessions.workspace_share columns. NO column holds file content or a recipient's
// secret in clear: the token's secret and each guest code are stored as SHA-256, the
// optional password as the engine's password hash. The row's own id is the link's
// public reference.
const (
	colShWorkspaceRef = "workspace_ref" // the registered folder the link reads
	colShPath         = "path"          // slash path below that folder; empty is the folder itself
	colShTarget       = "target"        // file | dir, fixed when the link is made
	colShSecret       = "secret_sha256" // hex SHA-256 of the token's secret half
	colShPassword     = "password_hash" // optional; the engine's password hash
	colShGuests       = "guests"        // optional JSON list of named guests, each with its own code
	colShExpiresAt    = "expires_at"
	colShMaxDownloads = "max_downloads" // optional cap on served files
	colShDownloads    = "downloads"
	colShFailed       = "failed_attempts" // wrong passwords or codes since the last success or lock
	colShLockedUntil  = "locked_until"
	colShState        = "state" // active | revoked
	colShRevokedAt    = "revoked_at"
	colShRevokedBy    = "revoked_by"
	colShCreatedBy    = "created_by"
)

// shareGuest is one named guest of a link: a label the sharer chose (an address or a
// name, never an account) and the SHA-256 of the code only that person was given.
type shareGuest struct {
	Name       string `json:"name"`
	CodeSHA256 string `json:"code_sha256"`
	UsedAt     string `json:"used_at,omitempty"` // when the one-time code was exchanged; empty until then
}

// Share link targets, fixed when the link is made.
const (
	shareTargetFile = "file"
	shareTargetDir  = "dir"
)

// Share link states. An expired link keeps state active: its end is expires_at.
const (
	shareActive  = "active"
	shareRevoked = "revoked"
)

// registerWorkspaceShareSchema declares the share link entity.
func (m *Module) registerWorkspaceShareSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  shareKind,
		Table: shareTable,
		Fields: []model.FieldSpec{
			{Name: colShWorkspaceRef, Kind: model.KindText, Indexed: true, Principal: model.None("an opaque workspace registry id, the workspace_ref of the folder the link reads (workspace_schema.go:25-27); never an account")},
			{Name: colShPath, Kind: model.KindText, Principal: model.None("a slash path below the shared folder, resolved by the folder jail (workspace_fs.go:41-60); never an account")},
			{Name: colShTarget, Kind: model.KindText, Principal: model.None("file or dir, a closed set fixed at creation (workspace_share_schema.go:34)")},
			{Name: colShSecret, Kind: model.KindText, Principal: model.None("the hex SHA-256 of a random secret, compared in constant time; never an account (workspace_share_schema.go:32-35)")},
			{Name: colShPassword, Kind: model.KindText, Nullable: true, Principal: model.None("an argon2id password hash of a secret the sharer chose, never an account (workspace_share_schema.go:32-35)")},
			{Name: colShGuests, Kind: model.KindJSON, Nullable: true, Principal: model.Nested([]shareGuest{}, model.ClassEvidence,
				model.Leaf("[].name", model.None("a label the sharer typed for the person who receives the code; it names a guest in the audit and is never resolved to an account (workspace_share_schema.go:46-49)")),
				model.Leaf("[].code_sha256", model.None("the hex SHA-256 of that guest's random code; never an account (workspace_share_schema.go:46-49)")),
				model.Leaf("[].used_at", model.None("the time that guest's one-time code was exchanged; never an account (workspace_share_schema.go:46-49)")))},
			{Name: colShExpiresAt, Kind: model.KindTimestamp, Indexed: true},
			{Name: colShMaxDownloads, Kind: model.KindInt, Nullable: true},
			{Name: colShDownloads, Kind: model.KindInt},
			{Name: colShFailed, Kind: model.KindInt},
			{Name: colShLockedUntil, Kind: model.KindTimestamp, Nullable: true},
			{Name: colShState, Kind: model.KindText, Indexed: true, Principal: model.None("active or revoked, a closed set (workspace_share_schema.go:52-55)")},
			{Name: colShRevokedAt, Kind: model.KindTimestamp, Nullable: true},
			{Name: colShRevokedBy, Kind: model.KindText, Nullable: true, Principal: pdeclActorRef},
			{Name: colShCreatedBy, Kind: model.KindText, Indexed: true, Principal: pdeclActorRef},
		},
	})
}
