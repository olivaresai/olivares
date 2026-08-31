// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
)

// maxHintLen bounds a masked-hint field (a short masked partial like "sk-…aB12"); a
// longer value is rejected as a guard against an operator pasting a full credential.
const maxHintLen = 64

// maxNoteLen bounds the free-text note.
const maxNoteLen = 512

// validSourceTypes are the connected-source kinds a binding may target.
var validSourceTypes = map[string]bool{
	sourceMCP: true, sourceModel: true, sourceProvider: true, sourceKnowledge: true, sourceData: true,
}

// Binding effect, mirroring model-access vocabulary. An empty stored value
// is an allow (back-compat). A forbid subtracts (forbid-overrides-allow, absolute).
const (
	effectAllow  = "allow"
	effectForbid = "forbid"
)

// validScopeTrees are the tree kinds a binding may bind to: the containment nodes
// (workspace, agent-group folder) plus the subject axes (session, agent, user,
// user_group, role). See ADR-0022 §1.
var validScopeTrees = map[string]bool{
	scopeWorkspace: true, scopeAgentGroup: true, scopeFolder: true,
	scopeSession: true, scopeAgent: true, scopeUser: true, scopeUserGroup: true, scopeRole: true,
}

// subjectTrees are the axes matched at resolve against the authenticated actor
// (principal / route-gated session-agent ref). They are shape-validated only — the auth
// subjects (user, directory group, role) are not reachable from the module's tenant store
// scope, and an unknown ref never matches an actor (deny-closed), exactly as does.
var subjectTrees = map[string]bool{
	scopeSession: true, scopeAgent: true, scopeUser: true, scopeUserGroup: true, scopeRole: true,
}

// normalizeEffect maps a stored effect to its canonical value: an empty/legacy value is an
// allow (the pre default), so a response always reads a concrete effect. Identical
// semantics to normalizeEffect.
func normalizeEffect(stored string) string {
	if stored == effectForbid {
		return effectForbid
	}
	return effectAllow
}

// validRefKinds are the locator kinds a scoped credential reference may use (where
// the credential actually lives — the binding stores only the reference, never the
// value, docs/SECURITY-HARDENING.md). Empty (no scoped credential) is allowed.
var validRefKinds = map[string]bool{"env": true, "vault": true, "secret_manager": true, "file": true, "other": true}

// bindingDTO is the wire shape of a source→scope binding. The credential surface is a
// REFERENCE only (name + ref_kind + locator + masked hint), never a value.
type bindingDTO struct {
	ID         string `json:"id,omitempty"`
	SourceType string `json:"source_type"`
	SourceRef  string `json:"source_ref"`
	ScopeTree  string `json:"scope_tree"`
	ScopeRef   string `json:"scope_ref,omitempty"`
	// Effect is "allow" (default) or "forbid". Omitted on input ⇒ allow.
	Effect string `json:"effect,omitempty"`
	// FolderPath is OUTPUT-ONLY: the store-resolved Path of a folder binding's anchor
	//, surfaced for readability. It is ignored on input — the anchor is scope_ref
	// (the folder id) and the Path is always resolved server-side.
	FolderPath  string `json:"folder_path,omitempty"`
	CredName    string `json:"cred_name,omitempty"`
	CredRefKind string `json:"cred_ref_kind,omitempty"`
	CredRef     string `json:"cred_ref,omitempty"`
	CredHint    string `json:"cred_hint,omitempty"`
	Enabled     bool   `json:"enabled"`
	Note        string `json:"note,omitempty"`
}

