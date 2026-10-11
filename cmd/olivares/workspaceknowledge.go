// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/sourcescope"
	"github.com/olivaresai/olivares/sdk"
)

// Administrator ingest selects a connector from the KB's stored, exclusive
// workspace. It cannot borrow a global connector or the engine's env/file secrets.
type workspaceKnowledgeSources struct {
	principals *auth.Authenticator
	scopes     *sourcescope.Resolver
	authz      *auth.Authorizer
	vault      *auth.SecretStore
}

func (s workspaceKnowledgeSources) Open(ctx context.Context, mc api.ModuleContext, kbRef, sourceName string) (*knowledge.OpenedSource, string, int) {
	if !strings.HasPrefix(sourceName, "workspace:") {
		return nil, "", 0 // preserve ordinary unknown-source handling
	}
	name := strings.TrimPrefix(sourceName, "workspace:")
	if name == "" || mc.Data == nil || mc.Tenant.IsZero() || mc.Tenant == model.SystemTenantID || s.principals == nil || s.scopes == nil || s.authz == nil || s.vault == nil {
		return nil, "workspace source authority is unavailable", http.StatusServiceUnavailable
	}
	credential, valid := mc.Principal.Ref()
	if !valid {
		return nil, "workspace source ingest is not authorized", http.StatusForbidden
	}
	principal, err := s.principals.ResolvePrincipalScope(ctx, credential, mc.Tenant)
	if err != nil {
		return nil, "workspace source ingest is not authorized", http.StatusForbidden
	}
	mc.Principal = principal
	var ws model.Workspace
	var sourceVersion string
	var connectorVersion string
	var connector struct {
		id, kind        string
		config, secrets map[string]string
	}
	err = mc.Data.View(ctx, func(sc store.Scope) error {
		if sc.Tenant() != mc.Tenant {
			return store.ErrNoTenant
		}
		var err error
		ws, err = sourcescope.SourceWorkspace(ctx, sc, sourcescope.SourceKnowledge, kbRef)
		if err != nil {
			return err
		}
		sourceVersion, err = sourcescope.SourceAuthorityVersion(ctx, sc, mc.Principal, sourcescope.SourceKnowledge, kbRef)
		if err != nil {
			return err
		}
		connectors, err := sourcescope.ListWorkspaceConnectors(ctx, sc, ws.Slug)
		if err != nil {
			return err
		}
		for _, candidate := range connectors {
			if candidate.Name == name {
				connector.id, connector.kind = candidate.ID, candidate.Kind
				connector.config, connector.secrets = candidate.Config, candidate.Secrets
				encoded, err := json.Marshal(candidate)
				if err != nil {
					return err
				}
				connectorVersion = string(encoded)
				return nil
			}
		}
		return store.ErrNotFound
	})
	switch {
	case errors.Is(err, sourcescope.ErrSourceWorkspace):
		return nil, "knowledge base must be confined to exactly one workspace for workspace ingest", http.StatusConflict
	case errors.Is(err, store.ErrNotFound):
		return nil, "enabled workspace document source was not found", http.StatusNotFound
	case err != nil:
		return nil, "workspace source authority could not be read", http.StatusServiceUnavailable
	}
	decision, err := s.scopes.ResolveForAgent(ctx, mc.Tenant, mc.Principal, "", sourcescope.SourceKnowledge, kbRef)
	if err != nil {
		return nil, "workspace source authority could not be evaluated", http.StatusServiceUnavailable
	}
	question := auth.Request{
		Principal: mc.Principal, Tenant: mc.Tenant, Permission: "sourcescope:workspace_connector:write",
		Resource: auth.ResourceAttrs{Kind: "sourcescope.workspace_connector", ID: connector.id, WorkspaceID: ws.ID},
	}
	authority, err := s.authz.AuthorizeRouteMutation(ctx, question)
	if !decision.Allowed || err != nil {
		return nil, "workspace source ingest is not authorized", http.StatusForbidden
	}
	validate := func(ctx context.Context, sc store.Scope, stage knowledge.SourceValidation) (checkErr error) {
		if sc.Tenant() != mc.Tenant {
			return store.ErrConflict
		}
		clock, ok := sc.(store.TransactionClock)
		if !ok {
			return store.ErrConflict
		}
		now, err := clock.TransactionNow(ctx)
		if err != nil || now.IsZero() {
			return store.ErrConflict
		}
		bundle, err := authority.AuthorityFor(now.Time(), question)
		if err != nil {
			return store.ErrConflict
		}
		if stage == knowledge.SourceFreshness {
			return nil
		}
		if stage != knowledge.SourceRead && stage != knowledge.SourcePin {
			return store.ErrConflict
		}
		defer func() {
			if checkErr != nil {
				return
			}
			current, err := clock.TransactionNow(ctx)
			if err != nil || current.IsZero() {
				checkErr = store.ErrConflict
				return
			}
			if _, err := authority.AuthorityFor(current.Time(), question); err != nil {
				checkErr = store.ErrConflict
			}
		}()
		if stage == knowledge.SourceRead {
			reader, ok := sc.(store.AuthoritySnapshotBundleReader)
			if !ok {
				return store.ErrConflict
			}
			if err := reader.ValidateAuthoritySnapshotBundle(ctx, bundle); err != nil {
				return store.ErrConflict
			}
		}
		if stage == knowledge.SourcePin {
			// Human-session changes can advance only the User fence. The native
			// directory barrier pins it with all tenant facts before product writes.
			locker, ok := sc.(store.DirectoryAuthoritySnapshotLocker)
			if !ok {
				return store.ErrConflict
			}
			if err := locker.LockDirectoryAuthoritySnapshot(ctx, bundle); err != nil {
				return store.ErrConflict
			}
		}
		currentVersion, err := sourcescope.SourceAuthorityVersion(ctx, sc, mc.Principal, sourcescope.SourceKnowledge, kbRef)
		if err != nil || currentVersion != sourceVersion {
			return store.ErrConflict
		}
		currentWorkspace, err := sourcescope.SourceWorkspace(ctx, sc, sourcescope.SourceKnowledge, kbRef)
		if err != nil || currentWorkspace.ID != ws.ID {
			return store.ErrConflict
		}
		connectors, err := sourcescope.ListWorkspaceConnectors(ctx, sc, ws.Slug)
		if err != nil {
			return store.ErrConflict
		}
		for _, current := range connectors {
			if current.ID == connector.id {
				encoded, err := json.Marshal(current)
				if err == nil && string(encoded) == connectorVersion {
					return nil
				}
				return store.ErrConflict
			}
		}
		return store.ErrConflict
	}
	if err := mc.Data.View(ctx, func(sc store.Scope) error { return validate(ctx, sc, knowledge.SourceRead) }); err != nil {
		return nil, "workspace source changed before ingest; retry with its current settings", http.StatusConflict
	}

	// These live HTTP adapters read only their declared settings. S3 has ambient
	// AWS fallback; PostgreSQL DSNs may select host files/environment. Those and
	// filesystem/export modes need separate admitted authority before consumption.
	switch connector.kind {
	case "confluence", "gdrive", "notion", "sharepoint", "sap_odata", "salesforce", "snowflake", "azure_ai_search":
	default:
		return nil, "workspace ingest does not support this document source kind", http.StatusBadRequest
	}
	src, ok := buildContentSource(connector.kind, nil)
	if !ok || src.Kind() != contentsource.ClassDocument {
		return nil, "workspace ingest does not support this document source kind", http.StatusBadRequest
	}
	fields := map[string]sdk.ConfigField{}
	for _, field := range src.Descriptor().ConfigFields {
		fields[field.Key] = field
	}
	settings := make(map[string]string, len(connector.config)+len(connector.secrets))
	for key, value := range connector.config {
		field, known := fields[key]
		if !known || key == "export_path" || field.Secret || secret.IsCredentialBearingConfigKey(key) || secret.IsReference(value) || secret.ContainsInlineCredential(value) {
			return nil, "workspace source configuration must use declared live settings and workspace secret references", http.StatusBadRequest
		}
		settings[key] = value
	}
	if strings.TrimSpace(settings["mode"]) != "live" {
		return nil, "workspace document ingest requires live mode", http.StatusBadRequest
	}
	prefix := fmt.Sprintf("ws/%x/", sha256.Sum256([]byte(ws.Slug)))
	for key, value := range connector.secrets {
		field, known := fields[key]
		ref, valid := secret.ParseReference(value)
		if !known || !field.Secret || !valid || ref.Scheme != secret.SchemeStore || !strings.HasPrefix(ref.Locator, prefix) {
			return nil, "workspace source credentials must be references in this workspace vault", http.StatusForbidden
		}
		resolved, err := s.vault.Resolve(ctx, mc.Tenant, ref.Locator)
		if err != nil {
			return nil, "workspace source credential is unavailable", http.StatusServiceUnavailable
		}
		settings[key] = string(resolved)
	}
	if err := src.Open(ctx, sdk.Config{Settings: settings}); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = src.Close(cleanup)
		cancel()
		return nil, "workspace document source could not be opened", http.StatusBadGateway
	}
	return &knowledge.OpenedSource{Source: wrapContentSourceMode(src, "live"), Validate: validate}, "", 0
}
