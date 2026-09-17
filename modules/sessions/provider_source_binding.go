// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// B1 — source → profile bindings.
//
// A binding says that ONE configured source, at ONE successfully applied
// revision, on ONE execution environment, is dedicated to ONE profile. It is what
// lets an observation that arrived through that source be ATTRIBUTED to the
// profile — and nothing more: a binding never says the external id inside the
// observation belongs to a process the plane launched. Rows are immutable except
// for revocation; another revision or another profile is another row, and the old
// row keeps explaining old events. The row identity is the SourceDef's persistent
// ID and its applied version, never its editable name: delete-and-recreate under
// the same name is another ID and inherits nothing.

const (
	providerBindingKind  model.Kind = "sessions.provider_source_binding"
	providerBindingTable            = "sessions_provider_source_binding"
)

// sessions.provider_source_binding columns.
const (
	colPBRef        = "binding_ref"
	colPBSourceID   = "source_id"
	colPBSourceRev  = "source_revision"
	colPBSourceName = "source_name" // informational snapshot of the roster name at bind time
	colPBEnvRef     = "environment_ref"
	colPBSelector   = "selector_key"
	colPBProfileID  = "provider_profile_id"
	colPBDriver     = "driver"
	colPBState      = "state"
	colPBBoundAt    = "bound_at"
	colPBRevokedAt  = "revoked_at"
)

// Binding states.
const (
	BindingActive  = "active"
	BindingRevoked = "revoked"
)

// SelectorDedicated is the only channel discriminator B1 supports: the whole
// source is dedicated to the profile. A multiplexing source would need a
// server-established discriminator; a label in the payload is not one, so such
// sources stay unattributed rather than bound to an arbitrary profile.
const SelectorDedicated = "dedicated"

const bindingRefPrefix = "psb_"

// Binding-plane errors.
var (
	ErrBindingNotFound      = &runErr{http.StatusNotFound, "provider source binding not found"}
	ErrBindingExists        = &runErr{http.StatusConflict, "this source revision is already bound on this environment"}
	ErrBindingRevoked       = &runErr{http.StatusConflict, "provider source binding is revoked"}
	ErrSourceRevisionStale  = &runErr{http.StatusConflict, "the requested source revision is not the revision this environment has applied"}
	ErrSourceTenantMismatch = &runErr{http.StatusUnprocessableEntity, "the source stamps observations for another tenant"}
	// ErrNoSourceResolver is the deny-closed answer when composition wired no
	// source port: nothing here can validate a roster row, so nothing binds.
	ErrNoSourceResolver = &runErr{http.StatusServiceUnavailable, "source registry port is not wired; bindings are deny-closed"}
	// ErrSourceNotFound is what the composition port returns for an unknown or
	// not-applied source id.
	ErrSourceNotFound = &runErr{http.StatusNotFound, "source not found or not applied on this execution environment"}
	// ErrSourceForbidden is what the port returns when the actor may not
	// administer the source (global sources need the deployment-wide admin).
	ErrSourceForbidden = &runErr{http.StatusForbidden, "administering this source requires the global source administration permission"}
)

// SourceRevision is the narrow composition-port view of one durable roster row
// AS APPLIED by this node's runtime: the persistent ID, the version the runtime
// successfully opened, the operational name, the tenant it stamps, its kind and
// the environment that applied it. The module never receives the roster store,
// the auth scope or the Config map.
type SourceRevision struct {
	ID             model.ID
	Version        int64
	Name           string
	Tenant         string
	Kind           string
	EnvironmentRef string
}

// ProviderSourceResolver is the composition seam behind bindings. It resolves a
// source by persistent ID for an ACTOR: the root checks that the actor may
// administer the source (for the global roster, the deployment-wide admin
// permission the console's source CRUD already requires) and that this node has
// applied it, and returns the applied revision. Any refusal is deny-closed.
type ProviderSourceResolver interface {
	ResolveAppliedSource(ctx context.Context, p auth.Principal, tenant model.TenantID, sourceID model.ID) (SourceRevision, error)
}

// UseProviderSourceResolver late-binds the source port during boot.
func (m *Module) UseProviderSourceResolver(r ProviderSourceResolver) {
	if r != nil {
		m.providerSources = r
	}
}

// ProviderSourceBinding is the module's own view of one binding row.
type ProviderSourceBinding struct {
	ID             model.ID
	Ref            string
	SourceID       model.ID
	SourceRevision int64
	SourceName     string
	EnvironmentRef string
	SelectorKey    string
	ProfileRef     string
	Driver         string
	State          string
	BoundAt        string
	RevokedAt      string
}

