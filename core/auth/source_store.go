// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
)

// The DURABLE SOURCE ROSTER: the store-backed authoring surface for an
// operator's observation-source connectors. It is the successor to a `sources[]`
// entry in the boot config file — persisted so the roster survives a restart and
// can be reconciled into the running engine WITHOUT one. The CRUD here mirrors the
// SecretStore / FederationService sealed-CRUD pattern (same package
// helpers: auditAct, byEq, drainList) and lives in the system tenant, reachable
// ONLY through the auth partition (store.AuthScope.Sources) — so a module can
// never read or edit the source roster.
//
// Unlike the secret store, a source row carries NO secret value: Config holds
// connector settings and secret REFERENCES (`store:<name>`, `vault:…`), and the
// engine resolves each reference to its sealed value at Open. So the SourceStore
// needs no Sealer; persisting only references is what keeps the roster
// non-secret-bearing.

// GlobalSourceScope is the deployment-wide source-roster scope — the only scope
// Reconciles. The column is present from day one so a per-tenant roster is
// an additive row, never a schema change (the SecretEntry precedent).
var GlobalSourceScope = model.SystemTenantID

// sourceDefKind is the audit target kind for source-roster mutations.
const sourceDefKind model.Kind = "core.source_def"

// maxSourceNameLen bounds a source name (a non-secret operator handle).
const maxSourceNameLen = 256

var (
	// ErrSourceDefNotFound means no source row exists for the (scope, name).
	ErrSourceDefNotFound = errors.New("auth: no such source")
	// ErrBadSourceName is returned for an empty or malformed source name.
	ErrBadSourceName = errors.New("auth: invalid source name")
	// ErrBadSourceDef is returned when a source definition is incomplete or
	// contradictory (no tenant, neither kind nor plugin, etc.).
	ErrBadSourceDef = errors.New("auth: invalid source definition")
)

// SourceStore owns the durable source roster: CRUD over the auth partition. It
// does NOT build or run connectors — that is the composition root's live
// reconciler (cmd/olivares); this is only the persisted source of truth.
type SourceStore struct {
	st store.Store
}

// NewSourceStore builds the service.
func NewSourceStore(st store.Store) *SourceStore { return &SourceStore{st: st} }

// ValidateSourceName reports a non-empty message when name is not a valid source
// handle: printable, bounded, free of whitespace and of the ':' that would split
// a secret reference. Same shape as a secret name.
func ValidateSourceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "source name is required"
	}
	if len(name) > maxSourceNameLen {
		return "source name too long"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/':
		default:
			return "source name may use only letters, digits and . _ - /"
		}
	}
	return ""
}

// ValidateSourceDef reports why Put would REFUSE def, or nil when the definition
// is internally consistent. It normalizes exactly as Put does (trim the name,
// default the scope) so its verdict is the write's verdict and not an
// approximation of it.
//
// It is exported for the PREVIEW verbs (`olivares sources plan` /
// `validate`), which exist to tell an operator what a write would do before the
// write happens. A preview that reimplemented these rules could pass what the
// write refuses — the one failure mode that makes a preview worse than none —
// so there is one implementation and both callers reach it.
func ValidateSourceDef(def model.SourceDef) error {
	def.Name = strings.TrimSpace(def.Name)
	if def.Scope.IsZero() {
		def.Scope = GlobalSourceScope
	}
	return validateDef(def)
}

// validateDef checks a definition is internally consistent before it is
// persisted and applies the descriptor-independent credential guard. The richer
// checks (a known kind, every descriptor-declared secret field, an admissible
// plugin attestation) also run in the reconciler, where the connector and trust
// policy are available; a row that passes here but fails there is reported as
// rejected, never silently run.
func validateDef(def model.SourceDef) error {
	if msg := ValidateSourceName(def.Name); msg != "" {
		return fmt.Errorf("%w: %s", ErrBadSourceName, msg)
	}
	if strings.TrimSpace(def.Tenant) == "" {
		return fmt.Errorf("%w: a source must name the business tenant its observations belong to", ErrBadSourceDef)
	}
	if def.PollSeconds < 0 {
		return fmt.Errorf("%w: poll_seconds cannot be negative", ErrBadSourceDef)
	}
	hasKind := strings.TrimSpace(def.Kind) != ""
	hasPlugin := def.Plugin != nil
	if hasKind == hasPlugin {
		return fmt.Errorf("%w: a source is either a first-party kind OR an external plugin, exactly one", ErrBadSourceDef)
	}
	if hasPlugin && strings.TrimSpace(def.Plugin.Path) == "" {
		return fmt.Errorf("%w: an external plugin source needs a binary path", ErrBadSourceDef)
	}
	if err := validateReferenceOnlyCredentials(def.Config); err != nil {
		return err
	}
	return nil
}

