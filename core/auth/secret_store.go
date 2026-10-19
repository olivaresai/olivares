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
	"github.com/olivaresai/olivares/core/store"
)

// The RUNTIME SECRET STORE: the store-backed, sealed replacement for an
// operator secret that used to live by VALUE in a config file. A named secret's
// value is sealed at rest (an AES-256-GCM SecretSealer bound to the scope, never
// the cleartext, never a one-way hash — the engine must replay the real value to
// the connector at Open) and carries a non-secret hint for display. The engine's
// reference resolver (core/secret) turns a `store:<name>` config reference into
// the opened value at Open, so the literal secret never has to be in the operator
// file. The CRUD here is the console/CLI authoring surface; it mirrors the
// managed-SSO FederationService sealed-CRUD pattern (same package helpers:
// auditAct, byEq, drainList, fingerprint).
//
// Like FederationConfig the rows live in the system tenant and are reachable ONLY
// through the auth partition (store.AuthScope.Secrets), so a module — holding no
// Store — can never read a secret value. The scope axis is global-vs-per-tenant stores and resolves a single GLOBAL scope (GlobalSecretScope).

// GlobalSecretScope is the deployment-wide secret scope — the only scope
// resolves. The column is present from day one so per-tenant secrets are additive.
var GlobalSecretScope = model.SystemTenantID

// secretEntryKind is the audit target kind for secret mutations.
const secretEntryKind model.Kind = "core.secret_entry"

// maxSecretNameLen bounds a secret name (a non-secret handle, not the value).
const maxSecretNameLen = 256

var (
	// ErrNoSecretSealer is returned when a secret must be sealed/opened but no
	// sealer is wired (fail-closed; never store or accept cleartext).
	ErrNoSecretSealer = errors.New("auth: no secret sealer wired; cannot store or open a secret value")
	// ErrSecretNotFound means no secret exists for the (scope, name).
	ErrSecretNotFound = errors.New("auth: no such secret")
	// ErrBadSecretName is returned for an empty or malformed secret name.
	ErrBadSecretName = errors.New("auth: invalid secret name")
	// ErrEmptySecretValue is returned when creating a new secret with no value.
	ErrEmptySecretValue = errors.New("auth: a new secret requires a value")
)

// SecretSealer seals/opens a secret value at rest, bound to its scope as AAD so a
// ciphertext cannot be replayed across scopes. The composition root implements it
// over an engine-held key (cmd/olivares); the core never sees key material. Same
// shape as FederationSealer / eventing.SecretSealer, but a distinct AAD purpose
// and a distinct key, so a sealed secret cannot be opened as an SSO secret.
type SecretSealer interface {
	Seal(ctx context.Context, scope model.TenantID, plaintext []byte) (string, error)
	Open(ctx context.Context, scope model.TenantID, sealed string) ([]byte, error)
}

// SecretView is the non-secret read shape: the name, a fingerprint hint and
// metadata — never the value.
type SecretView struct {
	Name        string
	Hint        string
	Description string
	CreatedAt   model.Timestamp
	UpdatedAt   model.Timestamp
}

// SecretStore owns the runtime secret store: sealed CRUD over the auth partition
// plus the Resolve path the engine's reference resolver consumes.
type SecretStore struct {
	st     authStorage
	sealer SecretSealer
}

// NewSecretStore builds the service. A nil sealer makes writes fail closed (a
// secret can never be stored or opened in cleartext) — reads of metadata still
// work, but Put/Resolve return ErrNoSecretSealer.
func NewSecretStore(st store.Store, sealer SecretSealer) *SecretStore {
	return &SecretStore{st: st, sealer: sealer}
}

// SealerWired reports whether a sealer is available (so the API/CLI can surface an
// honest "secret writes disabled" posture rather than failing per request).
func (s *SecretStore) SealerWired() bool { return s.sealer != nil }

