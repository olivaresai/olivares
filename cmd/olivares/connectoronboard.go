// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
)

// Connector onboarding uses the reconciler's live roster and sealed secret store.
// Stored definitions contain credential references, never inline secret values.
// The catalog offers observation sources; roster providers and document sources
// have separate boot lifecycles. Edition-specific kinds retain their file/CLI seam.

var _ api.ConnectorOnboarding = (*sourceReconciler)(nil)

// ListConnectors returns the connector kinds this build can wire as live observation
// sources, each annotated for descriptor-driven form rendering. In-process kinds
// carry their real declared schema (read from the connector's Descriptor); the
// out-of-process plugin kinds carry no host-known fields (the host cannot introspect
// a subprocess without launching it) — honest, never fabricated.
func (sr *sourceReconciler) ListConnectors(_ context.Context) ([]api.ConnectorInfo, error) {
	out := make([]api.ConnectorInfo, 0, len(inProcSourceFactories)+len(pluginBinaryForKind))
	for kind, constructor := range inProcSourceFactories {
		d := constructor().Descriptor()
		out = append(out, api.ConnectorInfo{
			Kind: kind, Title: d.Title, Description: d.Description,
			Transport: "in_process", FieldsKnown: true,
			Fields:  projectConfigFields(d.ConfigFields),
			Hosting: hostingFromFields(d.ConfigFields),
		})
	}
	for kind := range pluginBinaryForKind {
		// A plugin's fields are not host-known (FieldsKnown false), so there is
		// nothing to derive hosting FROM. Say unknown rather than guess.
		out = append(out, api.ConnectorInfo{
			Kind: kind, Transport: "plugin", FieldsKnown: false,
			Hosting: api.HostingUnknown,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// hostingFromFields reads absolute HTTP(S) URLs from declared field defaults.
// An operator-run endpoint wins regardless of field order; a routable endpoint is
// vendor-hosted. Missing or non-URL defaults leave hosting unknown.
//
// KNOWN COUNTEREXAMPLE: MCP's defaults name optional public feeds, while the observed
// servers can be local stdio commands. This describes its declared endpoints, not
// the observed subject. Placeholder URLs also count by syntax, not ownership.
func hostingFromFields(fields []sdk.ConfigField) string {
	answer := api.HostingUnknown
	for _, f := range fields {
		if f.Default == "" {
			continue
		}
		u, err := url.Parse(f.Default)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			continue
		}
		if isOperatorRunHost(u.Hostname()) {
			// Loopback WINS and returns immediately: a connector that declares both a
			// local server and a vendor endpoint (a hybrid like local's Ollama + a
			// remote vLLM) is still something the operator runs. Without this the
			// answer would depend on ConfigFields ORDER, which no author controls.
			return api.HostingSelfHosted
		}
		answer = api.HostingVendorHosted
	}
	return answer
}

// isOperatorRunHost recognizes this machine and non-public operator networks.
func isOperatorRunHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	// Loopback and unspecified are this machine. Private (RFC1918 / ULA), link-local
	// and CGNAT space are the operator's own network. None of them is reachable as a
	// vendor endpoint from anywhere else.
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// 100.64.0.0/10 — carrier-grade NAT, which is what Tailscale and similar overlay
	// networks hand out. net.IP has no predicate for it.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return true
	}
	return false
}

// projectConfigFields maps a connector's declared ConfigFields to the transport DTO
// the console renders a form from.
func projectConfigFields(fields []sdk.ConfigField) []api.ConnectorField {
	if len(fields) == 0 {
		return nil
	}
	out := make([]api.ConnectorField, 0, len(fields))
	for _, f := range fields {
		out = append(out, api.ConnectorField{
			Key: f.Key, Type: string(f.Type), Required: f.Required,
			Secret: f.Secret, Default: f.Default, Description: f.Description,
		})
	}
	return out
}

// connectorKindKnown reports whether kind is a connector this build can onboard (a
// known in-process kind or an out-of-process plugin kind). Enterprise-only kinds in
// the default build are NOT known (buildInProcSource returns (nil,false) for them).
func connectorKindKnown(kind string) bool {
	if _, ok := pluginBinaryForKind[kind]; ok {
		return true
	}
	_, ok := buildInProcSource(kind)
	return ok
}

// requireKnownOnboardKind rejects an unknown kind before any side effect (so a typo
// never seals a credential for a connector that cannot run).
func requireKnownOnboardKind(kind string) error {
	if connectorKindKnown(kind) {
		return nil
	}
	return fmt.Errorf("%w: unknown connector kind %q (not offered by this build)", auth.ErrBadSourceDef, strings.TrimSpace(kind))
}

// ownedSecretName is the deterministic name of the sealed secret the onboarding flow
// creates for a connector's inline credential field. The `source/` namespace keeps
// auto-owned credentials recognizable (and cascade-deletable) and distinct from
// operator-named secrets.
func ownedSecretName(source, field string) string {
	return "source/" + strings.TrimSpace(source) + "/" + field
}

// ownedSecretRefPrefix is the `store:` reference prefix of every secret a connector
// owns, used to cascade-delete only auto-owned credentials (never an operator- or
// externally-supplied reference) when the connector is removed.
func ownedSecretRefPrefix(source string) string {
	return secret.SchemeStore + ":source/" + strings.TrimSpace(source) + "/"
}

// TestConnector builds the candidate connector and Opens it (then Closes it) WITHOUT
// persisting anything or wiring it live, so the operator can confirm connectivity
// before saving. It resolves secret references and uses a just-typed inline value
// directly. A failure is a SAFE reason — never the raw connector error.
func (sr *sourceReconciler) TestConnector(ctx context.Context, _ auth.Principal, in api.ConnectorOnboardInput) error {
	if err := requireKnownOnboardKind(in.Kind); err != nil {
		return err
	}
	// An out-of-process connector cannot be Opened on the host without launching and
	// wiring its subprocess; the honest answer is that it is validated WHEN SAVED (it
	// is Opened in its subprocess and the live-apply result reports the outcome).
	if _, isPlugin := pluginBinaryForKind[in.Kind]; isPlugin {
		return fmt.Errorf("%w: connection test is only available for in-process connectors; %q runs out-of-process and is validated when you save it", auth.ErrBadSourceDef, in.Kind)
	}
	conn, ok := buildInProcSource(in.Kind)
	if !ok {
		return fmt.Errorf("%w: unknown connector kind %q", auth.ErrBadSourceDef, in.Kind)
	}
	resolved, err := sr.resolveTestConfig(ctx, conn.Descriptor(), in)
	if err != nil {
		return err // already genericized
	}
	if oerr := conn.Open(ctx, resolved); oerr != nil {
		_ = conn.Close(ctx) // Close is safe even when Open failed (SDK contract)
		// The Open error ran against the RESOLVED config and can embed a live
		// credential — log it at Debug, surface only the generic sentinel.
		sr.log.Debug("connector onboarding: connectivity test open failed", "kind", in.Kind, "source", in.Name, "err", oerr)
		return api.ErrConnectorTestFailed
	}
	_ = conn.Close(ctx)
	return nil
}

// resolveTestConfig builds the live config to Open a test candidate with: non-secret
// settings and secret REFERENCES (a blank secret field opens the EXISTING sealed
// value via its stored reference) are resolved through the engine's resolver; an
// inline LITERAL secret is held aside and overlaid AFTER resolution (the resolver's
// strict mode would refuse a literal in a declared-secret field). Nothing is sealed
// or persisted. A resolver error is genericized (it can name a backend/locator).
func (sr *sourceReconciler) resolveTestConfig(ctx context.Context, desc sdk.Descriptor, in api.ConnectorOnboardInput) (sdk.Config, error) {
	secretField := map[string]bool{}
	for _, f := range desc.ConfigFields {
		if f.Secret {
			secretField[f.Key] = true
		}
	}
	existing, _, err := sr.store.Get(ctx, sr.scope, in.Name)
	if err != nil {
		return sdk.Config{}, err
	}
	refCfg := make(map[string]string, len(in.Config)+len(in.Secrets))
	for k, v := range in.Config {
		if secretField[k] {
			continue // a secret-declared field is taken from in.Secrets, never Config
		}
		refCfg[k] = v
	}
	literals := map[string]string{}
	for field, val := range in.Secrets {
		switch {
		case val == "":
			if ref := existing.Config[field]; ref != "" {
				refCfg[field] = ref // open the already-stored sealed value
			}
		case secret.IsReference(val):
			refCfg[field] = val // an existing/external reference, used as-is
		default:
			literals[field] = val // overlaid post-resolution
		}
	}
	resolved, rerr := resolveConfig(ctx, sr.resolver, desc, sdk.Config{Settings: refCfg})
	if rerr != nil {
		return sdk.Config{}, fmt.Errorf("%w: a secret reference could not be resolved", auth.ErrBadSourceDef)
	}
	out := resolved.Settings
	if out == nil {
		out = map[string]string{}
	}
	for field, lit := range literals {
		out[field] = lit
	}
	return sdk.Config{Settings: out}, nil
}

// PutConnector seals inline secrets, persists only references and applies the source
// live. SourceApplyResult distinguishes persistence from successful application.
func (sr *sourceReconciler) PutConnector(ctx context.Context, actor auth.Principal, in api.ConnectorOnboardInput) (api.SourceApplyResult, error) {
	if err := requireKnownOnboardKind(in.Kind); err != nil {
		return api.SourceApplyResult{}, err
	}
	// Validate the operator-facing identity BEFORE sealing, so an invalid name/tenant
	// never leaves an orphan sealed credential behind.
	if msg := auth.ValidateSourceName(in.Name); msg != "" {
		return api.SourceApplyResult{}, fmt.Errorf("%w: %s", auth.ErrBadSourceName, msg)
	}
	if strings.TrimSpace(in.Tenant) == "" {
		return api.SourceApplyResult{}, fmt.Errorf("%w: a connector must name the business tenant its observations belong to", auth.ErrBadSourceDef)
	}
	cfg, err := sr.sealOnboardSecrets(ctx, actor, in)
	if err != nil {
		return api.SourceApplyResult{}, err
	}
	credentialsChanged := false
	for _, value := range in.Secrets {
		if value != "" && !secret.IsReference(value) {
			credentialsChanged = true
			break
		}
	}
	return sr.putSource(ctx, actor, api.SourceRosterInput{
		Name: in.Name, Kind: in.Kind, Tenant: in.Tenant,
		PollSeconds: in.PollSeconds, Enabled: in.Enabled, Config: cfg,
	}, credentialsChanged)
}

// sealOnboardSecrets resolves the inline secret fields into the reference-only Config
// the durable roster stores: a blank field keeps the existing reference; an
// already-reference value is used verbatim; any other literal is SEALED into the
// secret store under a deterministic owned name and replaced by its `store:<name>`
// reference. The non-secret settings pass through unchanged.
func (sr *sourceReconciler) sealOnboardSecrets(ctx context.Context, actor auth.Principal, in api.ConnectorOnboardInput) (map[string]string, error) {
	cfg := make(map[string]string, len(in.Config)+len(in.Secrets))
	for k, v := range in.Config {
		cfg[k] = v
	}
	if len(in.Secrets) == 0 {
		return cfg, nil
	}
	existing, _, err := sr.store.Get(ctx, sr.scope, in.Name)
	if err != nil {
		return nil, err
	}
	// Deterministic order so the sealed-secret writes (and their audit) are stable.
	fields := make([]string, 0, len(in.Secrets))
	for field := range in.Secrets {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		val := in.Secrets[field]
		switch {
		case val == "":
			if ref := existing.Config[field]; ref != "" {
				cfg[field] = ref // keep the stored sealed value (blank = keep)
			}
			// A blank field with no stored value leaves the setting unset; the live
			// apply (or test) reports honestly if the connector requires it.
		case secret.IsReference(val):
			cfg[field] = val // operator pointed at an existing/external secret; verbatim
		default:
			if sr.secrets == nil {
				return nil, auth.ErrNoSecretSealer // deny-closed: never persist a literal
			}
			name := ownedSecretName(in.Name, field)
			desc := fmt.Sprintf("console connector %q credential for field %q", strings.TrimSpace(in.Name), field)
			if _, perr := sr.secrets.Put(ctx, actor, auth.GlobalSecretScope, name, val, desc); perr != nil {
				return nil, perr
			}
			cfg[field] = secret.SchemeStore + ":" + name
		}
	}
	return cfg, nil
}

// DeleteConnector removes the source (stopping it live) and then deletes the
// onboarding-OWNED sealed credentials it created. Only auto-owned secrets
// (`store:source/<name>/<field>`) are removed; an operator- or externally-supplied
// reference is left untouched. The secret cleanup is best-effort: the source is
// already gone, so a failed credential delete is logged, not fatal.
func (sr *sourceReconciler) DeleteConnector(ctx context.Context, actor auth.Principal, name string) (api.SourceApplyResult, error) {
	name = strings.TrimSpace(name)
	// Read the definition BEFORE deleting so we know which owned credentials to clean.
	def, found, lerr := sr.store.Get(ctx, sr.scope, name)
	if lerr != nil {
		return api.SourceApplyResult{}, lerr
	}
	res, err := sr.DeleteSource(ctx, actor, name)
	if err != nil {
		return res, err
	}
	if found && sr.secrets != nil {
		remaining, lerr := sr.store.List(ctx, sr.scope)
		if lerr != nil {
			sr.log.Warn("connector onboarding: could not verify credential ownership after source removal", "source", name, "err", lerr)
			return res, nil
		}
		prefix := ownedSecretRefPrefix(name)
	credentials:
		for _, ref := range def.Config {
			target, ok := secret.ParseReference(ref)
			if !ok || !strings.HasPrefix(target.Scheme+":"+target.Locator, prefix) {
				continue
			}
			// Source names and setting keys may contain slashes. Preserve ambiguous
			// namespace ownership and references still used by another stored source.
			for _, other := range remaining {
				if strings.HasPrefix(target.Scheme+":"+target.Locator, ownedSecretRefPrefix(other.Name)) {
					continue credentials
				}
				for _, otherRef := range other.Config {
					otherTarget, ok := secret.ParseReference(otherRef)
					if ok && otherTarget == target {
						continue credentials
					}
				}
			}
			if derr := sr.secrets.Delete(ctx, actor, auth.GlobalSecretScope, target.Locator); derr != nil && !errors.Is(derr, auth.ErrSecretNotFound) {
				sr.log.Warn("connector onboarding: could not delete owned credential after source removal", "source", name, "err", derr)
			}
		}
	}
	return res, nil
}
