// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Template permissions.
const (
	permTemplateRead  auth.Permission = "sessions:template:read"
	permTemplateWrite auth.Permission = "sessions:template:write"
	permTemplateAdmin auth.Permission = "sessions:template:admin"
)

func templatePermissions() []auth.Permission {
	return []auth.Permission{permTemplateRead, permTemplateWrite, permTemplateAdmin}
}

// templateDTO is the API view of a workspace template.
type templateDTO struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Version     int64   `json:"version"`
	Author      string  `json:"author"`
	Builtin     bool    `json:"builtin"`
	ArchivedAt  string  `json:"archived_at,omitempty"`
	Body        tplBody `json:"body"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// tplBody is the JSON body of a workspace template.
type tplBody struct {
	Hooks      *tplHooks    `json:"hooks,omitempty"`
	Settings   *tplSettings `json:"settings,omitempty"`
	Connectors []string     `json:"connectors,omitempty"`
	Policies   *tplPolicies `json:"policies,omitempty"`
}

type tplHooks struct {
	PreTool     []tplHookEntry `json:"pre_tool,omitempty"`
	PostTool    []tplHookEntry `json:"post_tool,omitempty"`
	PreSession  []tplHookEntry `json:"pre_session,omitempty"`
	PostSession []tplHookEntry `json:"post_session,omitempty"`
}

type tplHookEntry struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type tplSettings struct {
	PermissionMode     string `json:"permission_mode,omitempty"`
	Effort             string `json:"effort,omitempty"`
	Model              string `json:"model,omitempty"`
	CustomInstructions string `json:"custom_instructions,omitempty"`
}

type tplPolicies struct {
	DLPMode                   string   `json:"dlp_mode,omitempty"`
	MaxSessionDurationMinutes int      `json:"max_session_duration_minutes,omitempty"`
	AllowedTools              []string `json:"allowed_tools,omitempty"`
	RecordIO                  *bool    `json:"record_io,omitempty"`
}

func toTemplateDTO(rec model.Record) templateDTO {
	dto := templateDTO{
		ID:          rec.String(model.ColID),
		Name:        rec.String(colTplName),
		Description: rec.String(colTplDescription),
		Version:     rec.Int(model.ColVersion),
		Author:      rec.String(colTplAuthor),
		Builtin:     rec.Bool(colTplBuiltin),
		ArchivedAt:  rec.String(colTplArchivedAt),
		CreatedAt:   rec.String(model.ColCreatedAt),
		UpdatedAt:   rec.String(model.ColUpdatedAt),
	}
	raw := rec.String(colTplBody)
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &dto.Body)
	}
	return dto
}

// --- Handlers ---

func (m *Module) handleListTemplates(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// Lazily seed built-in templates for this tenant on first access.
	m.ensureBuiltins(r.Context(), mc.Tenant)

	q := listQuery(r)
	builtinFilter := r.URL.Query().Get("builtin")
	includeArchived := r.URL.Query().Get("include_archived") == "true"

	out := listResponse[templateDTO]{Items: []templateDTO{}}
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		if builtinFilter != "" {
			q.Filters = append(q.Filters, model.Filter{
				Column: colTplBuiltin,
				Op:     model.OpEq,
				Value:  builtinFilter == "true",
			})
		}
		recs, page, rerr := repo.List(r.Context(), q)
		if rerr != nil {
			return rerr
		}
		for _, rec := range recs {
			dto := toTemplateDTO(rec)
			// Filter out archived templates in-memory (no OpIsNull in the store).
			if !includeArchived && dto.ArchivedAt != "" {
				continue
			}
			out.Items = append(out.Items, dto)
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

func (m *Module) handleGetTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var dto templateDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(r.Context(), id)
		if rerr != nil {
			return rerr
		}
		dto = toTemplateDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

type createTemplateRequest struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Body        tplBody `json:"body"`
}

func (m *Module) handleCreateTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// STRICT: an unknown key is rejected, not dropped. The body is decoded into a typed
	// struct and marshaled straight back to storage, so a misspelled policy key —
	// "allowed_tool", "record_i0" — used to disappear on the way in and leave a template
	// that looked authored and governed nothing. That is the same defect as the rest of
	// this pack, entering one level earlier (Codex sol max contrast, 2026-08-11).
	var req createTemplateRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}
	bodyJSON, _ := json.Marshal(req.Body)

	var dto templateDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Create(r.Context(), model.Record{
			colTplName:        req.Name,
			colTplDescription: req.Description,
			colTplAuthor:      mc.Principal.Actor(),
			colTplBuiltin:     false,
			colTplBody:        string(bodyJSON),
		})
		if rerr != nil {
			return rerr
		}
		dto = toTemplateDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

type updateTemplateRequest struct {
	Name        *string  `json:"name,omitempty"`
	Description *string  `json:"description,omitempty"`
	Body        *tplBody `json:"body,omitempty"`
}

func (m *Module) handleUpdateTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req updateTemplateRequest // strict, for the reason in handleCreateTemplate
	if !decodeJSONBody(w, r, &req) {
		return
	}

	var dto templateDTO
	var builtin bool
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(r.Context(), id)
		if rerr != nil {
			return rerr
		}
		if rec.Bool(colTplBuiltin) {
			builtin = true
			return nil
		}
		if req.Name != nil {
			rec[colTplName] = *req.Name
		}
		if req.Description != nil {
			rec[colTplDescription] = *req.Description
		}
		if req.Body != nil {
			bodyJSON, _ := json.Marshal(*req.Body)
			rec[colTplBody] = string(bodyJSON)
		}
		rec, rerr = repo.Update(r.Context(), rec)
		if rerr != nil {
			return rerr
		}
		dto = toTemplateDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if builtin {
		writeJSON(w, http.StatusForbidden, errorBody("built-in templates cannot be modified"))
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (m *Module) handleDeleteTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var builtin bool
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(r.Context(), id)
		if rerr != nil {
			return rerr
		}
		if rec.Bool(colTplBuiltin) {
			builtin = true
			return nil
		}
		now := m.clock.Now().String()
		rec[colTplArchivedAt] = now
		_, rerr = repo.Update(r.Context(), rec)
		return rerr
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if builtin {
		writeJSON(w, http.StatusForbidden, errorBody("built-in templates cannot be archived"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleDuplicateTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var nameReq struct {
		Name string `json:"name"`
	}
	if !decodeJSONBody(w, r, &nameReq) {
		return
	}
	if nameReq.Name == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("name is required"))
		return
	}

	var dto templateDTO
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		src, rerr := repo.Get(r.Context(), id)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Create(r.Context(), model.Record{
			colTplName:        nameReq.Name,
			colTplDescription: src.String(colTplDescription),
			colTplAuthor:      mc.Principal.Actor(),
			colTplBuiltin:     false,
			colTplBody:        src.String(colTplBody),
		})
		if rerr != nil {
			return rerr
		}
		dto = toTemplateDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, dto)
}

// --- Merge / Apply ---

type mergeConflict struct {
	Field    string `json:"field"`
	OldValue any    `json:"old_value"`
	NewValue any    `json:"new_value"`
}

// applyTarget is the launch configuration a template is merged ONTO — the same fields
// POST /runs takes, so the preview and the launch agree by construction rather than by
// two implementations happening to match. Every field is optional: an omitted one means
// "not chosen", which is what makes a conflict a real contradiction and not an artifact
// of a default.
type applyTarget struct {
	// Transport is the launch transport the preview is asked about. It matters because
	// some terms are only unenforceable on a particular transport (a mandated recording
	// on remote-control has no bridged I/O to anchor).
	Transport          string   `json:"transport,omitempty"`
	PermissionMode     string   `json:"permission_mode,omitempty"`
	Effort             string   `json:"effort,omitempty"`
	Model              string   `json:"model,omitempty"`
	AllowedTools       []string `json:"allowed_tools,omitempty"`
	Instructions       string   `json:"custom_instructions,omitempty"`
	RecordIO           bool     `json:"record_io,omitempty"`
	MaxDurationMinutes int64    `json:"max_session_duration_minutes,omitempty"`
	WorkspaceRef       string   `json:"workspace_ref,omitempty"`
}

// applyTemplateRequest is the OPTIONAL POST /templates/{id}/apply body. An absent body
// previews the template against an empty configuration, which is what a caller asking
// "what does this template impose?" wants and what every pre client sends.
type applyTemplateRequest struct {
	Target applyTarget `json:"target"`
}

// applyResponse is the merge result.
//
// ⛔ `applied` is a FACT and no longer a constant. It reports whether the template's
// terms COULD be imposed on the target — false when the template declares something
// this runtime cannot enforce, and `unenforceable` then says which field and why. It
// has never meant "a running session was changed" and must not be made to: this
// endpoint mutates nothing, and the way a template GOVERNS a session is to be named at
// launch (POST /runs template_id), where the server merges it before the gates and
// writes it into the child's argv.
type applyResponse struct {
	Applied   bool            `json:"applied"`
	Conflicts []mergeConflict `json:"conflicts"`
	Template  templateDTO     `json:"template"`
	// Merged is the configuration a launch would run under. Absent when applied is false.
	Merged *applyTarget `json:"merged,omitempty"`
	// Unenforceable names every declared term this runtime cannot keep. Non-empty ⇒
	// applied is false ⇒ a launch naming this template is refused with the same list.
	Unenforceable []string `json:"unenforceable,omitempty"`
}

func (m *Module) handleApplyTemplate(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := model.ID(chi.URLParam(r, "id"))
	if id.IsZero() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var req applyTemplateRequest
	if !decodeOptionalJSONBody(w, r, &req) {
		return
	}

	var dto templateDTO
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, rerr := sc.Ext(templateKind)
		if rerr != nil {
			return rerr
		}
		rec, rerr := repo.Get(r.Context(), id)
		if rerr != nil {
			return rerr
		}
		dto = toTemplateDTO(rec)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// The SAME reduction and the SAME merge the launch runs (templateapply.go). A second
	// implementation here is how the preview and the launch would drift into disagreeing,
	// which is a subtler version of the defect this endpoint used to have.
	//
	// ⛔ AND THE SAME REFUSALS, which is the half this handler was missing: it used to
	// answer applied:true for an ARCHIVED template and for one whose terms the launch
	// refuses on the chosen transport, so a preview could promise a configuration the very
	// next call rejected. A preview that disagrees with the thing it previews is worse than
	// none (Codex sol max contrast, 2026-08-11).
	terms := templateTerms(dto.Body)
	if dto.ArchivedAt != "" {
		terms.unenforceable = append(terms.unenforceable,
			"this template is archived and cannot govern a launch")
	}
	terms.unenforceable = append(terms.unenforceable,
		unenforceableForTransport(terms, Transport(strings.TrimSpace(req.Target.Transport)))...)
	if len(terms.unenforceable) > 0 {
		writeJSON(w, http.StatusOK, applyResponse{
			Applied:       false,
			Conflicts:     []mergeConflict{},
			Template:      dto,
			Unenforceable: terms.unenforceable,
		})
		return
	}
	p := req.Target.toParams()
	conflicts := terms.applyTo(&p)
	if conflicts == nil {
		conflicts = []mergeConflict{}
	}
	merged := targetOf(p)
	writeJSON(w, http.StatusOK, applyResponse{
		Applied:   true,
		Conflicts: conflicts,
		Template:  dto,
		Merged:    &merged,
	})
}

// toParams projects the request target onto the launch parameters the merge operates
// on, so the preview runs the launch's own code path.
func (t applyTarget) toParams() CreateRunParams {
	return CreateRunParams{
		PermissionMode:  t.PermissionMode,
		Effort:          t.Effort,
		Model:           t.Model,
		AllowedTools:    t.AllowedTools,
		Instructions:    t.Instructions,
		RecordRequested: t.RecordIO,
		MaxDuration:     time.Duration(t.MaxDurationMinutes) * time.Minute,
		WorkspaceRef:    t.WorkspaceRef,
	}
}

// targetOf projects merged launch parameters back into the API shape.
func targetOf(p CreateRunParams) applyTarget {
	return applyTarget{
		Transport:          string(p.Transport),
		PermissionMode:     p.PermissionMode,
		Effort:             p.Effort,
		Model:              p.Model,
		AllowedTools:       p.AllowedTools,
		Instructions:       p.Instructions,
		RecordIO:           p.RecordRequested,
		MaxDurationMinutes: int64(p.MaxDuration / time.Minute),
		WorkspaceRef:       p.WorkspaceRef,
	}
}

// --- Built-in template seeds ---

// ⛔ A SEEDED TEMPLATE IS A PROMISE MADE IN EVERY TENANT — it is written into all of
// them on first access (ensureBuiltins) and its description is rendered as a security
// posture. So every term below has to be one the launch can actually keep; the merge
// (templateapply.go) refuses a launch whose template it cannot honor, and a built-in
// that refuses is a defect we shipped, not a control working.
//
// Two of them were exactly that until: "Secure Development" declared
// dlp_mode:"classify" and "Security Audit" dlp_mode:"block", and NEITHER IS A VALUE
// THIS PRODUCT HAS EVER HAD — the DLP vocabulary is off|label|deny (workspace_schema.go,
// validated at workspace.go:551). They read as strict postures and named nothing. They
// are corrected here to the product's own words, which is the same promise stated in a
// language the engine speaks — not a smaller one.
//
// The permission mode is likewise stated rather than implied wherever a template
// carries a tool allowlist: dontAsk is the only mode under which an allowlist confines
// anything (see permModeDontAsk), so a template that declares one and stays silent
// about the mode is relying on a derivation the operator reading it cannot see.
var builtinTemplates = []struct {
	name string
	desc string
	body tplBody
}{
	{
		name: "Secure Development",
		desc: "DLP content classification, restricted tool set, mandatory I/O recording.",
		body: tplBody{
			Policies: &tplPolicies{
				DLPMode:      dlpLabel, // "content classification" in the workspace's own vocabulary
				AllowedTools: []string{"Read", "Edit", "Write", "Bash"},
				RecordIO:     boolPtr(true),
			},
			Settings: &tplSettings{Effort: "high", PermissionMode: permModeDontAsk},
		},
	},
	{
		name: "Code Review",
		desc: "Read-only analysis mode — no file writes, no destructive operations.",
		body: tplBody{
			Policies: &tplPolicies{
				AllowedTools: []string{"Read", "Bash"},
			},
			Settings: &tplSettings{Effort: "high", PermissionMode: permModeDontAsk},
		},
	},
	{
		name: "Documentation",
		desc: "Unrestricted tool access for documentation tasks.",
		body: tplBody{
			Settings: &tplSettings{
				CustomInstructions: "Focus on writing clear, concise documentation. Follow the project's existing documentation style.",
			},
		},
	},
	{
		name: "Refactoring",
		desc: "Test-first discipline with pre-tool verification hooks.",
		body: tplBody{
			// ⚠ Hooks are its ONLY term, and the launch cannot provision them — see the
			// note on "Security Audit". Naming this template at launch is refused.
			Hooks: &tplHooks{
				PreTool: []tplHookEntry{
					{Command: "test -f *_test.go || echo 'WARNING: No test files found'", TimeoutMs: 5000},
				},
			},
			Settings: &tplSettings{Effort: "high"},
		},
	},
	{
		name: "Security Audit",
		desc: "Vulnerability scanning with strict DLP and read-only access.",
		body: tplBody{
			Policies: &tplPolicies{
				DLPMode:      dlpDeny, // "strict DLP" in the workspace's own vocabulary
				AllowedTools: []string{"Read", "Bash"},
				RecordIO:     boolPtr(true),
			},
			// ⚠ DECLARED AND NOT YET ENFORCEABLE, and left standing on purpose. The launch
			// does not provision hooks into the child (templateTerms says why), so naming
			// this template at launch is REFUSED with that reason rather than started
			// without its pre-session hook. Deleting the hook would make the template
			// launchable by promising less, which is the move this whole pack exists to
			// stop; the fix is hook provisioning, and until it lands the refusal is the
			// honest state. Same for "Refactoring" below.
			Hooks: &tplHooks{
				PreSession: []tplHookEntry{
					{Command: "echo 'Security audit session — all actions recorded'"},
				},
			},
		},
	},
	{
		name: "Onboarding / Exploration",
		desc: "Safe learning mode — read-only, no destructive operations, low effort.",
		body: tplBody{
			Policies: &tplPolicies{
				AllowedTools: []string{"Read", "Bash"},
			},
			Settings: &tplSettings{Effort: "low", PermissionMode: permModeDontAsk},
		},
	},
	{
		name: "CI/CD Pipeline",
		desc: "Headless automation — no interactive tools, strict timeout, structured output.",
		body: tplBody{
			Policies: &tplPolicies{
				MaxSessionDurationMinutes: 60,
				AllowedTools:              []string{"Bash", "Read"},
			},
			// Was "default", which CONTRADICTED the allowlist beside it and would now refuse
			// its own launch. dontAsk is what the description already promised — "no
			// interactive tools" is precisely the mode that never prompts.
			Settings: &tplSettings{PermissionMode: permModeDontAsk},
		},
	},
	{
		name: "Incident Response",
		desc: "Emergency debugging — full access, mandatory recording, high effort.",
		body: tplBody{
			Policies: &tplPolicies{
				RecordIO: boolPtr(true),
			},
			Settings: &tplSettings{Effort: "max"},
		},
	},
}

func boolPtr(v bool) *bool { return &v }

// seededTenants tracks which tenants have had built-in templates seeded, so the
// check runs at most once per process lifetime per tenant.
var seededTenants sync.Map

// ensureBuiltins lazily seeds built-in templates for the given tenant on first
// access. It is safe to call concurrently.
func (m *Module) ensureBuiltins(ctx context.Context, tenant model.TenantID) {
	if m.data == nil {
		return
	}
	if _, loaded := seededTenants.LoadOrStore(tenant, struct{}{}); loaded {
		return // already seeded this process lifetime
	}
	if err := m.seedBuiltins(ctx, tenant); err != nil {
		seededTenants.Delete(tenant) // allow retry on next request
	}
}

// seedBuiltins idempotently creates or updates built-in templates. It runs at
// most once per tenant per process boot (guarded by ensureBuiltins).
func (m *Module) seedBuiltins(ctx context.Context, tenant model.TenantID) error {
	return m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(templateKind)
		if err != nil {
			return err
		}
		for _, bt := range builtinTemplates {
			bodyJSON, _ := json.Marshal(bt.body)
			existing, _, ferr := repo.List(ctx, model.Query{
				Filters: []model.Filter{eq(colTplName, bt.name)},
				Limit:   1,
			})
			if ferr != nil {
				return ferr
			}
			if len(existing) > 0 {
				if !existing[0].Bool(colTplBuiltin) {
					continue // custom template with same name exists — don't overwrite
				}
				rec := existing[0]
				rec[colTplDescription] = bt.desc
				rec[colTplBody] = string(bodyJSON)
				if _, uerr := repo.Update(ctx, rec); uerr != nil {
					return uerr
				}
				continue
			}
			if _, cerr := repo.Create(ctx, model.Record{
				colTplName:        bt.name,
				colTplDescription: bt.desc,
				colTplAuthor:      "system",
				colTplBuiltin:     true,
				colTplBody:        string(bodyJSON),
			}); cerr != nil {
				return cerr
			}
		}
		return nil
	})
}

// templateRoutes mounts the template CRUD endpoints under /v1/m/sessions/.
func (m *Module) templateRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/templates", permTemplateRead, m.handleListTemplates)
	reg.Handle("GET", "/templates/{id}", permTemplateRead, m.handleGetTemplate)
	reg.Handle("POST", "/templates", permTemplateWrite, m.handleCreateTemplate)
	reg.Handle("PUT", "/templates/{id}", permTemplateWrite, m.handleUpdateTemplate)
	reg.Handle("DELETE", "/templates/{id}", permTemplateAdmin, m.handleDeleteTemplate)
	reg.Handle("POST", "/templates/{id}/duplicate", permTemplateWrite, m.handleDuplicateTemplate)
	reg.Handle("POST", "/templates/{id}/apply", permTemplateRead, m.handleApplyTemplate)
}