// ValidateSecretName reports a non-empty message when name is not a valid secret
// handle. A name is a non-secret reference target (`store:<name>`): printable,
// bounded, and free of whitespace and the ':' that would split the reference.
func ValidateSecretName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "secret name is required"
	}
	if len(name) > maxSecretNameLen {
		return "secret name too long"
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-' || r == '/':
		default:
			return "secret name may use only letters, digits and . _ - /"
		}
	}
	return ""
}

func (s *SecretStore) load(ctx context.Context, scope model.TenantID, name string) (model.SecretEntry, bool, error) {
	var (
		out model.SecretEntry
		ok  bool
	)
	err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		rows, _, err := as.Secrets().List(ctx, model.Query{Filters: []model.Filter{
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

// Get returns the non-secret view for a secret, ok=false if none.
func (s *SecretStore) Get(ctx context.Context, scope model.TenantID, name string) (SecretView, bool, error) {
	row, ok, err := s.load(ctx, scope, name)
	if err != nil || !ok {
		return SecretView{}, ok, err
	}
	return toSecretView(row), true, nil
}

// List returns the non-secret views for every secret in a scope, sorted by name.
func (s *SecretStore) List(ctx context.Context, scope model.TenantID) ([]SecretView, error) {
	var rows []model.SecretEntry
	err := s.st.AuthView(ctx, func(as store.AuthScope) error {
		all, lerr := drainList(ctx, as.Secrets().List, byEq("scope", scope.String(), 0))
		if lerr != nil {
			return lerr
		}
		rows = all
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]SecretView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toSecretView(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Put creates or updates a secret, sealing the supplied value. An empty value on
// an EXISTING secret keeps the stored sealed value (so editing the description
// never forces re-entering the secret); an empty value on a NEW secret is refused.
// Deny-closed: a write with no sealer wired is refused (never cleartext).
func (s *SecretStore) Put(ctx context.Context, actor Principal, scope model.TenantID, name, value, description string) (SecretView, error) {
	return s.put(ctx, actor, scope, name, value, description, nil, "")
}

// put is Put with two more inputs: the configuration authority a guarded write pins, and a
// non-secret hint to store in place of the value's fingerprint (a vault item's kind).
func (s *SecretStore) put(ctx context.Context, actor Principal, scope model.TenantID, name, value, description string, authority *configurationAuthority, hint string) (SecretView, error) {
	name = strings.TrimSpace(name)
	if msg := ValidateSecretName(name); msg != "" {
		return SecretView{}, fmt.Errorf("%w: %s", ErrBadSecretName, msg)
	}
	existing, found, err := s.load(ctx, scope, name)
	if err != nil {
		return SecretView{}, err
	}
	next := existing
	next.Scope = scope
	next.Name = name
	next.Description = description
	if value != "" {
		if s.sealer == nil {
			return SecretView{}, ErrNoSecretSealer
		}
		sealed, serr := s.sealer.Seal(ctx, scope, []byte(value))
		if serr != nil {
			return SecretView{}, serr
		}
		next.ValueSealed, next.Hint = sealed, fingerprint(value)
		if hint != "" {
			next.Hint = hint
		}
	} else if !found {
		return SecretView{}, ErrEmptySecretValue
	}

	var committed model.SecretEntry
	err = s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if authority != nil {
			if err := authority.pin(ctx, as); err != nil {
				return err
			}
		}
		var (
			saved model.SecretEntry
			werr  error
		)
		if found {
			saved, werr = as.Secrets().Update(ctx, next)
		} else {
			saved, werr = as.Secrets().Create(ctx, next)
		}
		if werr != nil {
			return werr
		}
		committed = saved
		if authority != nil {
			return authority.auditAndFinalize(ctx, as, "secret.put", secretEntryKind, saved.ID)
		}
		return auditAct(ctx, as, actor, "secret.put", secretEntryKind, saved.ID)
	})
	if err != nil {
		return SecretView{}, err
	}
	if authority != nil {
		return toSecretView(committed), nil
	}
	view, _, err := s.Get(ctx, scope, name)
	return view, err
}

// Delete removes a secret. A reference to a deleted secret then fails closed at
// resolution (a connector cannot wire), which is the intended effect of removing
// a credential. Returns ErrSecretNotFound if there is nothing to delete.
func (s *SecretStore) Delete(ctx context.Context, actor Principal, scope model.TenantID, name string) error {
	row, found, err := s.load(ctx, scope, name)
	if err != nil {
		return err
	}
	if !found {
		return ErrSecretNotFound
	}
	return s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if derr := as.Secrets().Delete(ctx, row.ID); derr != nil {
			return derr
		}
		return auditAct(ctx, as, actor, "secret.delete", secretEntryKind, row.ID)
	})
}

// PersonalVaultPrefix opens a person's own namespace in a tenant scope:
// vault/<user-id>/<item>. Such an item is read back by its owner through Reveal and
// is never a `store:` reference. The deployment-wide scope has no such rule: an
// operator's own `vault/token` is an ordinary global secret.
const PersonalVaultPrefix = "vault/"

// open loads a secret and opens its sealed value. It fails closed: a missing sealer is
// ErrNoSecretSealer, a bad name ErrBadSecretName, a missing secret ErrSecretNotFound, a
// value that does not open is the sealer's error. The returned bytes are a fresh copy
// the caller owns.
func (s *SecretStore) open(ctx context.Context, scope model.TenantID, name string) (model.SecretEntry, []byte, error) {
	if s.sealer == nil {
		return model.SecretEntry{}, nil, ErrNoSecretSealer
	}
	name = strings.TrimSpace(name)
	if msg := ValidateSecretName(name); msg != "" {
		return model.SecretEntry{}, nil, fmt.Errorf("%w: %s", ErrBadSecretName, msg)
	}
	row, ok, err := s.load(ctx, scope, name)
	if err != nil {
		return model.SecretEntry{}, nil, err
	}
	if !ok {
		return model.SecretEntry{}, nil, fmt.Errorf("%w: %q", ErrSecretNotFound, name)
	}
	value, err := s.sealer.Open(ctx, scope, row.ValueSealed)
	return row, value, err
}

// Resolve opens a secret's sealed value to its plaintext — the hot path the
// engine's reference resolver calls at Open for a `store:<name>` reference. It
// fails closed like open. A tenant's personal vault item is refused: connectors, the
// deploy executor and providers resolve names a tenant administrator chose, and
// only the item's owner may read it back (Reveal).
func (s *SecretStore) Resolve(ctx context.Context, scope model.TenantID, name string) ([]byte, error) {
	if scope != GlobalSecretScope && strings.HasPrefix(strings.TrimSpace(name), PersonalVaultPrefix) {
		return nil, fmt.Errorf("%w: a personal vault item is not a store reference", ErrBadSecretName)
	}
	_, value, err := s.open(ctx, scope, name)
	return value, err
}

// Reveal opens a secret's sealed value for a person and records the reveal on the
// ledger. It is the one path that hands a stored value to a human; Resolve stays the
// engine's own path to a connector. The ledger entry names the actor and the secret
// entry and never carries the value, and the value is returned only when that entry
// was written: an audit failure refuses the reveal. Like Resolve it fails closed
// (ErrNoSecretSealer, ErrSecretNotFound, the sealer's own error). Whether the actor
// may see this secret is the caller's decision; the API admits only a vault owner.
func (s *SecretStore) Reveal(ctx context.Context, actor Principal, scope model.TenantID, name string) ([]byte, error) {
	row, value, err := s.open(ctx, scope, name)
	if err != nil {
		return nil, err
	}
	if err := s.st.AuthMutate(ctx, func(as store.AuthScope) error {
		return auditAct(ctx, as, actor, "secret.reveal", secretEntryKind, row.ID)
	}); err != nil {
		clear(value)
		return nil, err
	}
	return value, nil
}

func toSecretView(e model.SecretEntry) SecretView {
	return SecretView{
		Name: e.Name, Hint: e.Hint, Description: e.Description,
		CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}