// CreateBindingInput is the validated create request: the source's persistent
// id, the EXACT revision the caller means, and the profile.
type CreateBindingInput struct {
	SourceID       model.ID
	SourceRevision int64
	ProfileRef     string
}

func (m *Module) registerProviderBindingSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  providerBindingKind,
		Table: providerBindingTable,
		Fields: []model.FieldSpec{
			{Name: colPBRef, Kind: model.KindText},
			{Name: colPBSourceID, Kind: model.KindUUID, Indexed: true},
			{Name: colPBSourceRev, Kind: model.KindInt},
			{Name: colPBSourceName, Kind: model.KindText},
			{Name: colPBEnvRef, Kind: model.KindText},
			{Name: colPBSelector, Kind: model.KindText},
			{Name: colPBProfileID, Kind: model.KindText, Indexed: true},
			{Name: colPBDriver, Kind: model.KindText},
			{Name: colPBState, Kind: model.KindText},
			{Name: colPBBoundAt, Kind: model.KindTimestamp},
			{Name: colPBRevokedAt, Kind: model.KindTimestamp, Nullable: true},
		},
		Indexes: []model.IndexSpec{
			{Name: "sessions_provider_binding_ref_uniq", Columns: []string{model.ColTenantID, colPBRef}, Unique: true},
			// One dedicated binding per (source id, applied revision, environment). No
			// component of the key is nullable, so the index is total.
			{Name: "sessions_provider_binding_source_uniq", Columns: []string{model.ColTenantID, colPBSourceID, colPBSourceRev, colPBEnvRef, colPBSelector}, Unique: true},
		},
	})
}

func bindingFromRecord(rec model.Record) ProviderSourceBinding {
	return ProviderSourceBinding{
		ID:             model.ID(rec.String(model.ColID)),
		Ref:            rec.String(colPBRef),
		SourceID:       model.ID(rec.String(colPBSourceID)),
		SourceRevision: rec.Int(colPBSourceRev),
		SourceName:     rec.String(colPBSourceName),
		EnvironmentRef: rec.String(colPBEnvRef),
		SelectorKey:    rec.String(colPBSelector),
		ProfileRef:     rec.String(colPBProfileID),
		Driver:         rec.String(colPBDriver),
		State:          rec.String(colPBState),
		BoundAt:        rec.String(colPBBoundAt),
		RevokedAt:      rec.String(colPBRevokedAt),
	}
}

func validBindingRef(ref string) bool {
	if !strings.HasPrefix(ref, bindingRefPrefix) || len(ref) > 128 {
		return false
	}
	id, err := model.ParseID(strings.TrimPrefix(ref, bindingRefPrefix))
	return err == nil && !id.IsZero()
}

// CreateBinding dedicates a source revision to a profile. The order of checks is
// the order of authority: the composition port decides whether the actor may
// administer the source and what revision this node applied; the module decides
// whether the profile can take a binding; the database decides that the (source,
// revision, environment) key is free.
func (m *Module) CreateBinding(ctx context.Context, tenant model.TenantID, p auth.Principal, in CreateBindingInput) (ProviderSourceBinding, error) {
	if m.data == nil {
		return ProviderSourceBinding{}, errNoData
	}
	if m.providerSources == nil {
		return ProviderSourceBinding{}, ErrNoSourceResolver
	}
	env, err := m.localEnvironment()
	if err != nil {
		return ProviderSourceBinding{}, err
	}
	if in.SourceID.IsZero() {
		return ProviderSourceBinding{}, badRequest("source_id is required")
	}
	if in.SourceRevision < 1 {
		return ProviderSourceBinding{}, badRequest("source_revision must name the exact applied revision")
	}
	prof, err := m.GetProfile(ctx, tenant, in.ProfileRef)
	if err != nil {
		return ProviderSourceBinding{}, err
	}
	switch prof.State {
	case ProfileActive:
	case ProfileDisabled:
		return ProviderSourceBinding{}, ErrProfileDisabled
	default:
		return ProviderSourceBinding{}, ErrProfileRetired
	}
	if prof.EnvironmentRef != env {
		return ProviderSourceBinding{}, ErrProfileForeignEnvironment
	}
	src, err := m.providerSources.ResolveAppliedSource(ctx, p, tenant, in.SourceID)
	if err != nil {
		return ProviderSourceBinding{}, err
	}
	if src.ID != in.SourceID || src.Version < 1 {
		return ProviderSourceBinding{}, ErrSourceNotFound
	}
	if src.Version != in.SourceRevision {
		return ProviderSourceBinding{}, ErrSourceRevisionStale
	}
	if src.Tenant != tenant.String() {
		return ProviderSourceBinding{}, ErrSourceTenantMismatch
	}
	if src.EnvironmentRef != env {
		return ProviderSourceBinding{}, ErrProfileForeignEnvironment
	}
	ref := bindingRefPrefix + string(model.NewID())
	var out ProviderSourceBinding
	err = m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerBindingKind)
		if err != nil {
			return err
		}
		created, err := repo.Create(ctx, model.Record{
			colPBRef:        ref,
			colPBSourceID:   src.ID.String(),
			colPBSourceRev:  src.Version,
			colPBSourceName: src.Name,
			colPBEnvRef:     env,
			colPBSelector:   SelectorDedicated,
			colPBProfileID:  prof.Ref,
			colPBDriver:     prof.Driver,
			colPBState:      BindingActive,
			colPBBoundAt:    model.NewTimestamp(m.now()).String(),
		})
		if err != nil {
			return err
		}
		out = bindingFromRecord(created)
		return nil
	})
	if errors.Is(err, store.ErrConflict) {
		return ProviderSourceBinding{}, ErrBindingExists
	}
	if err != nil {
		return ProviderSourceBinding{}, err
	}
	return out, nil
}

