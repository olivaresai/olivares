// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package secret is the engine's RUNTIME SECRET RESOLVER: it turns a
// secret REFERENCE of the form "<scheme>:<locator>" — carried in a connector /
// notify / identity / MCP config — into the live secret VALUE at Open, so the
// literal secret never has to sit by value in the operator's config file. This is
// the code that finally makes the long-decorative "a credential is always a
// reference, never the secret" promise true (sdk.Config docs, docs/SECURITY-HARDENING.md).
//
// The resolver is a pure dispatcher over a registry of scheme HANDLERS (env / file
// built in here; the sealed DB store and the external backends — vault and the
// cloud secret managers — are injected by the composition root, which owns the
// store and the network transports). It binds NO key material and opens no
// network of its own. It is consumed at the composition root (cmd/olivares),
// shared by `serve` and `collector`; a scheme whose handler is not wired in a
// given process (e.g. `store:` in the storeless collector) fails CLOSED rather
// than passing a reference through as if it were a value.
//
// Strict posture ("estricto ya"): a config field a connector DECLARES as a
// secret (Descriptor ConfigField Secret=true) may hold ONLY an empty value or a
// reference — an inline literal secret is REFUSED at Open. References in any field
// (secret or not) are resolved; a non-secret field's non-reference value passes
// through unchanged.
package secret

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/sdk"
)

// Scheme constants for the secret-reference grammar. `db` is an accepted alias of
// `store` (both name the sealed runtime secret store). The set is closed: a value
// whose scheme is NOT one of these is treated as a literal, never a reference, so
// an ordinary config value (a URL like "https://…", a path) is never mistaken for
// a secret reference.
const (
	SchemeStore             = "store"
	SchemeDB                = "db"
	SchemeEnv               = "env"
	SchemeFile              = "file"
	SchemeVault             = "vault"
	SchemeAWSSecretsManager = "aws-secretsmanager"
	SchemeGCPSecretManager  = "gcp-secretmanager"
	SchemeAzureKeyVault     = "azure-keyvault"
	SchemeInfisical         = "infisical"
	SchemeK8sSecret         = "k8s-secret"
)

// knownSchemes is the closed allow-list of reference schemes the grammar
// recognizes, independent of which handlers a given process has wired. A value
// using one of these IS a reference (so a secret field carrying it is accepted,
// and resolution is attempted); a value using any other scheme is a literal.
var knownSchemes = map[string]bool{
	SchemeStore: true, SchemeDB: true, SchemeEnv: true, SchemeFile: true, SchemeVault: true,
	SchemeAWSSecretsManager: true, SchemeGCPSecretManager: true, SchemeAzureKeyVault: true,
	SchemeInfisical: true, SchemeK8sSecret: true,
}

// canonicalScheme folds aliases (db → store) and lower-cases the scheme.
func canonicalScheme(scheme string) string {
	s := strings.ToLower(strings.TrimSpace(scheme))
	if s == SchemeDB {
		return SchemeStore
	}
	return s
}

// Reference is a parsed "<scheme>:<locator>" secret reference.
type Reference struct {
	Scheme  string // canonical (db folded to store), lower-cased
	Locator string // everything after the first ':', trimmed
}

// ParseReference splits a value into a Reference when it is a recognized
// "<scheme>:<locator>" with a known scheme and a non-empty locator; ok=false for
// any literal value (no colon, an unknown scheme, or an empty locator).
func ParseReference(value string) (Reference, bool) {
	if hasInlineAuthorityCredential(strings.TrimSpace(value)) {
		return Reference{}, false
	}
	scheme, locator, found := strings.Cut(value, ":")
	if !found {
		return Reference{}, false
	}
	cs := canonicalScheme(scheme)
	if !knownSchemes[cs] {
		return Reference{}, false
	}
	locator = strings.TrimSpace(locator)
	if locator == "" {
		return Reference{}, false
	}
	return Reference{Scheme: cs, Locator: locator}, true
}

// IsReference reports whether value is a recognized secret reference.
func IsReference(value string) bool {
	_, ok := ParseReference(value)
	return ok
}

// Handler resolves a scheme's locator to the live secret value. Implementations
// MUST fail closed (a missing/unreadable secret is an error, never an empty
// value) and MUST never log the value. The env/file handlers live here; the
// store and network handlers are injected by the composition root.
type Handler interface {
	Resolve(ctx context.Context, locator string) ([]byte, error)
}

// Resolver dispatches references to their scheme handlers and enforces the strict
// no-inline-secret posture. It is immutable after construction (safe for
// concurrent Resolve calls).
type Resolver struct {
	handlers map[string]Handler
	strict   bool
}

// Option configures a Resolver.
type Option func(*Resolver)

// WithStrict toggles the no-inline-secret enforcement. Production always uses the
// default (true, "estricto ya"); the option exists only so tests can exercise the
// resolution path without the enforcement.
func WithStrict(strict bool) Option { return func(r *Resolver) { r.strict = strict } }