// validate normalizes and form-checks an incoming binding, enforcing the minimal-data
// invariant (the credential locator is a reference, never an inline secret). Scope
// existence (the workspace/agent-group slug actually exists) is checked in the Mutate
// path against the store; this only validates shape.
func (d *bindingDTO) validate() string {
	d.SourceType = strings.TrimSpace(strings.ToLower(d.SourceType))
	if !validSourceTypes[d.SourceType] {
		return "source_type must be one of mcp, model, provider, knowledge, data"
	}
	d.SourceRef = strings.TrimSpace(d.SourceRef)
	if d.SourceRef == "" {
		return "source_ref is required"
	}
	d.ScopeTree = strings.TrimSpace(strings.ToLower(d.ScopeTree))
	if !validScopeTrees[d.ScopeTree] {
		return "scope_tree must be one of workspace, agent_group, folder, session, agent, user, user_group, role"
	}
	d.ScopeRef = strings.TrimSpace(d.ScopeRef)
	if d.ScopeTree == scopeAgentGroup && d.ScopeRef == "" {
		return "scope_ref (agent-group slug) is required for an agent_group binding"
	}
	if d.ScopeTree == scopeFolder && d.ScopeRef == "" {
		return "scope_ref (folder Resource id) is required for a folder binding"
	}
	// Subject trees require a non-empty scope_ref (unlike workspace, whose empty ref
	// means the default workspace). SHAPE-ONLY: no store lookup — an unknown subject never
	// matches the actor at resolve (deny-closed), the pattern (ADR-0022 §1).
	if subjectTrees[d.ScopeTree] && d.ScopeRef == "" {
		return "scope_ref is required for a " + d.ScopeTree + " binding"
	}
	// Effect: allow (default; empty ⇒ allow) or forbid.
	d.Effect = strings.TrimSpace(strings.ToLower(d.Effect))
	if d.Effect == "" {
		d.Effect = effectAllow
	}
	if d.Effect != effectAllow && d.Effect != effectForbid {
		return "effect must be allow or forbid"
	}
	// Credential reference: all-or-nothing and value-free.
	d.CredName = strings.TrimSpace(d.CredName)
	d.CredRefKind = strings.TrimSpace(strings.ToLower(d.CredRefKind))
	d.CredRef = strings.TrimSpace(d.CredRef)
	d.CredHint = strings.TrimSpace(d.CredHint)
	if d.CredName != "" || d.CredRefKind != "" || d.CredRef != "" {
		if d.CredName == "" {
			return "a scoped credential needs a name"
		}
		if !validRefKinds[d.CredRefKind] {
			return "cred_ref_kind must be one of env, vault, secret_manager, file, other"
		}
		if d.CredRef == "" {
			return "a scoped credential needs a ref locator"
		}
		if containsInlineCredential(d.CredRef) {
			return "cred_ref must be a locator, never the credential value"
		}
	}
	if len(d.CredHint) > maxHintLen {
		return "cred_hint must be a short masked partial, never a full credential"
	}
	if len(d.Note) > maxNoteLen {
		return "note is too long"
	}
	return ""
}

// containsInlineCredential is the same defensive heuristic capabilities uses: it
// rejects the obvious ways a secret could end up persisted in a reference field
// (basic-auth userinfo, secret-like query params). It is a guardrail, not a scanner —
// the real guarantee is structural (the binding has no value column). It never stores
// the matched value.
func containsInlineCredential(s string) bool {
	return secret.ContainsInlineCredential(s)
}

// fields renders the binding's entity columns (the engine stamps the base columns).
// workspaceID is the scope's resolved workspace model.ID (for the grant-override
// declaredScope path); empty for a dangling/default scope.
func (d bindingDTO) fields(workspaceID model.ID, createdBy string) model.Record {
	return model.Record{
		colSourceType:  d.SourceType,
		colSourceRef:   d.SourceRef,
		colScopeTree:   d.ScopeTree,
		colScopeRef:    d.ScopeRef,
		colWorkspaceID: workspaceID.String(),
		colFolderPath:  d.FolderPath,
		colCredName:    d.CredName,
		colCredRefKind: d.CredRefKind,
		colCredRef:     d.CredRef,
		colCredHint:    d.CredHint,
		colEnabled:     d.Enabled,
		colCreatedBy:   createdBy,
		colNote:        d.Note,
		colEffect:      normalizeEffect(d.Effect), // concrete value (validate() sets it; defensive)
	}
}

