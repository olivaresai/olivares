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
	"maps"
	"net"
	"net/http"
	"net/url"
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
	roster     *auth.SourceStore
}

// businessRowRefusal answers an ingest by the name of a roster document row of this tenant whose
// kind is a Business connector this build does not construct (a Community engine reading what a
// Business engine stored) with the one Business refusal, 501 connector_unavailable. Any other
// name keeps the ordinary unknown-source answer.
func (s workspaceKnowledgeSources) businessRowRefusal(ctx context.Context, mc api.ModuleContext, name string) (*knowledge.OpenedSource, string, int) {
	if s.roster == nil {
		return nil, "", 0
	}
	def, ok, err := s.roster.Get(ctx, auth.GlobalSourceScope, name)
	if err != nil || !ok || !isDocumentRow(def) || def.Tenant != mc.Tenant.String() || !businessKindUnavailable(def.Kind) {
		return nil, "", 0
	}
	return nil, errBusinessSourceKind(def.Kind).Error(), http.StatusNotImplemented
}

func (s workspaceKnowledgeSources) Open(ctx context.Context, mc api.ModuleContext, kbRef, sourceName string) (*knowledge.OpenedSource, string, int) {
	if !strings.HasPrefix(sourceName, "workspace:") {
		return s.businessRowRefusal(ctx, mc, sourceName)
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
	if businessKindUnavailable(connector.kind) {
		return nil, errBusinessSourceKind(connector.kind).Error(), http.StatusNotImplemented
	}
	switch connector.kind {
	case "confluence", "gdrive", "notion", "sharepoint", "sap_odata", "salesforce", "snowflake", "azure_ai_search":
	case "s3content":
		// The connector falls back to the engine's own AWS environment for any credential
		// it is not given, so it is admitted only where the person supplies all of them.
		if strings.TrimSpace(connector.config["identity"]) != "user" {
			return nil, "workspace ingest does not support this document source kind", http.StatusBadRequest
		}
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
		if key == "identity" {
			continue // the opener's own switch, read below; never a connector setting
		}
		field, known := fields[key]
		if !known || key == "export_path" || field.Secret || secret.IsCredentialBearingConfigKey(key) || secret.IsReference(value) || secret.ContainsInlineCredential(value) {
			return nil, "workspace source configuration must use declared live settings and workspace secret references", http.StatusBadRequest
		}
		settings[key] = value
	}
	if strings.TrimSpace(settings["mode"]) != "live" {
		return nil, "workspace document ingest requires live mode", http.StatusBadRequest
	}
	readAsUser, refusal, status := workspaceSourceIdentity(connector.kind, connector.config["identity"], len(connector.secrets) > 0)
	if refusal != "" {
		return nil, refusal, status
	}
	live, isLive := src.(contentsource.LiveSource)
	if readAsUser && !isLive {
		return nil, "workspace document source kind cannot read as the user", http.StatusBadRequest
	}
	var onlyACL string
	if readAsUser {
		credential, refusal, status := s.personCredential(ctx, mc, name, connector.kind, settings, fields)
		if refusal != "" {
			return nil, refusal, status
		}
		maps.Copy(settings, credential)
		onlyACL = "user:" + mc.Principal.UserID.String()
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
	opened := wrapContentSourceMode(src, "live")
	if readAsUser {
		opened = modeLiveContentSource{LiveSource: live, mode: "live", onlyACL: onlyACL}
	}
	return &knowledge.OpenedSource{Source: opened, Validate: validate}, "", 0
}

// workspaceSourceIdentity reads a workspace source's identity setting: "service" (the
// default) reads with the workspace's shared credential, "user" with the credential the
// requesting person keeps in their own vault. User is for the sources whose live API takes
// a person's bearer token, and a source that names both a shared credential and the user
// is refused, so no request is ever made under the wrong one.
func workspaceSourceIdentity(kind, identity string, hasSharedCredential bool) (readAsUser bool, refusal string, status int) {
	switch strings.TrimSpace(identity) {
	case "", "service":
		return false, "", 0
	case "user":
	default:
		return false, `workspace source identity must be "service" or "user"`, http.StatusBadRequest
	}
	if _, ok := personalEndpoints[kind]; !ok {
		return false, "workspace document source kind cannot read as the user", http.StatusBadRequest
	}
	if hasSharedCredential {
		return false, "a workspace source that reads as the user must not hold a shared credential", http.StatusBadRequest
	}
	return true, "", 0
}

// personalEndpoints names, for each source kind that can read as the user, the setting
// that says where its bearer token is sent and the service's own address when that is
// unset. A person's token goes only to the origin they stored it for.
var personalEndpoints = map[string]struct{ setting, fallback string }{
	"confluence": {setting: "base_url"},
	"gdrive":     {setting: "api_base", fallback: "https://www.googleapis.com/drive/v3"},
	"notion":     {setting: "base_url", fallback: "https://api.notion.com/v1"},
	"sharepoint": {fallback: "https://graph.microsoft.com/v1.0"},
	// An object store's token service answers at its own address, and at AWS's when none is set.
	"s3content": {setting: "endpoint", fallback: "https://sts.amazonaws.com"},
}

// endpointOrigin returns scheme://host:port of an http(s) address, with the default port filled in.
func endpointOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	switch {
	case scheme == "https" && port == "":
		port = "443"
	case scheme == "http" && port == "":
		port = "80"
	case scheme != "http" && scheme != "https":
		return "", false
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), true
}

// personCredential opens the requesting person's own credential for a source: the vault
// item sources/<name>, a login whose password is their token and whose url is the service
// it belongs to. The token is released only when that url has the origin the source is
// configured to call, because whoever manages the connector chooses its endpoint and the
// token is the person's. Without it the source is not read, and there is no fallback to
// any other credential. The reveal is on the ledger.
func (s workspaceKnowledgeSources) personCredential(ctx context.Context, mc api.ModuleContext, name, kind string, settings map[string]string, fields map[string]sdk.ConfigField) (map[string]string, string, int) {
	var secretFields []string
	for key, field := range fields {
		if field.Secret {
			secretFields = append(secretFields, key)
		}
	}
	vaultName, ok := auth.PersonalVaultName(mc.Principal, "sources/"+name)
	if !ok {
		return nil, "reading a source as the user needs a signed-in person", http.StatusForbidden
	}
	endpoint := personalEndpoints[kind]
	callsTo := strings.TrimSpace(settings[endpoint.setting])
	if callsTo == "" {
		callsTo = endpoint.fallback
	}
	callsOrigin, validEndpoint := endpointOrigin(callsTo)
	if (kind != "s3content" && len(secretFields) != 1) || auth.ValidateSecretName(vaultName) != "" || !validEndpoint {
		return nil, "workspace source cannot read as the user", http.StatusBadRequest
	}
	connect := "connect your account: store your credential for " + name + " in your vault as sources/" + name +
		", a login whose password is your token and whose url is " + callsOrigin
	if kind == "s3content" {
		connect += ", and whose user name is the role to assume (empty where the storage needs none)"
	}
	raw, err := s.vault.Reveal(ctx, mc.Principal, mc.Tenant, vaultName)
	switch {
	case errors.Is(err, auth.ErrSecretNotFound):
		return nil, connect, http.StatusForbidden
	case err != nil:
		return nil, "your credential could not be read", http.StatusServiceUnavailable
	}
	item, err := auth.ParseVaultItem(string(raw))
	clear(raw)
	token := strings.TrimSpace(item.Password)
	if err != nil || item.Type != auth.VaultLogin || token == "" {
		return nil, "your credential for " + name + " is not a login holding your token: " + connect, http.StatusForbidden
	}
	if storedOrigin, _ := endpointOrigin(item.URL); storedOrigin != callsOrigin {
		return nil, connect, http.StatusForbidden
	}
	if kind != "s3content" {
		return map[string]string{secretFields[0]: token}, "", 0
	}
	// An object store takes the token to its token service, and only the temporary
	// credentials it issues sign the reads. The login's user name is the role to assume.
	// shortcut: the credentials are not refreshed, so an ingest that outlasts their lifetime
	// (an hour by default) fails and is run again; refresh them when ingests take longer.
	credentials, err := assumeWebIdentity(ctx, callsTo, strings.TrimSpace(item.Username), token, "olivares-"+mc.Principal.UserID.String())
	var refused stsRefused
	switch {
	case errors.As(err, &refused):
		return nil, "connect your account: " + refused.Error() + ". Store a current identity token for " + name + " in your vault as sources/" + name, http.StatusForbidden
	case err != nil:
		return nil, err.Error(), http.StatusBadGateway
	}
	return map[string]string{"access_key_id": credentials.AccessKeyID, "secret_access_key": credentials.SecretAccessKey, "session_token": credentials.SessionToken}, "", 0
}
