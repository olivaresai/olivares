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

// connectoronboard.go is the composition-root half of the console connector
// ONBOARDING (the api.ConnectorOnboarding implementation). It lets an operator add,
// configure, test and remove a connector AND its credentials from the embedded
// console — sealed and persisted in the database — instead of editing the boot
// config file by hand. It REUSES the two surfaces the reconciler already owns rather
// than introducing a third secret path:
//
//   - the secret store (sr.secrets): an inline credential the operator types is
//     SEALED at rest and the source row keeps only a `store:<name>` REFERENCE — never
//     the literal. This preserves invariant that the durable roster is
//     non-secret-bearing (checkInlineSecrets enforces it on the write).
//   - the live roster (sr.PutSource / sr.DeleteSource): the reference-only source
//     is persisted and applied to the running engine WITHOUT a restart.
//
// Only OBSERVATION-source kinds are onboardable here — exactly what the live roster
// can wire (buildInProcSource + pluginBinaryForKind). Identity / roster providers and
// knowledge document sources are read once at boot (a restart domain — see
// requiresRestartDomains), so they are NOT in the catalog: an honest absence, never a
// pretend-live-reconfigurable connector. Enterprise-only kinds (e.g. CyberArk Conjur)
// are not enumerated in the console catalog yet either — under -tags enterprise they
// are still wired by file/CLI; surfacing them here is a follow-on.

var _ api.ConnectorOnboarding = (*sourceReconciler)(nil)

// inProcConnectorKinds is the maintained list of first-party IN-PROCESS
// observation-source kinds the console offers, in the same grouping as the
// buildInProcSource switch (the source of truth). Each kind here MUST construct via
// buildInProcSource — TestConnectorCatalogKinds asserts it, so a renamed/removed kind
// fails the build's test rather than silently 404ing in the console. Adding a new
// in-process source kind to buildInProcSource without adding it here only means it is
// not yet offered in the console (a graceful omission, never a wrong form). Aliases
// (okta/entra→idp, pg-audit→pgaudit, …) are represented by their canonical kind.
var inProcConnectorKinds = []string{
	// model-provider & agent governance
	"vault", "claude-api", "claude-config", "claude-managed-agents", "claude-projects", "codex", "cursor",
	"gemini-cli", "openclaw", "hermes", "fal", "vertex", "azure-openai", "mistral", "xai", "claude-compliance",
	"claude-apps-gateway", "managed-settings", "codex-managed-config", "agents-md", "mcpb", "cowork-analytics",
	"deepseek", "glm", "openrouter", "cohere", "claude-batch", "claude-routines",
	// first-party base providers — built long ago, never composed, so the console
	// could not offer them: OpenAI platform, Gemini API, and local/self-hosted inference.
	"openai", "gemini", "local",
	// local agent-surface config observers
	// grok = Grok Build (xAI) leido por su configuracion LOCAL. Va AQUI y no con los
	// proveedores: `xai` lee la API de modelos, este lee el AGENTE (connectors/grok/grok.go:12-21).
	"openhands", "goose", "cline", "opencode", "grok",
	// identity/auth telemetry observers
	"kerberos", "aaa", "ssf", "edugain", "openidfed",
	// data-platform R/RW observers
	"snowflake-audit", "databricks-uc", "bigquery-audit", "mssql-audit", "oracle-audit",
	"mongo-audit", "redshift-audit", "gcs-audit", "azure-blob-audit", "iceberg-catalog",
	"openlineage", "delta-sharing",
	// cloud management-plane + edge-estate observers (S165)
	"gcp-audit", "azure-activity", "cloudflare", "bedrock-kb",
	// secrets/PKI/KMS observers
	"aws-kms", "gcp-kms", "azure-key-vault", "external-secrets", "sops", "kmip",
	// network/mesh/gateway L7 observers + AI-gateway config posture
	"istio-telemetry", "inference-gateway", "egress-proxy", "ai-gateway", "kong-audit",
	"envoy-ai-gateway", "kong-agent-gateway", "litellm",
	// IaC/GitOps observers
	"argocd", "flux", "crossplane",
	// access-map differential connectors + federation edge/finding scans
	"vault-audit", "onepassword", "entra-agent", "agentcore", "oasf",
	"ldap", "idp", "infisical", "pgaudit", "s3cloudtrail", "ebpf", "runtime", "mcp",
	// agent-platform governance + code-repo sources
	"agent365", "google-agent", "google-adk", "foundry-agents", "github", "gitlab",
	// agent-interop protocol observers (A2A Agent Cards + task-lifecycle edges)
	"a2a",
	// tactical/DDIL
	"tak",
}