// toBindingDTO renders a stored binding record to the wire DTO (the credential
// locator is surfaced — it is a reference, never a value; the hint is already masked).
func toBindingDTO(rec model.Record) bindingDTO {
	return bindingDTO{
		ID:          rec.String(model.ColID),
		SourceType:  rec.String(colSourceType),
		SourceRef:   rec.String(colSourceRef),
		ScopeTree:   rec.String(colScopeTree),
		ScopeRef:    rec.String(colScopeRef),
		Effect:      normalizeEffect(rec.String(colEffect)),
		FolderPath:  rec.String(colFolderPath),
		CredName:    rec.String(colCredName),
		CredRefKind: rec.String(colCredRefKind),
		CredRef:     rec.String(colCredRef),
		CredHint:    rec.String(colCredHint),
		Enabled:     rec.Bool(colEnabled),
		Note:        rec.String(colNote),
	}
}

// resolveScope validates the binding's scope against the store and returns the scope's
// workspace model.ID (used by the grant-override path) and, for a folder binding, the
// anchor folder's materialized Path (the advisory display snapshot; "" otherwise). A
// workspace binding must name an existing workspace (empty slug ⇒ the tenant default);
// an agent_group binding must name an existing group, whose workspace id is returned
// (zero ⇒ the group is workspace-unscoped → the default workspace); a folder binding
// must name an existing Resource by id, whose workspace id and Path are returned. An
// unknown slug/id is a validation error (deny-closed: a dangling scope must never
// silently bind to nothing).
func resolveScope(ctx context.Context, sc store.Scope, tree string, ref *string) (model.ID, string, error) {
	switch tree {
	case scopeWorkspace:
		slug := *ref
		if slug == "" {
			slug = model.DefaultWorkspaceSlug
			*ref = slug
		}
		ws, ok, err := findWorkspaceBySlug(ctx, sc, slug)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", validationError("no workspace with slug " + slug)
		}
		return ws.ID, "", nil
	case scopeAgentGroup:
		g, ok, err := findAgentGroupBySlug(ctx, sc, *ref)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", validationError("no agent-group with slug " + *ref)
		}
		return g.WorkspaceID, "", nil
	case scopeFolder:
		// The anchor is a Resource id (a folder is just a Resource of Kind "folder", but
		// any Resource node is a valid subtree root). We resolve its workspace (for audit/
		// consistency — the folder grant path does NOT use it) and its live Path (the
		// advisory display snapshot). The id, not the Path, is what the resolver hands the
		// Engine, so the Path may later lag a Move without affecting decisions.
		res, ok, err := findResourceByID(ctx, sc, *ref)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", validationError("no Resource (folder) with id " + *ref)
		}
		return res.WorkspaceID, res.Path, nil
	case scopeSession, scopeAgent, scopeUser, scopeUserGroup, scopeRole:
		// Subject trees: SHAPE-ONLY, no store lookup. The auth subjects (user,
		// directory group, role) are not reachable from this tenant store.Scope, and an
		// unknown session/agent ref simply never matches the acting actor at resolve
		// (deny-closed) — the subject pattern (ADR-0022 §1). No workspace id / path:
		// these carry no declaredScope and are decided by containment + row effect.
		return model.ID(""), "", nil
	default:
		return "", "", validationError("invalid scope_tree")
	}
}

// findWorkspaceBySlug returns the tenant's workspace with slug, and whether it exists.
func findWorkspaceBySlug(ctx context.Context, sc store.Scope, slug string) (model.Workspace, bool, error) {
	ws, _, err := sc.Workspaces().List(ctx, model.Query{Filters: []model.Filter{eq("slug", slug)}, Limit: 1})
	if err != nil {
		return model.Workspace{}, false, err
	}
	if len(ws) == 0 {
		return model.Workspace{}, false, nil
	}
	return ws[0], true, nil
}