// validateReferenceOnlyCredentials is the storage-boundary guard for connector
// configuration. Descriptor-aware callers reject every field a known connector
// declares Secret; this lower seam also protects direct SourceStore callers,
// bootstrap imports and descriptorless plugins by refusing literal values under
// credential-bearing config names. Only the existing secret-reference grammar is
// accepted. The error names the field but never the attempted value.
func validateReferenceOnlyCredentials(config map[string]string) error {
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := strings.TrimSpace(config[key])
		if value == "" {
			continue
		}
		if secret.ContainsInlineCredential(value) {
			return fmt.Errorf("%w: config field %q embeds credential material and must contain only non-secret settings", ErrBadSourceDef, key)
		}
		if secret.IsCredentialBearingConfigKey(key) && !secret.IsReference(value) {
			return fmt.Errorf("%w: config field %q is credential-bearing and must contain a secret reference, never a literal value", ErrBadSourceDef, key)
		}
	}
	return nil
}

func (s *SourceStore) load(ctx context.Context, scope model.TenantID, name string) (model.SourceDef, bool, error) {
	var (
		out model.SourceDef
		ok  bool
	)
	err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Sources().List(ctx, model.Query{Filters: []model.Filter{
			{Column: "scope", Op: model.OpEq, Value: scope.String()},
			{Column: "name", Op: model.OpEq, Value: name},
		}, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			out, ok = rows[0], true
		}
		return nil
	})
	return out, ok, err
}

// Get returns the definition for a source, ok=false if none. The returned Config
// carries secret REFERENCES, never values — safe to surface for editing.
func (s *SourceStore) Get(ctx context.Context, scope model.TenantID, name string) (model.SourceDef, bool, error) {
	return s.load(ctx, scope, strings.TrimSpace(name))
}

// List returns every source definition in a scope, sorted by name.
func (s *SourceStore) List(ctx context.Context, scope model.TenantID) ([]model.SourceDef, error) {
	var rows []model.SourceDef
	err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		all, lerr := drainList(ctx, as.Sources().List, byEq("scope", scope.String(), 0))
		if lerr != nil {
			return lerr
		}
		rows = all
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

// Put creates or updates a source definition (keyed by scope+name) and audits the
// change. It validates internal consistency and the storage-level reference-only
// credential invariant; descriptor-aware validation also runs at apply time.
func (s *SourceStore) Put(ctx context.Context, actor Principal, def model.SourceDef) (model.SourceDef, error) {
	def.Name = strings.TrimSpace(def.Name)
	if def.Scope.IsZero() {
		def.Scope = GlobalSourceScope
	}
	if err := validateDef(def); err != nil {
		return model.SourceDef{}, err
	}
	existing, found, err := s.load(ctx, def.Scope, def.Name)
	if err != nil {
		return model.SourceDef{}, err
	}
	next := def
	if found {
		// Preserve the row identity/version; replace the mutable fields.
		next.BaseFields = existing.BaseFields
	}

	var saved model.SourceDef
	err = s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		var werr error
		if found {
			saved, werr = as.Sources().Update(ctx, next)
		} else {
			saved, werr = as.Sources().Create(ctx, next)
		}
		if werr != nil {
			return werr
		}
		return auditAct(ctx, as, actor, "source.put", sourceDefKind, saved.ID)
	})
	if err != nil {
		return model.SourceDef{}, err
	}
	return saved, nil
}

// SeedAll creates every definition in a SINGLE transaction (the one-time bootstrap
// import of the file roster). It is atomic: if ANY entry is invalid or its
// write fails, the whole transaction rolls back and nothing is persisted — so a
// partial, silently-incomplete migration is impossible, and the caller can simply
// retry on the next boot (the roster stays empty). It returns the number created.
func (s *SourceStore) SeedAll(ctx context.Context, actor Principal, defs []model.SourceDef) (int, error) {
	if len(defs) == 0 {
		return 0, nil
	}
	n := 0
	err := s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		for _, def := range defs {
			def.Name = strings.TrimSpace(def.Name)
			if def.Scope.IsZero() {
				def.Scope = GlobalSourceScope
			}
			if verr := validateDef(def); verr != nil {
				return fmt.Errorf("seed %q: %w", def.Name, verr)
			}
			saved, cerr := as.Sources().Create(ctx, def)
			if cerr != nil {
				return fmt.Errorf("seed %q: %w", def.Name, cerr)
			}
			if aerr := auditAct(ctx, as, actor, "source.seed", sourceDefKind, saved.ID); aerr != nil {
				return aerr
			}
			n++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// Delete removes a source definition. Returns ErrSourceDefNotFound if absent.
func (s *SourceStore) Delete(ctx context.Context, actor Principal, scope model.TenantID, name string) error {
	row, found, err := s.load(ctx, scope, strings.TrimSpace(name))
	if err != nil {
		return err
	}
	if !found {
		return ErrSourceDefNotFound
	}
	return s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if derr := as.Sources().Delete(ctx, row.ID); derr != nil {
			return derr
		}
		return auditAct(ctx, as, actor, "source.delete", sourceDefKind, row.ID)
	})
}