// RevokeBinding ends a binding. It reassigns nothing: events attributed under it
// keep their provenance, and later observations through that source are simply
// no longer attributed to the profile.
func (m *Module) RevokeBinding(ctx context.Context, tenant model.TenantID, ref string) (ProviderSourceBinding, error) {
	if m.data == nil {
		return ProviderSourceBinding{}, errNoData
	}
	if !validBindingRef(ref) {
		return ProviderSourceBinding{}, ErrBindingNotFound
	}
	var out ProviderSourceBinding
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerBindingKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colPBRef, ref)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return ErrBindingNotFound
		}
		rec := recs[0]
		if rec.String(colPBState) == BindingRevoked {
			out = bindingFromRecord(rec)
			return nil // idempotent
		}
		rec[colPBState] = BindingRevoked
		rec[colPBRevokedAt] = model.NewTimestamp(m.now()).String()
		updated, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		out = bindingFromRecord(updated)
		return nil
	})
	return out, err
}

// GetBinding reads one binding by ref.
func (m *Module) GetBinding(ctx context.Context, tenant model.TenantID, ref string) (ProviderSourceBinding, error) {
	if m.data == nil {
		return ProviderSourceBinding{}, errNoData
	}
	if !validBindingRef(ref) {
		return ProviderSourceBinding{}, ErrBindingNotFound
	}
	var out ProviderSourceBinding
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerBindingKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colPBRef, ref)}, Limit: 1})
		if err != nil {
			return err
		}
		if len(recs) == 0 {
			return ErrBindingNotFound
		}
		out = bindingFromRecord(recs[0])
		return nil
	})
	return out, err
}

// ListBindings lists the tenant's bindings, optionally narrowed to a profile.
func (m *Module) ListBindings(ctx context.Context, tenant model.TenantID, profileRef string, q model.Query) ([]ProviderSourceBinding, model.Page, error) {
	if m.data == nil {
		return nil, model.Page{}, errNoData
	}
	if profileRef != "" {
		q.Filters = append(q.Filters, eq(colPBProfileID, profileRef))
	}
	var out []ProviderSourceBinding
	var page model.Page
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(providerBindingKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(ctx, q)
		if err != nil {
			return err
		}
		out = make([]ProviderSourceBinding, 0, len(recs))
		for _, rec := range recs {
			out = append(out, bindingFromRecord(rec))
		}
		page = p
		return nil
	})
	return out, page, err
}

// findActiveBinding resolves the profile a stamped source registration is
// dedicated to, by the exact (source id, applied revision, environment) the host
// stamped on the event envelope — never by the source's current name and never
// by a label the payload carried. No active row means: known registration, no
// verifiable profile.
func findActiveBinding(ctx context.Context, sc store.Scope, sourceID string, revision int64, envRef string) (ProviderSourceBinding, bool, error) {
	repo, err := sc.Ext(providerBindingKind)
	if err != nil {
		return ProviderSourceBinding{}, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{
		eq(colPBSourceID, sourceID),
		{Column: colPBSourceRev, Op: model.OpEq, Value: revision},
		eq(colPBEnvRef, envRef),
		eq(colPBSelector, SelectorDedicated),
	}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return ProviderSourceBinding{}, false, err
	}
	b := bindingFromRecord(recs[0])
	if b.State != BindingActive {
		return b, false, nil
	}
	return b, true, nil
}