// findAgentGroupBySlug returns the tenant's agent-group with slug, and whether it exists.
func findAgentGroupBySlug(ctx context.Context, sc store.Scope, slug string) (model.AgentGroup, bool, error) {
	gs, _, err := sc.AgentGroups().List(ctx, model.Query{Filters: []model.Filter{eq("slug", slug)}, Limit: 1})
	if err != nil {
		return model.AgentGroup{}, false, err
	}
	if len(gs) == 0 {
		return model.AgentGroup{}, false, nil
	}
	return gs[0], true, nil
}

// findResourceByID returns the tenant's Resource (the folder-binding anchor) by id, and
// whether it exists. A missing/other-tenant id is "not found" (deny-closed at bind time),
// never a hard error.
func findResourceByID(ctx context.Context, sc store.Scope, id string) (model.Resource, bool, error) {
	res, err := sc.Resources().Get(ctx, model.ID(id))
	if errors.Is(err, store.ErrNotFound) {
		return model.Resource{}, false, nil
	}
	if err != nil {
		return model.Resource{}, false, err
	}
	return res, true, nil
}

// validationError is a deferred validation failure raised from inside a Mutate closure
// (where the request is validated against the loaded scope), mapped to a 400 by the caller.
type validationError string

func (e validationError) Error() string { return string(e) }

// forbiddenError is refused with 403, not 409: the caller is not the right KIND of principal
// for this act and no retry or re-proposal changes that. A 409 would read as "try
// again later", which is exactly wrong for "a system token cannot decide a posture change".
type forbiddenError string

func (e forbiddenError) Error() string { return string(e) }

// --- handlers ----------------------------------------------------------------------

