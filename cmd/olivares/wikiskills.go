// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// wikiCatalogData retains the catalog's own mutation authority around the common
// publisher. Folder initialization does not borrow workspace admin as catalog write.
type wikiCatalogData struct {
	api.ScopedData
	request   auth.Request
	authority auth.RouteMutationAuthorization
}

func (d wikiCatalogData) Mutate(ctx context.Context, fn func(store.Scope) error) error {
	return d.ScopedData.Mutate(ctx, func(sc store.Scope) error {
		bundle, err := d.authority.AuthorityFor(time.Now(), d.request)
		if err != nil {
			return err
		}
		locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
		if !ok {
			return store.ErrStoreUnavailable
		}
		if err := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
			return err
		}
		if _, err := d.authority.AuthorityFor(time.Now(), d.request); err != nil {
			return err
		}
		return fn(sc)
	})
}

func (b *bootState) ensureWikiSkills(ctx context.Context, mc api.ModuleContext, target skills.Target) (assignment skills.Assignment, operationErr error) {
	ctx, bounded := context.WithTimeout(ctx, 30*time.Second)
	defer bounded()
	resource := mc.Resource
	var err error
	ctx, mc, err = mc.ForModule(ctx, b.set.skills)
	if err != nil {
		return skills.Assignment{}, err
	}
	var complete func(api.RecordedResult)
	mc, complete, err = mc.AdmitOperation(ctx, api.RecordedCall{Namespace: "skills", Method: "POST", Pattern: "/assignments", Permission: "skills:assignment:write", Params: map[string]string{"target_kind": target.Kind, "target_id": target.ID}})
	if err != nil {
		if errors.Is(err, api.ErrModuleOperationAdmission) {
			err = auth.ErrRouteDenied
		}
		return skills.Assignment{}, err
	}
	defer func() {
		status := http.StatusOK
		if operationErr != nil {
			status = skills.OperationErrorStatus(operationErr)
		}
		encoded, err := json.Marshal(assignment)
		if err != nil {
			complete(api.RecordedResult{Status: status})
			return
		}
		digest := sha256.Sum256(encoded)
		complete(api.RecordedResult{Status: status, BodySHA256: digest[:], BodyBytes: int64(len(encoded))})
	}()
	mc.Resource = resource
	mc.BodyEntityFields = map[string]string{"target_kind": target.Kind, "target_id": target.ID}
	ref, ok := mc.Principal.Ref()
	if !ok {
		return skills.Assignment{}, auth.ErrRouteDenied
	}
	mc.Principal, err = b.authr.ResolvePrincipalScope(ctx, ref, mc.Tenant)
	if err != nil {
		return skills.Assignment{}, err
	}
	request := auth.Request{Tenant: mc.Tenant, Principal: mc.Principal, Permission: "skills:catalog:write", Resource: auth.ResourceFor("skills:catalog:write")}
	authority, err := b.authz.AuthorizeRouteMutation(ctx, request)
	if err != nil {
		return skills.Assignment{}, err
	}
	metadata, err := authority.MetadataFor(time.Now(), request)
	if err != nil {
		return skills.Assignment{}, err
	}
	ctx, cancel := context.WithDeadline(ctx, metadata.FreshUntil)
	defer cancel()
	mc.Data = wikiCatalogData{ScopedData: mc.Data, request: request, authority: authority}
	return b.set.skills.EnsureWikiFolder(ctx, mc, target)
}