// ListConnectors returns the connector kinds this build can wire as live observation
// sources, each annotated for descriptor-driven form rendering. In-process kinds
// carry their real declared schema (read from the connector's Descriptor); the
// out-of-process plugin kinds carry no host-known fields (the host cannot introspect
// a subprocess without launching it) — honest, never fabricated.
func (sr *sourceReconciler) ListConnectors(_ context.Context) ([]api.ConnectorInfo, error) {
	out := make([]api.ConnectorInfo, 0, len(inProcConnectorKinds)+len(pluginBinaryForKind))
	for _, kind := range inProcConnectorKinds {
		conn, ok := buildInProcSource(kind)
		if !ok {
			// Defensive: a kind that no longer builds is omitted rather than crashing
			// the catalog. The drift test keeps this from happening silently.
			sr.log.Warn("connector onboarding: catalog kind no longer builds; omitted", "kind", kind)
			continue
		}
		d := conn.Descriptor()
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

// hostingFromFields derives api.ConnectorInfo.Hosting from the connector's OWN
// declared endpoint defaults. It is deliberately NOT a hand-maintained list of which
// kinds are self-hosted: such a list is an opinion that drifts silently the moment a
// connector changes, and there are 100+ kinds to be wrong about.
//
// The signal used is the one the connector already publishes: the Default of a URL
// setting is the author's statement of where the thing normally lives. A default on
// the LOOPBACK host says "you run this"; a routable vendor URL says "they run it".
//
// CALIBRATED against the live catalog (2026-08-09, all 104 kinds served by this
// build), which is what makes it a measurement and not a guess:
//   - operator-run ⇒ 3 kinds: local (http://localhost:11434), vault and vault-audit
//     (https://127.0.0.1:8200). All three are software the operator runs.
//   - routable ⇒ 32 kinds, all but one a vendor cloud API (api.openai.com,
//     generativelanguage.googleapis.com, api.anthropic.com, graph.microsoft.com…).
//   - neither ⇒ 69 kinds that declare no endpoint at all (gemini-cli, litellm,
//     openhands… — local CONFIG observers). They get unknown, which is honest: no
//     endpoint was declared, so nothing was measured.
//
// ⚠ KNOWN COUNTEREXAMPLE, and it is recorded rather than papered over: `mcp` lands in
// vendor_hosted and its observed subject need not be a vendor at all. Its declared
// defaults are three AUXILIARY public feeds (the MCP registry, a deprecation feed and
// the Docker catalog), each opt-in; the servers it actually introspects come from
// config and can be local stdio commands. So this answers "where do this connector's
// DECLARED default endpoints point", which for 103 of 104 kinds is the same question
// as "where does the observed system run" — and for `mcp` is not. Distinguishing an
// auxiliary feed from the primary subject needs semantic descriptor metadata the SDK
// does not carry today; inventing a field-name heuristic here would trade a known,
// bounded wrong answer for an unbounded one. TestHostingKnownCounterexample pins it so
// the limitation cannot quietly become a claim of correctness.
//
// A field is only consulted when its Default PARSES as an absolute http(s) URL, so a
// free-text description can never move a kind between answers. A PLACEHOLDER that does
// parse (https://vault.example.com) still counts as routable — the rule reads syntax,
// not ownership, and cannot tell an example host from a real one.
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

// isOperatorRunHost reports whether a URL hostname names a machine the OPERATOR runs:
// this host, or one on their own network. No vendor's cloud API lives at any of these
// addresses, so a connector defaulting to one is shipping the expectation that you run
// the thing it observes.
//
// It is deliberately wider than loopback, and the name says so. An earlier version was
// called isLoopbackHost and answered only for 127.0.0.0/8, ::1 and the literal name,
// which had two consequences the contrast caught: a default of http://10.0.0.5 was
// classified as a VENDOR cloud — nobody's vendor is at an RFC1918 address — and
// 0.0.0.0 was being called "loopback", which it is not (it is the unspecified address;
// it belongs here for the same reason, not for that one).
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

// PutConnector seals each inline secret into the store (storing only a
// reference), then persists the reference-only source and applies it live (the
// PutSource path, which also auto-triggers the reconcile of THIS source). The
// returned SourceApplyResult reports persisted-vs-applied honestly.
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
	return sr.PutSource(ctx, actor, api.SourceRosterInput{
		Name: in.Name, Kind: in.Kind, Tenant: in.Tenant,
		PollSeconds: in.PollSeconds, Enabled: in.Enabled, Config: cfg,
	})
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
		prefix := ownedSecretRefPrefix(name)
		for _, ref := range def.Config {
			if !strings.HasPrefix(ref, prefix) {
				continue
			}
			locator := strings.TrimPrefix(ref, secret.SchemeStore+":")
			if derr := sr.secrets.Delete(ctx, actor, auth.GlobalSecretScope, locator); derr != nil && !errors.Is(derr, auth.ErrSecretNotFound) {
				sr.log.Warn("connector onboarding: could not delete owned credential after source removal", "source", name, "err", derr)
			}
		}
	}
	return res, nil
}