// handleListBindings lists the tenant's source→scope bindings, optionally filtered by
// ?source_type / ?source_ref / ?scope_tree.
func (m *Module) handleListBindings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	for _, f := range []struct{ param, col string }{
		{"source_type", colSourceType}, {"source_ref", colSourceRef}, {"scope_tree", colScopeTree},
	} {
		if v := r.URL.Query().Get(f.param); v != "" {
			q.Filters = append(q.Filters, eq(f.col, v))
		}
	}
	out := listResponse[bindingDTO]{Items: []bindingDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(bindingKind)
		if err != nil {
			return err
		}
		recs, page, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		for _, rec := range recs {
			out.Items = append(out.Items, toBindingDTO(rec))
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

// handleGetBinding returns one binding.
func (m *Module) handleGetBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var (
		out   bindingDTO
		found bool
	)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(bindingKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		found, out = true, toBindingDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCreateBinding binds a connected source to a workspace/agent-group, recording a
// self-audit attributed to the real principal. The scope is validated against the
// store inside the transaction (an unknown slug is rejected, deny-closed).
//
// an allow added to an ALREADY-CONFINED source widens who may reach it, so it is
// proposed rather than applied (202), exactly like a relaxing update. The first allow — the
// one that brings a source under governance — and every forbid still apply immediately.
func (m *Module) handleCreateBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in bindingDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	if msg := in.validate(); msg != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	var (
		out     bindingDTO
		pending *postureRequestDTO
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// EVERYTHING THAT CAN REFUSE THE WRITE RUNS BEFORE THE CLASSIFICATION, and that
		// ordering is the whole point. A proposal is only worth queueing if approving it can
		// succeed: whatever a gated create defers to approval, an approver meets as a 409 or
		// a 400 with the request stuck pending and no route to withdraw it. So the scope is
		// resolved here — an unknown workspace/agent-group/folder is deny-closed at PROPOSE
		// time, exactly as it is for an ungated create — and only then is the row classified.
		// (The update path defers this; it has the same wart, on a row that at least exists.)
		wsID, folderPath, err := resolveScope(r.Context(), sc, in.ScopeTree, &in.ScopeRef)
		if err != nil {
			return err
		}
		in.FolderPath = folderPath

		// F2: classified BEFORE the row exists, so the confinement count needs no
		// exclusion. resolveScope has already canonicalised the ref, so the duplicate check
		// below compares the same value the unique index will.
		duplicate, otherAllows, assignRows, err := createPreflight(r.Context(), sc, in.SourceType, in.SourceRef, scopeOf(in))
		if err != nil {
			return err
		}
		// A duplicate is refused HERE, before classification, for the same reason: the store's
		// unique index would refuse it either way, but for a gated create that refusal lands
		// on the APPROVER. The 409 this API has always returned stays a 409, for every effect.
		if duplicate {
			return store.ErrConflict
		}
		if relaxing, reason := classifyCreate(in, otherAllows, assignRows); relaxing {
			pr, perr := m.createPostureRequest(r.Context(), sc, mc, postureOpCreate, in.SourceType, in.SourceRef, "", reason, &in)
			if perr != nil {
				return perr
			}
			pending = &pr
			return nil
		}
		repo, err := sc.Ext(bindingKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(r.Context(), in.fields(wsID, mc.Principal.Actor()))
		if err != nil {
			return err
		}
		out = toBindingDTO(rec)
		return auditBinding(r.Context(), sc, mc, "create", in)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusAccepted, *pending) // relaxation awaits a second, distinct approver
		return
	}
	m.publishBindingEdges(r.Context(), mc.Tenant, out) // best-effort access-map projection
	writeJSON(w, http.StatusCreated, out)
}

// handleUpdateBinding updates a binding in place (scope, credential reference, enabled,
// note). The source_type/source_ref natural key is immutable: the URL targets a
// specific binding, so the incoming source identity is forced to the stored one.
func (m *Module) handleUpdateBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in bindingDTO
	if !decodeJSON(w, r, &in) {
		return
	}
	var (
		out     bindingDTO
		pending *postureRequestDTO
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(bindingKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		old := toBindingDTO(rec) // the posture BEFORE the change (F2 relax classification)
		// The source identity is the immutable natural key; force it to the stored row.
		in.SourceType = rec.String(colSourceType)
		in.SourceRef = rec.String(colSourceRef)
		if msg := in.validate(); msg != "" {
			return validationError(msg)
		}
		// F2: a RELAXING change is never applied by one actor — record a pending
		// dual-control request instead (ADR-0022 §5). Tightening/neutral changes apply now.
		otherAllows, err := countOtherEnabledAllows(r.Context(), sc, old.SourceType, old.SourceRef, id.String())
		if err != nil {
			return err
		}
		if relaxing, reason := classifyUpdate(old, in, otherAllows); relaxing {
			pr, perr := m.createPostureRequest(r.Context(), sc, mc, postureOpUpdate, old.SourceType, old.SourceRef, id.String(), reason, &in)
			if perr != nil {
				return perr
			}
			pending = &pr
			return nil
		}
		wsID, folderPath, err := resolveScope(r.Context(), sc, in.ScopeTree, &in.ScopeRef)
		if err != nil {
			return err
		}
		in.FolderPath = folderPath
		for k, v := range in.fields(wsID, rec.String(colCreatedBy)) {
			rec[k] = v
		}
		rec, err = repo.Update(r.Context(), rec)
		if err != nil {
			return err
		}
		out = toBindingDTO(rec)
		return auditBinding(r.Context(), sc, mc, "update", in)
	})
	if verr, ok := err.(validationError); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(string(verr)))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusAccepted, *pending) // relaxation awaits a second, distinct approver
		return
	}
	m.publishBindingEdges(r.Context(), mc.Tenant, out)
	writeJSON(w, http.StatusOK, out)
}

