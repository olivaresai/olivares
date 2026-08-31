// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// workspace_ledger.go anchors governed file MUTATIONS in the tamper-evident core
// audit chain (pattern). Each mutation is sealed by a PayloadHash that
// commits to the OPERATION and the CONTENT HASH — never the bytes (minimal-data,
// docs/SECURITY-HARDENING.md). The seal happens BEFORE the filesystem op (deny-closed: if the
// evidence cannot be appended, the mutation does not run — the engine's
// recording-gate philosophy). Reads are audited best-effort AFTER the RBAC check
// (a privileged read, like the observe stream-open audit), carrying the DLP classes
// (never content) in Meta.

// wsMutationInput is one governed file mutation to seal.
type wsMutationInput struct {
	workspaceID  model.ID
	workspaceRef string
	op           string // write|mkdir|move|delete|register|deregister
	path         string // primary relative path
	path2        string // secondary (move destination); "" otherwise
	contentHash  string // hex sha-256 of written content; "" for non-write ops
	actor        string
	actorKind    string
	classes      []string // DLP classes (non-sensitive); "" for non-read
}

// sealWorkspaceMutation appends a FILE mutation to the global audit ledger in its own
// transaction. It returns an error if the seal fails so the caller aborts the FS op
// (no unaudited mutation). It is used for write/mkdir/move/delete, whose filesystem
// effect is NOT in a store transaction, so the seal precedes the FS op (deny-closed).
func (m *Module) sealWorkspaceMutation(ctx context.Context, tenant model.TenantID, in wsMutationInput) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		return appendWorkspaceAudit(ctx, sc, in)
	})
}

// appendWorkspaceAudit seals one workspace mutation in the caller's transaction (used
// by register/deregister so the audit is ATOMIC with the row insert/delete — the
// strongest deny-closed posture, no orphan row without evidence).
func appendWorkspaceAudit(ctx context.Context, sc store.Scope, in wsMutationInput) error {
	hash := workspacePayloadHash(in)
	meta := map[string]any{"workspace_ref": in.workspaceRef, "op": in.op, "path": in.path}
	if in.path2 != "" {
		meta["to"] = in.path2
	}
	if in.contentHash != "" {
		meta["sha256"] = in.contentHash
	}
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:       orSystem(in.actor),
		ActorKind:   orSystemKind(in.actorKind),
		Action:      "sessions.workspace." + in.op,
		TargetKind:  workspaceKind,
		TargetID:    in.workspaceID,
		PayloadHash: hash[:],
		Meta:        meta,
	})
	return err
}

// auditWorkspaceRead records a governed file read best-effort (RBAC already gated it;
// a failed audit logs but never denies — mirroring the observe stream-open audit). It
// carries the DLP classes detected, never the content.
func (m *Module) auditWorkspaceRead(ctx context.Context, tenant model.TenantID, in wsMutationInput) {
	meta := map[string]any{"workspace_ref": in.workspaceRef, "op": in.op, "path": in.path}
	if len(in.classes) > 0 {
		meta["sensitivity"] = in.classes
	}
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, e := sc.Audit().Append(ctx, model.AuditDraft{
			Actor:      orSystem(in.actor),
			ActorKind:  orSystemKind(in.actorKind),
			Action:     "sessions.workspace." + in.op,
			TargetKind: workspaceKind,
			TargetID:   in.workspaceID,
			Meta:       meta,
		})
		return e
	})
	if err != nil {
		m.debugf("sessions: workspace-read audit failed", "err", redactErr(err))
	}
}

// workspacePayloadHash is the SHA-256 of the canonical, non-sensitive mutation: the
// op, the workspace ref, the path(s), and the CONTENT HASH (never the bytes).
func workspacePayloadHash(in wsMutationInput) [32]byte {
	h := sha256.New()
	for _, part := range []string{in.op, in.workspaceRef, in.path, in.path2, in.contentHash} {
		_, _ = h.Write([]byte(strconv.Itoa(len(part))))
		_, _ = h.Write([]byte{':'})
		_, _ = h.Write([]byte(part))
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// sha256Hex returns the hex SHA-256 of content (the ledger anchor for a write).
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
