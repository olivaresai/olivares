// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

const (
	snapshotMaxFile    int64 = 1 << 20
	snapshotMaxTotal   int64 = 16 << 20
	snapshotMaxEntries       = 1024
	snapshotMaxDepth         = 16
)

type snapshotFile struct {
	path string
	mode fs.FileMode
	data []byte
}

type snapshotTree struct {
	files []snapshotFile
	stamp string
}

// ReadWorkspaceSnapshot reads one safe relative directory in an active registered
// folder. It releases no file until repeated filesystem reads, DLP and stored
// registration/source-authority checks agree. The callback receives only buffered
// regular-file bytes, directory-relative names and modes 0644/0755, never a root or
// live filesystem handle. Consumers must stage callbacks and publish only after a
// successful return. Any callback failure aborts the operation.
func (m *Module) ReadWorkspaceSnapshot(ctx context.Context, tenant model.TenantID, principal auth.Principal, ref, dir string, receive func(string, fs.FileMode, []byte) error) (int64, error) {
	if ctx == nil || receive == nil || !safeSnapshotPath(dir) {
		return 0, badRequest("snapshot requires a safe relative directory and a receiver")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if m.Data == nil || m.WorkspaceSnapshotAuthority == nil || tenant.IsZero() || tenant == model.SystemTenantID {
		return 0, &runErr{http.StatusServiceUnavailable, "workspace snapshot authority is unavailable"}
	}
	rec, err := m.loadWorkspaceRec(ctx, tenant, ref)
	if err != nil {
		return 0, err
	}
	if rec.String(colWsState) != wsActive || rec.Int(model.ColVersion) < 1 {
		return 0, conflictErr("workspace registration is not active")
	}
	if !safeSnapshotRoot(rec.String(colWsRootPath)) || m.protectedSnapshotDirectory(filepath.Join(rec.String(colWsRootPath), filepath.FromSlash(dir))) {
		return 0, forbiddenErr("workspace snapshot cannot read a protected source tree")
	}
	id := model.ID(rec.String(model.ColID))
	authority, err := m.WorkspaceSnapshotAuthority(ctx, tenant, principal, ref, id)
	if ctx.Err() != nil {
		return 0, ctx.Err()
	}
	if err != nil || authority == "" {
		return 0, forbiddenErr("workspace snapshot read is not authorized")
	}
	ws, err := m.resolveWorkspace(ctx, tenant, ref)
	if err != nil {
		return 0, err
	}
	// The ordinary jail resolves links for existing file APIs. This narrower port
	// requires the stored canonical path still to be canonical, without a link.
	if ws.rootReal != rec.String(colWsRootPath) {
		return 0, forbiddenErr("workspace snapshot cannot read a linked or protected source tree")
	}
	if _, err := ws.jail(dir, true); err != nil {
		return 0, mapFSErr(err)
	}
	files, err := readWorkspaceSnapshot(ctx, ws, dir)
	if err != nil {
		return 0, err
	}
	var sensitivity []SensitivityHit
	for _, file := range files.files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		hits, deny := m.classifyContent(ctx, ws.dlpMode, file.data)
		if deny {
			m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "read-denied", workspaceID: id, workspaceRef: ref, path: dir, actor: principal.Actor(), actorKind: principal.ActorKind(), classes: dlpClasses(hits)})
			return 0, forbiddenErr("workspace snapshot denied by DLP policy")
		}
		sensitivity = append(sensitivity, hits...)
	}
	verify := func() error {
		if m.protectedSnapshotDirectory(filepath.Join(rec.String(colWsRootPath), filepath.FromSlash(dir))) {
			return forbiddenErr("workspace snapshot cannot read a protected source tree")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		currentAuthority, err := m.WorkspaceSnapshotAuthority(ctx, tenant, principal, ref, id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || currentAuthority == "" || currentAuthority != authority {
			return conflictErr("workspace source authority changed during the snapshot")
		}
		current, err := m.loadWorkspaceRec(ctx, tenant, ref)
		if err != nil || !sameSnapshotRegistration(rec, current) {
			return conflictErr("workspace registration changed during the snapshot")
		}
		// Read after the authority seam: an authorizer or classifier that changes
		// the source cannot pass verification with its earlier filesystem state.
		verified, err := readWorkspaceSnapshot(ctx, ws, dir)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || !equalSnapshotFiles(files, verified) {
			return conflictErr("workspace snapshot source changed during the read")
		}
		current, err = m.loadWorkspaceRec(ctx, tenant, ref)
		if err != nil || !sameSnapshotRegistration(rec, current) {
			return conflictErr("workspace registration changed during the snapshot")
		}
		currentAuthority, err = m.WorkspaceSnapshotAuthority(ctx, tenant, principal, ref, id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || currentAuthority == "" || currentAuthority != authority {
			return conflictErr("workspace source authority changed during the snapshot")
		}
		return ctx.Err()
	}
	if err := verify(); err != nil {
		return 0, err
	}
	for _, file := range files.files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := receive(file.path, file.mode, bytes.Clone(file.data)); err != nil {
			return 0, err
		}
	}
	m.auditWorkspaceRead(ctx, tenant, wsMutationInput{op: "snapshot", workspaceID: id, workspaceRef: ref, path: dir, actor: principal.Actor(), actorKind: principal.ActorKind(), classes: dlpClasses(sensitivity)})
	// A consumer stages data until this return. Its callback cannot mutate the
	// comparison buffer or make a changed source look like a successful import.
	if err := verify(); err != nil {
		return 0, err
	}
	return rec.Int(model.ColVersion), nil
}

func sameSnapshotRegistration(a, b model.Record) bool {
	x, err := json.Marshal(a)
	if err != nil {
		return false
	}
	y, err := json.Marshal(b)
	return err == nil && bytes.Equal(x, y)
}

func equalSnapshotFiles(a, b snapshotTree) bool {
	if a.stamp != b.stamp || len(a.files) != len(b.files) {
		return false
	}
	for i := range a.files {
		if a.files[i].path != b.files[i].path || a.files[i].mode != b.files[i].mode || !bytes.Equal(a.files[i].data, b.files[i].data) {
			return false
		}
	}
	return true
}

// Account roots are configured paths, not reserved directory names. Protect
// these trees and the node's engine storage even when registered as a folder.
func (m *Module) protectedSnapshotDirectory(selected string) bool {
	roots := []string{m.accountsRoot, m.profileHomesRoot, m.toolLoginsRoot}
	if m.rt != nil {
		roots = append(roots, m.rt.confineProtect...)
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		canonical := root
		var suffix []string
		for {
			resolved, err := filepath.EvalSymlinks(canonical)
			if err == nil {
				for i := len(suffix) - 1; i >= 0; i-- {
					resolved = filepath.Join(resolved, suffix[i])
				}
				canonical = resolved
				break
			}
			if !errors.Is(err, os.ErrNotExist) || filepath.Dir(canonical) == canonical {
				return true // an unobservable protected root cannot authorize reads
			}
			suffix = append(suffix, filepath.Base(canonical))
			canonical = filepath.Dir(canonical)
		}
		if within(root, selected) || within(selected, root) || within(canonical, selected) || within(selected, canonical) {
			return true
		}
	}
	return false
}

func safeSnapshotRoot(root string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return false
	}
	relative := strings.TrimPrefix(filepath.ToSlash(root), "/")
	// System configuration and kernel/device trees cannot be skill sources even
	// when a privileged operator has registered one as a general workspace.
	switch strings.Split(relative, "/")[0] {
	case "etc", "proc", "sys", "dev":
		return false
	}
	return safeSnapshotPath(relative)
}

func safeSnapshotPath(p string) bool {
	if p == "" || p == "." || !utf8.ValidString(p) || len(p) > 1024 || strings.ContainsAny(p, "\\:\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p || len(strings.Split(p, "/")) > snapshotMaxDepth {
		return false
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		lower := strings.ToLower(part)
		switch lower {
		case ".git", ".ssh", ".aws", ".azure", ".kube", ".gnupg", ".codex", ".claude", ".grok", ".t3", ".config", ".secrets", ".olivares-secrets", ".olivares", "config", "configs", "account", "accounts", "auth", "credentials", "secrets", "auth.json", "credentials.json", "config.json", "token.json", "tokens.json", "id_rsa", "id_ed25519":
			return false
		}
		if lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.HasSuffix(lower, ".key") || strings.HasPrefix(lower, ".credentials") {
			return false
		}
	}
	return true
}