// createPreflight reads a source's existing bindings ONCE and answers the three questions
// the create path asks before classifying: is `want` already bound (the (source, scope)
// natural key of sourcescope_binding_uniq, schema.go:219-222), how many enabled ALLOW
// bindings the source already carries (the confinement signal), and how many assignment
// rows its REF carries (the second gate the first allow would switch off — E-2).
//
// Disabled rows count for the duplicate — the unique index does not look at `enabled` — and
// scopes are compared CANONICALLY (scopeOf), so the default workspace cannot be bound twice
// by spelling its ref two ways.
//
// The assignment count is keyed on the source REF ALONE, with no source_type filter, because
// that is exactly how the gate it models reads them: resolver.go:257-258 hands
// ConnectorAssigned the ref and nothing else. Keying it on the type here would count rows the
// resolver does not, and the classifier would be answering a different question from the one
// the enforcement path asks.
func createPreflight(ctx context.Context, sc store.Scope, sourceType, sourceRef string, want postureScope) (duplicate bool, enabledAllows, assignmentRows int, err error) {
	recs, err := allExt(ctx, sc, bindingKind, eq(colSourceType, sourceType), eq(colSourceRef, sourceRef))
	if err != nil {
		return false, 0, 0, err
	}
	for _, rec := range recs {
		b := toBindingDTO(rec)
		if scopeOf(b) == want {
			duplicate = true
		}
		if b.Enabled && normalizeEffect(b.Effect) != effectForbid {
			enabledAllows++
		}
	}
	arecs, err := allExt(ctx, sc, assignmentKind, eq(colAssignConnector, sourceRef))
	if err != nil {
		return false, 0, 0, err
	}
	return duplicate, enabledAllows, len(arecs), nil
}

// handleDeleteBinding removes a binding (the source reverts to global/unbound, or to
// any remaining bindings). A self-audit records who unbound it.
func (m *Module) handleDeleteBinding(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var pending *postureRequestDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(bindingKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		snap := toBindingDTO(rec)
		// F2: deleting a forbid, or the last enabled allow, RELAXES enforcement —
		// route it through dual-control instead of applying it (ADR-0022 §5).
		otherAllows, err := countOtherEnabledAllows(r.Context(), sc, snap.SourceType, snap.SourceRef, id.String())
		if err != nil {
			return err
		}
		if relaxing, reason := classifyDelete(snap, otherAllows); relaxing {
			pr, perr := m.createPostureRequest(r.Context(), sc, mc, postureOpDelete, snap.SourceType, snap.SourceRef, id.String(), reason, nil)
			if perr != nil {
				return perr
			}
			pending = &pr
			return nil
		}
		if err := repo.Delete(r.Context(), id); err != nil {
			return err
		}
		return auditBinding(r.Context(), sc, mc, "delete", snap)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if pending != nil {
		writeJSON(w, http.StatusAccepted, *pending) // relaxation awaits a second, distinct approver
		return
	}
	writeJSON(w, http.StatusNoContent, nil)
}

// auditBinding appends a binding-governance audit event attributed to the real
// principal, in the caller's transaction — the ledger records WHO bound which source
// to which scope (docs/SECURITY-HARDENING.md self-audit), never a credential value.
func auditBinding(ctx context.Context, sc store.Scope, mc api.ModuleContext, verb string, b bindingDTO) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor:      mc.Principal.Actor(),
		ActorKind:  mc.Principal.ActorKind(),
		Action:     "sourcescope.binding." + verb,
		TargetKind: bindingKind,
		Meta: map[string]any{
			"source_type": b.SourceType, "source_ref": b.SourceRef,
			"scope_tree": b.ScopeTree, "scope_ref": b.ScopeRef,
			"effect":          normalizeEffect(b.Effect), // Posture delta (allow|forbid)
			"folder_path":     b.FolderPath,              // advisory; "" for non-folder bindings
			"has_scoped_cred": b.CredName != "",
		},
	})
	return err
}
