// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ReadAuthorityVersion is a bounded, opaque fingerprint of the stored inputs to
// this source's existing resolver. It grants nothing. Consumers must evaluate
// the native/source authorizers and compare this fingerprint before releasing a
// buffered snapshot. No row, credential locator or host path crosses the port.
func (r *Resolver) ReadAuthorityVersion(ctx context.Context, tenant model.TenantID, principal auth.Principal, sourceType, sourceRef string) (string, error) {
	data := r.m.moduleData()
	if data == nil || !validSourceTypes[sourceType] || sourceRef == "" {
		return "", errNotReady
	}
	var version string
	err := data.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		version, err = SourceAuthorityVersion(ctx, sc, principal, sourceType, sourceRef)
		return err
	})
	return version, err
}

// SourceAuthorityVersion observes the resolver inputs through the caller's
// tenant-pinned transaction. It is a version comparison, never a permission.
// Mutate callers can use it before writes without nesting a store View.
func SourceAuthorityVersion(ctx context.Context, sc store.Scope, principal auth.Principal, sourceType, sourceRef string) (string, error) {
	var version struct {
		Authorization store.AuthorizationFactRef
		Directory     model.DirectoryEpoch
		Bindings      []model.Record
		Assignments   []model.Record
		Workspace     string
		// Omitted when empty, so a tenant with no parent workspace keeps its digest.
		WorkspaceAbove []string `json:",omitempty"`
		Groups         []string
		Agent          string
	}
	err := func() error {
		epochs, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return store.ErrAuthorizationEpochUnavailable
		}
		directory, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return store.ErrDirectoryUnavailable
		}
		var err error
		version.Authorization, err = epochs.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		version.Directory, err = directory.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		version.Bindings, err = boundedAuthorityRows(ctx, sc, bindingKind, eq(colSourceType, sourceType), eq(colSourceRef, sourceRef))
		if err != nil {
			return err
		}
		version.Assignments, err = boundedAuthorityRows(ctx, sc, assignmentKind, eq(colAssignConnector, sourceRef))
		if err != nil {
			return err
		}
		actor, err := resolveActorScope(ctx, sc, actorRef{kind: actorAgent, ref: principal.AgentIdentity})
		if err != nil {
			return err
		}
		version.Workspace, version.WorkspaceAbove, version.Groups, version.Agent = actor.workspaceSlug, actor.workspaceAbove, actor.groups, actor.agentExternalID
		sort.Strings(version.Groups)
		return nil
	}()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(version)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func boundedAuthorityRows(ctx context.Context, sc store.Scope, kind model.Kind, filters ...model.Filter) ([]model.Record, error) {
	repo, err := sc.Ext(kind)
	if err != nil {
		return nil, err
	}
	q := model.Query{Filters: filters, Limit: 128}
	var rows []model.Record
	for {
		pageRows, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		rows = append(rows, pageRows...)
		if len(rows) > 1024 {
			return nil, store.ErrConflict
		}
		if !page.HasMore {
			break
		}
		if page.Cursor == "" || page.Cursor == q.Cursor {
			return nil, store.ErrConflict
		}
		q.Cursor = page.Cursor
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].String(model.ColID) < rows[j].String(model.ColID) })
	return rows, nil
}