// NewResolver builds a resolver over a scheme→handler registry. A scheme key may
// be `store` or `db` (folded to store), env, file, vault or any cloud-manager
// scheme. Strict enforcement is on by default.
func NewResolver(handlers map[string]Handler, opts ...Option) *Resolver {
	reg := make(map[string]Handler, len(handlers))
	for scheme, h := range handlers {
		if h != nil {
			reg[canonicalScheme(scheme)] = h
		}
	}
	r := &Resolver{handlers: reg, strict: true}
	for _, o := range opts {
		o(r)
	}
	return r
}

// ErrInlineSecret is the strict-mode refusal: a declared-secret field carries a
// non-reference literal. It never embeds the value.
type ErrInlineSecret struct{ Field string }

func (e ErrInlineSecret) Error() string {
	return fmt.Sprintf("config field %q carries an inline secret; a secret must be a reference "+
		"(store:<name>, vault:<path>#<key>, env:<VAR>, file:<path>, aws-secretsmanager:<id>, "+
		"gcp-secretmanager:<project>/<name>, azure-keyvault:<vault>/<name>, infisical:<path>, "+
		"k8s-secret:<ns>/<name>/<key>) — inline secrets in a config file are refused", e.Field)
}

// Resolve returns a NEW sdk.Config whose secret references have been replaced by
// their live values. The operator's original Settings map is never mutated. For
// every setting:
//
//   - a recognized reference is resolved through its handler (fail-closed: an
//     unwired scheme, or a handler error, is returned, never passed through);
//   - a literal value in a field the descriptor DECLARES secret is REFUSED under
//     strict mode (ErrInlineSecret);
//   - any other literal passes through unchanged.
//
// desc may be the zero Descriptor (an external plugin whose fields are not known
// to the host): references are still resolved, but the strict no-inline-secret
// check — which needs the per-field Secret flag — is skipped for that call.
func (r *Resolver) Resolve(ctx context.Context, desc sdk.Descriptor, cfg sdk.Config) (sdk.Config, error) {
	secretField := secretFieldSet(desc)
	out := make(map[string]string, len(cfg.Settings))
	// Deterministic iteration so a failure is reported on a stable first offender.
	keys := make([]string, 0, len(cfg.Settings))
	for k := range cfg.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := cfg.Settings[k]
		if v == "" {
			out[k] = v
			continue
		}
		ref, isRef := ParseReference(v)
		if !isRef {
			if r.strict && secretField[k] {
				return sdk.Config{}, ErrInlineSecret{Field: k}
			}
			out[k] = v
			continue
		}
		val, err := r.resolveRef(ctx, ref)
		if err != nil {
			// Never embed the locator's resolved value; the ref string is non-secret.
			return sdk.Config{}, fmt.Errorf("resolve %s reference for field %q: %w", ref.Scheme, k, err)
		}
		out[k] = string(val)
	}
	return sdk.Config{Settings: out}, nil
}

// resolveRef dispatches one reference to its handler, failing closed when the
// scheme is recognized by the grammar but no handler is wired in this process
// (e.g. `store:` in the collector, which holds no secret store).
func (r *Resolver) resolveRef(ctx context.Context, ref Reference) ([]byte, error) {
	h := r.handlers[ref.Scheme]
	if h == nil {
		return nil, fmt.Errorf("secret scheme %q is recognized but not available in this process "+
			"(no handler wired) — provision the secret another way or run where the backend is reachable", ref.Scheme)
	}
	val, err := h.Resolve(ctx, ref.Locator)
	if err != nil {
		return nil, err
	}
	return val, nil
}

// CheckNoInlineSecrets reports the first config field the descriptor DECLARES
// secret that carries a literal value instead of a `<scheme>:<locator>` reference,
// as ErrInlineSecret. It performs NO resolution (no backend, no network) — a pure
// syntactic guard suitable at AUTHORING time, before the referenced secret need
// even exist. It is what lets the durable source roster reject an inline secret
// when a row is written, so a literal credential never lands in the store,
// rather than only failing later at Open. desc may be the zero Descriptor (an
// external plugin whose fields the host does not know): the check is then a no-op
// — plugin secret fields are operator discipline, same as the file-config model.
func CheckNoInlineSecrets(desc sdk.Descriptor, cfg sdk.Config) error {
	secretField := secretFieldSet(desc)
	if len(secretField) == 0 {
		return nil
	}
	keys := make([]string, 0, len(cfg.Settings))
	for k := range cfg.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic first offender
	for _, k := range keys {
		v := cfg.Settings[k]
		if v == "" || !secretField[k] {
			continue
		}
		if !IsReference(v) {
			return ErrInlineSecret{Field: k}
		}
	}
	return nil
}

// secretFieldSet returns the set of config keys the descriptor declares secret.
func secretFieldSet(desc sdk.Descriptor) map[string]bool {
	if len(desc.ConfigFields) == 0 {
		return nil
	}
	set := make(map[string]bool, len(desc.ConfigFields))
	for _, f := range desc.ConfigFields {
		if f.Secret {
			set[f.Key] = true
		}
	}
	return set
}
