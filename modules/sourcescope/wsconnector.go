// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
)

// workspace-scoped connector definitions. A workspace admin creates connectors
// confined to their workspace. The config column carries non-secret settings; the
// secrets_ref column carries REFERENCES only (store:ws/…, env:…, vault:…), never
// values — the same structural invariant as binding.cred_ref and the global connector
// onboarding (connectoronboard.go). Inline secrets typed by the workspace admin are
// sealed into the sealed store under a workspace-namespaced path
// (ws/<workspace>/<connector>/<field>) by the composition-root WorkspaceConnectorSealer.

// WorkspaceConnectorSealer is the interface the composition root implements to seal
// inline secrets for workspace connectors. The module calls it on create/update; the
// composition root resolves it to the SecretStore under a workspace-namespaced
// path. A nil sealer means workspace connector secrets are not available (the module
// operates in reference-only mode).
type WorkspaceConnectorSealer interface {
	SealWorkspaceSecret(ctx context.Context, workspace, connector, field, value, actor string) (ref string, err error)
	DeleteWorkspaceSecrets(ctx context.Context, workspace, connector, actor string) error
}

// wsConnectorDTO is the wire shape of a workspace-scoped connector definition.
type wsConnectorDTO struct {
	ID           string            `json:"id,omitempty"`
	Name         string            `json:"name"`
	Kind         string            `json:"kind"`
	WorkspaceRef string            `json:"workspace_ref"`
	Config       map[string]string `json:"config,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
	PollSeconds  int               `json:"poll_seconds,omitempty"`
	Enabled      bool              `json:"enabled"`
	Note         string            `json:"note,omitempty"`
	Status       string            `json:"status,omitempty"`
}

func (d *wsConnectorDTO) validate() string {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" {
		return "name is required"
	}
	if len(d.Name) > 128 {
		return "name is too long"
	}
	d.Kind = strings.TrimSpace(d.Kind)
	if d.Kind == "" {
		return "kind is required"
	}
	d.WorkspaceRef = strings.TrimSpace(d.WorkspaceRef)
	if d.WorkspaceRef == "" {
		return "workspace_ref is required"
	}
	if d.PollSeconds < 0 {
		return "poll_seconds must be non-negative"
	}
	d.Note = strings.TrimSpace(d.Note)
	if len(d.Note) > maxNoteLen {
		return "note is too long"
	}
	for key, val := range d.Config {
		if val == "" {
			continue
		}
		if secret.IsCredentialBearingConfigKey(key) {
			return "credential-bearing fields must be supplied through secrets, never config"
		}
		if containsInlineCredential(val) {
			return "config values must not contain inline credentials; supply them through secrets"
		}
	}
	for _, val := range d.Secrets {
		if containsInlineCredential(val) {
			return "secrets values must be references or inline literals, never URLs with embedded credentials"
		}
	}
	return ""
}

func marshalMap(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseMap(s string) map[string]string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func (d wsConnectorDTO) fields(wsID model.ID, createdBy string) model.Record {
	return model.Record{
		colWCName:        d.Name,
		colWCKind:        d.Kind,
		colWCWorkspace:   d.WorkspaceRef,
		colWCWsID:        wsID.String(),
		colWCConfig:      marshalMap(d.Config),
		colWCSecretsRef:  marshalMap(d.Secrets),
		colWCPollSeconds: d.PollSeconds,
		colWCEnabled:     d.Enabled,
		colWCCreatedBy:   createdBy,
		colWCNote:        d.Note,
		colWCStatus:      d.Status,
	}
}

func toWsConnectorDTO(rec model.Record) wsConnectorDTO {
	cfg := parseMap(rec.String(colWCConfig))
	if cfg == nil {
		cfg = map[string]string{}
	}
	secrets := parseMap(rec.String(colWCSecretsRef))
	if secrets == nil {
		secrets = map[string]string{}
	}
	return wsConnectorDTO{
		ID:           rec.String(model.ColID),
		Name:         rec.String(colWCName),
		Kind:         rec.String(colWCKind),
		WorkspaceRef: rec.String(colWCWorkspace),
		Config:       cfg,
		Secrets:      redactSecretsRef(secrets),
		PollSeconds:  int(rec.Int(colWCPollSeconds)),
		Enabled:      rec.Bool(colWCEnabled),
		Note:         rec.String(colWCNote),
		Status:       rec.String(colWCStatus),
	}
}

// redactSecretsRef masks the secret reference values in the response: the response
// shows whether a secret exists for a field (key present, value = "***") but never
// the locator. An empty value means the field has no secret.
func redactSecretsRef(secrets map[string]string) map[string]string {
	out := make(map[string]string, len(secrets))
	for k, v := range secrets {
		if v != "" {
			out[k] = "***"
		} else {
			out[k] = ""
		}
	}
	return out
}

// handleListWsConnectors lists workspace-scoped connectors, optionally filtered by
// ?workspace_ref / ?kind.
func (m *Module) handleListWsConnectors(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, f := range []struct{ param, col string }{
		{"workspace_ref", colWCWorkspace}, {"kind", colWCKind},
	} {
		if v := r.URL.Query().Get(f.param); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := listResponse[wsConnectorDTO]{Items: []wsConnectorDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wsConnectorKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toWsConnectorDTO(rec))
		}
		out.Cursor, out.HasMore = page.Cursor, page.HasMore
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateWsConnector creates a workspace-scoped connector. An inline secret
// value in the secrets map is sealed by the composition-root sealer under the
// workspace namespace; a reference value is stored verbatim. The config column
// stores non-secret settings only.
func (m *Module) handleCreateWsConnector(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in wsConnectorDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var out wsConnectorDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		wsID, _, err := resolveScope(r.Context(), sc, scopeWorkspace, &in.WorkspaceRef)
		if err != nil {
			return err
		}
		sealedSecrets, err := m.sealWsSecrets(r.Context(), in.WorkspaceRef, in.Name, in.Secrets, nil, mc.Principal.Actor())
		if err != nil {
			return err
		}
		in.Secrets = sealedSecrets
		in.Status = "pending"
		repo, err := sc.Ext(wsConnectorKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.fields(wsID, mc.Principal.Actor()))
		if err != nil {
			return err
		}
		out = toWsConnectorDTO(rec)
		return auditWsConnector(r.Context(), sc, mc, "create", in)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// handleGetWsConnector returns one workspace connector.
func (m *Module) handleGetWsConnector(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var out wsConnectorDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wsConnectorKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		out = toWsConnectorDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpdateWsConnector updates a workspace connector. The name, kind and
// workspace_ref are immutable (they form the natural key + unique index).
func (m *Module) handleUpdateWsConnector(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in wsConnectorDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	var out wsConnectorDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wsConnectorKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		in.Name = rec.String(colWCName)
		in.Kind = rec.String(colWCKind)
		in.WorkspaceRef = rec.String(colWCWorkspace)
		if msg := in.validate(); msg != "" {
			return validationError(msg)
		}
		existingSecrets := parseMap(rec.String(colWCSecretsRef))
		sealedSecrets, err := m.sealWsSecrets(r.Context(), in.WorkspaceRef, in.Name, in.Secrets, existingSecrets, mc.Principal.Actor())
		if err != nil {
			return err
		}
		in.Secrets = sealedSecrets
		wsID := model.ID(rec.String(colWCWsID))
		for k, v := range in.fields(wsID, rec.String(colWCCreatedBy)) {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toWsConnectorDTO(rec)
		return auditWsConnector(r.Context(), sc, mc, "update", in)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDeleteWsConnector removes a workspace connector and cascade-deletes its
// workspace-owned sealed secrets.
func (m *Module) handleDeleteWsConnector(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(wsConnectorKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		snap := toWsConnectorDTO(rec)
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		m.deleteWsSecrets(r.Context(), snap.WorkspaceRef, snap.Name, mc.Principal.Actor())
		return auditWsConnector(r.Context(), sc, mc, "delete", snap)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// sealWsSecrets processes the incoming secrets map: a blank value keeps the existing
// reference; a reference value (env:…, vault:…, store:…) is stored verbatim; any
// other literal is sealed through the WorkspaceConnectorSealer. If no sealer is
// wired, literals are rejected (reference-only mode).
func (m *Module) sealWsSecrets(ctx context.Context, workspace, connector string, incoming, existing map[string]string, actor string) (map[string]string, error) {
	if len(incoming) == 0 {
		if existing != nil {
			return existing, nil
		}
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(incoming))
	for field, val := range incoming {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		switch {
		case val == "":
			if existing != nil {
				if ref, ok := existing[field]; ok {
					out[field] = ref
				}
			}
		case isSecretReference(val):
			out[field] = val
		default:
			m.mu.RLock()
			sealer := m.wsSealer
			m.mu.RUnlock()
			if sealer == nil {
				return nil, validationError("inline secrets require a workspace secret sealer (not configured)")
			}
			ref, err := sealer.SealWorkspaceSecret(ctx, workspace, connector, field, val, actor)
			if err != nil {
				return nil, err
			}
			out[field] = ref
		}
	}
	return out, nil
}

// deleteWsSecrets cascade-deletes workspace-owned sealed secrets (best-effort).
func (m *Module) deleteWsSecrets(ctx context.Context, workspace, connector, actor string) {
	m.mu.RLock()
	sealer := m.wsSealer
	m.mu.RUnlock()
	if sealer == nil {
		return
	}
	if err := sealer.DeleteWorkspaceSecrets(ctx, workspace, connector, actor); err != nil && m.log != nil {
		m.log.Warn("workspace connector: could not delete workspace secrets after removal",
			"workspace", workspace, "connector", connector, "err", err)
	}
}

// isSecretReference reports whether val looks like a secret reference (scheme:locator)
// rather than an inline literal. It matches the same vocabulary as the binding
// credential ref_kind.
func isSecretReference(val string) bool {
	for _, scheme := range []string{"env:", "vault:", "secret_manager:", "file:", "store:", "other:"} {
		if strings.HasPrefix(val, scheme) {
			return true
		}
	}
	return false
}

// auditWsConnector appends a self-audit event for a workspace connector change.
func auditWsConnector(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, d wsConnectorDTO) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "sourcescope.workspace_connector." + verb,
		TargetKind: wsConnectorKind,
		Meta: map[string]any{
			"name":          d.Name,
			"kind":          d.Kind,
			"workspace_ref": d.WorkspaceRef,
			"enabled":       d.Enabled,
		},
	})
	return err
}

// ListWorkspaceConnectors returns the enabled workspace connectors for a given
// workspace, used by the resolver to enumerate workspace-scoped sources.
func ListWorkspaceConnectors(ctx context.Context, sc store.Scope, workspaceSlug string) ([]wsConnectorDTO, error) {
	recs, err := allExt(ctx, sc, wsConnectorKind, eq(colWCWorkspace, workspaceSlug))
	if err != nil {
		return nil, err
	}
	out := make([]wsConnectorDTO, 0, len(recs))
	for _, rec := range recs {
		if !rec.Bool(colWCEnabled) {
			continue
		}
		dto := toWsConnectorDTO(rec)
		dto.Secrets = parseMap(rec.String(colWCSecretsRef))
		out = append(out, dto)
	}
	return out, nil
}
