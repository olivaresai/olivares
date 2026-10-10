// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package mcpgateway is the inbound agent-protocols gateway: the inline MCP
// Resource-Server PEP of connectors/mcp bound to the AGPL plane the connectors may
// not import — the operator configuration, the evidence journal and ledger, the
// upstream tool backend and the estate kill switch. cmd/olivares wires it from the
// engine (mcpgateway.go there); the connectors own the protocol and the deny-closed
// seams. Every governance seam stays deny-closed.
package mcpgateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
)

// Config is the operator provisioning for the inbound agent surface.
type Config struct {
	Listen string `json:"listen"`
	// MCPSource selects exactly one owner. Existing files default to file; no file defaults to store.
	MCPSource string `json:"mcp_source"`
	// SessionTools enables the product's private session MCP tools. Default off.
	SessionTools bool              `json:"session_tools"`
	MCP          *MCPConfig        `json:"mcp"`
	A2APush      *A2APushConfig    `json:"a2a_push"`
	A2AInbound   *A2AInboundConfig `json:"a2a_inbound"`
	// MCPRegistry provisions the embedded PRIVATE MCP sub-registry: the
	// generic registry OpenAPI /v0.1 served per tenant under /mcp-registry/
	// (tenant paths /mcp-registry/t/{tenant}/v0.1/..., default tenant on the bare
	// /mcp-registry/v0.1/...). The entries are the operator-APPROVED set — the
	// Internal registry elevated to a served registry (the official preview
	// registry rejects private servers; GitHub's org/enterprise MCP registries
	// require exactly this bring-your-own /v0.1 surface). Serving approved
	// modules/catalog kindMCP entries from the store is the follow-up provider
	// seam — provisioning is config-declared today, like the toolset.
	MCPRegistry *mcpc.SubRegistryConfig `json:"mcp_registry"`
}

// Source ownership cannot be resolved by last-key-wins JSON semantics. Legacy
// operator fields retain their decoding contract; an explicit source is strict.
func (cfg *Config) UnmarshalJSON(raw []byte) error {
	type plain Config
	var decoded plain
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return errors.New("agent-gateway: expected configuration object")
	}
	seen := false
	fileMCPDeclared, fileSessionEnabled := false, false
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if strings.EqualFold(key.(string), "mcp") && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			fileMCPDeclared = true
		}
		if strings.EqualFold(key.(string), "session_tools") {
			var on bool
			if json.Unmarshal(value, &on) == nil && on {
				fileSessionEnabled = true
			}
		}
		if strings.EqualFold(key.(string), "mcp_source") {
			var source string
			if seen || json.Unmarshal(value, &source) != nil || (source != "file" && source != "store") {
				return errors.New("agent-gateway: mcp_source must be one explicit file or store declaration")
			}
			seen = true
			decoded.MCPSource = source
		}
	}
	if decoded.MCPSource == "store" && (fileMCPDeclared || fileSessionEnabled) {
		return errors.New("agent-gateway: store source conflicts with file MCP declarations")
	}
	*cfg = Config(decoded)
	return ValidateSource(*cfg)
}

func ValidateSource(cfg Config) error {
	switch cfg.MCPSource {
	case "", "file":
		return nil
	case "store":
		if cfg.MCP != nil || cfg.SessionTools {
			return errors.New("agent-gateway: mcp_source store conflicts with file-owned mcp or session_tools; remove those declarations explicitly")
		}
		return nil
	default:
		return errors.New("agent-gateway: mcp_source must be file or store")
	}
}

// MCPConfig provisions the inline MCP Resource-Server PEP. Token trust is
// ISSUER-KEYED: `issuers` is the multi-issuer form; the legacy single-issuer
// fields remain accepted and are folded in by the connector. `issuer` is REQUIRED
// when any legacy anchor field is set — an RS that cannot validate the iss claim of
// every token (RFC 9068 §4) refuses to mount instead of skipping the check.
type MCPConfig struct {
	// Store-owned forwarding is injected by the governed composition, never JSON.
	ManagedUpstream           mcpc.Upstream     `json:"-"`
	ManagedUpstreamDescriptor string            `json:"-"`
	Resource                  string            `json:"resource"`
	AuthorizationServers      []string          `json:"authorization_servers"`
	ScopesSupported           []string          `json:"scopes_supported"`
	Issuers                   []IssuerTrust     `json:"issuers"`
	Issuer                    string            `json:"issuer"`
	IssuerJWKS                json.RawMessage   `json:"issuer_jwks"`
	JWKSURL                   string            `json:"jwks_url"`
	IntrospectionURL          string            `json:"introspection_url"`
	IntrospectionAuth         string            `json:"introspection_auth"` // secret: the RS's OWN introspection credential
	Tenant                    string            `json:"tenant"`
	Tools                     []mcpc.ToolPolicy `json:"tools"`
	// RoleClaim is the token claim the per-role tool allowlist (E1) reads roles from
	// (default "roles"); per-tool AllowedRoles ride inside each Tools entry.
	RoleClaim      string   `json:"role_claim"`
	AllowedOrigins []string `json:"allowed_origins"`
	// RequireDPoP requires every authenticated request to present a DPoP-bound
	// access token with a matching proof.
	RequireDPoP bool `json:"require_dpop"`
	// RequireDPoPNonce additionally requires the RS nonce in each DPoP proof; the
	// client retries after the use_dpop_nonce challenge.
	RequireDPoPNonce bool `json:"require_dpop_nonce"`
	// AcceptMTLSBoundTokens verifies RFC 8705 x5t#S256-bound access tokens against
	// the TLS peer certificate. It only works when this process terminates TLS with
	// client-cert negotiation; behind a TLS-terminating proxy, the peer certificate
	// is not visible and bound tokens fail closed.
	AcceptMTLSBoundTokens bool   `json:"accept_mtls_bound_tokens"`
	UpstreamURL           string `json:"upstream_url"`
	UpstreamAuth          string `json:"upstream_auth"` // secret: a SEPARATE upstream credential (NEVER the inbound token)
	// UpstreamRevision RECORDS the MCP protocol revision the upstream speaks, as
	// CONFIGURATION (round-5 R5-05). Nothing here negotiates or discovers it.
	//
	// ROUND-7 R7-07: an empty value ASSUMES the connector baseline (2026-07-28).
	// The round-6 wording "it is not a guess" was false — an unset field is exactly
	// an assumption, and it is the OPERATOR's to get right; the connector-side
	// comments were corrected for this and this one was missed. What IS true is the
	// failure direction: the operator reconciliation read synthesizes a
	// Tasks-extension request for the configured revision, and an upstream declared
	// as a revision whose Tasks extension this connector does not implement has that
	// read REFUSED rather than answered with a fabricated legacy shape — deny-closed,
	// so a mismatch retains the record instead of draining it on an unreadable
	// answer. Correcting a wrong value normally rebuilds the RS, which loses the
	// process-local task inventory (see connectors/mcp/taskreconcile.go).
	UpstreamRevision string `json:"upstream_revision"`
	// NextRevisionHeaders controls the MCP 2026-07-28 L7 header gate
	// (Mcp-Method/Mcp-Name deny-closed before body parse). Default OFF at the
	// operator-config level for backward-compat; set to true to enable (maps to
	// DisableNextRevisionHeaders:false on the RS).
	//
	// OMITTING IT KEEPS LEGACY IN THIS COMPOSITION. The previous sentence here
	// said to omit it for deployments that speak 2026-07-28 "because the RS layer
	// defaults ON", which is true of the connector in isolation and false of this
	// gateway: the builder always passes DisableNextRevisionHeaders as the
	// NEGATION of this field, so an omitted field arrives as
	// DisableNextRevisionHeaders:true and the RS resolves LEGACY. true selects
	// dual; the full pair is the RevisionMode table below.
	NextRevisionHeaders bool `json:"next_revision_headers"`
	// RevisionMode states this gateway's MCP revision posture EXPLICITLY,
	// with exactly the values connectors/mcp already implements: "legacy", "dual"
	// or "rc-strict". It is OPTIONAL and moves no default. The whole resolution
	// table, because the PAIR is what an operator actually reads:
	//
	//	revision_mode absent  + next_revision_headers absent or false → legacy
	//	revision_mode absent  + next_revision_headers true            → dual
	//	revision_mode present + (bool absent)                         → that mode
	//	revision_mode present + bool agreeing with its header posture → that mode
	//	revision_mode present + bool contradicting it                 → REFUSED
	//
	// A present mode must be one of the three; unknown, empty, whitespace-only or
	// null refuses to mount and names the accepted values. "legacy" means the
	// 2026-07-28 header gate is OFF, "dual" and "rc-strict" mean it is ON, so an
	// EXPLICITLY supplied next_revision_headers must agree — and an ABSENT boolean
	// contradicts nothing, which is the whole reason the decode records presence.
	//
	// What this field is NOT: a compatibility, conformance or interoperability
	// claim. It selects the posture the Resource Server enforces; nothing here has
	// been exercised against any counterparty.
	RevisionMode string `json:"revision_mode"`
	// revisionModePresent and nextRevisionHeadersPresent record whether the
	// operator document carried each key AT ALL. Absence is not "false": the
	// contradiction rule above can only be honest if an omitted
	// next_revision_headers is distinguishable from an explicit false, and a
	// revision_mode that is present but empty must be refused rather than read as
	// "not configured". UnmarshalJSON is their only writer; a Go composition
	// literal supplies neither, which is exactly the state "not explicitly
	// supplied" and leaves every existing fixture resolving as it always did.
	revisionModePresent        bool
	nextRevisionHeadersPresent bool
	// ambiguousRevisionControls records a revision control the operator document
	// supplied MORE THAN ONCE, in any mix of capitalizations, together with the
	// spellings it used so the refusal can quote the document back. UnmarshalJSON
	// is its only writer; a Go composition literal leaves it empty, which is the
	// state "no document stated anything twice".
	ambiguousRevisionControls []mcpRevisionControl
	// Retrieval enables the in-process governed retrieval upstream: the
	// knowledge module's RAG pipeline exposed as MCP tools (search_kb,
	// fetch_document, list_kbs). When enabled the retrieval tools are merged into
	// the toolset and an in-process upstream replaces (or supplements) the external
	// forwarder. The retrieval scope defaults to "knowledge:retrieval:read".
	Retrieval *RetrievalConfig `json:"retrieval"`
	// DurableTasks binds the optional MCP Tasks extension to the K5 WorkKernel and
	// ProtocolBinding authorities. The route is entirely operator-owned: request
	// metadata can never select a workspace, binding generation, or local owner.
	// Omit this block to keep ordinary synchronous MCP forwarding available while
	// the connector removes Tasks from its advertised capabilities.
	DurableTasks *DurableTasksConfig `json:"durable_tasks"`
	// DurableSubscriptions binds subscriptions/listen to the sessions-backed
	// cursor/event ledger. Omit it to leave the streaming method unavailable
	// (503) while preserving ordinary synchronous MCP forwarding.
	DurableSubscriptions *DurableSubscriptionsConfig `json:"durable_subscriptions"`
}

// The two operator controls whose PRESENCE this composition must read as exactly
// as their VALUE. They are the only member names scanned by name below; every
// other field keeps the decoder's ordinary treatment.
const (
	mcpRevisionModeKey        = "revision_mode"
	mcpNextRevisionHeadersKey = "next_revision_headers"
)

// jsonNullLiteral is the only value both controls treat specially, in opposite
// directions (see UnmarshalJSON).
const jsonNullLiteral = "null"

// mcpRevisionControl collects every appearance of ONE revision control in a
// single operator document, in the order the document wrote them: the spellings
// exactly as typed (a refusal quotes them back, so an operator can find the
// duplicate) and the raw value of each.
type mcpRevisionControl struct {
	name      string
	spellings []string
	values    []json.RawMessage
}

// scanMCPRevisionControls walks the document's top-level members ONCE and
// attributes each to a revision control with strings.EqualFold — the same rule
// encoding/json applies to field names, whose own fold is documented as
// "foldName(x) == foldName(y) is identical to bytes.EqualFold(x, y)". Matching
// the decoder's rule is the point: a control this scan attributes is exactly the
// one the struct decode would have written, so presence can never describe a
// different member than the value does.
//
// It reads values only for those two controls, rejects no member it does not
// know, and imposes no strictness on the rest of the document.
func scanMCPRevisionControls(data []byte) ([]mcpRevisionControl, error) {
	controls := []mcpRevisionControl{{name: mcpRevisionModeKey}, {name: mcpNextRevisionHeadersKey}}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		// Not an object (a bare null reaches the pointer, not this method): there
		// are no member names to attribute, and the alias decode above has already
		// accepted or rejected the document on its own terms.
		return controls, nil
	}
	for dec.More() {
		nameToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, fmt.Errorf("mcp gateway config: unexpected JSON member name %v", nameToken)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		for i := range controls {
			if strings.EqualFold(name, controls[i].name) {
				controls[i].spellings = append(controls[i].spellings, name)
				controls[i].values = append(controls[i].values, value)
			}
		}
	}
	return controls, nil
}

// UnmarshalJSON decodes the operator document exactly as the standard decoder
// always did — the alias type drops this method, so no field changes type,
// validation or strictness — and additionally reads the two revision controls
// from the document's OWN member names. That presence is the structural fact
// the posture needs: without it, "next_revision_headers explicitly set to false" and
// "the operator never mentioned it" are the same value, and the contradiction
// rule cannot be stated, let alone tested.
//
// Independent review ( 2026-09-08) — PRESENCE AND VALUE COME FROM THE SAME
// OCCURRENCE. This method used to read the value through the struct alias, where
// encoding/json matches member names WITHOUT distinguishing case, and the
// presence through a map lookup of two lowercase names. The two readings
// disagreed on every noncanonical spelling, and the disagreement was not
// cosmetic: "REVISION_MODE":null decoded to an empty mode whose presence was
// invisible and therefore resolved LEGACY instead of being refused, and
// "NEXT_REVISION_HEADERS":false contradicted an explicit rc-strict without the
// contradiction rule ever seeing the boolean. The scan above applies the
// decoder's own matching rule, and the two fields are RE-DERIVED from what it
// found rather than inherited from the alias, which is what makes disagreement
// impossible instead of merely unlikely.
//
// A control supplied MORE THAN ONCE is AMBIGUOUS and refuses to mount — including
// two spellings that differ only in case, which are one member name to the
// decoder. The refusal is recorded here and raised in
// ResolveRevisionMode, so it lands where every other revision refusal
// lands (the MCP composition, deny-closed) while the document itself still
// decodes for the blocks this correction does not touch. An ambiguous control
// leaves NO value behind: "last one wins" would publish one of two stated intents
// as the operator's choice, and the two old readings did not even agree on which
// one, since the struct kept the last NON-null value while the map kept the last
// RAW one.
//
// A JSON null is treated differently for the two fields, on purpose:
//
//   - "next_revision_headers": null decodes to false and resolves legacy TODAY.
//     Turning it into a startup refusal would change behavior for an existing
//     field, which this cut may not do, so a SINGLE null is recorded as NOT
//     supplied — in any capitalization.
//   - "revision_mode": null is a new field's explicit non-value, and root's rule
//     rejects it with the empty and whitespace-only cases.
//
// Only these two controls are read this way. Unknown members remain accepted,
// and no other field's decoding, type checking or strictness changes.
func (c *MCPConfig) UnmarshalJSON(data []byte) error {
	type alias MCPConfig
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = MCPConfig(decoded)
	controls, err := scanMCPRevisionControls(data)
	if err != nil {
		return err
	}
	// Both controls start from nothing and are set ONLY from their own
	// occurrence: one source for presence, null and value.
	c.RevisionMode, c.revisionModePresent = "", false
	c.NextRevisionHeaders, c.nextRevisionHeadersPresent = false, false
	c.ambiguousRevisionControls = nil
	for _, control := range controls {
		switch {
		case len(control.values) == 0:
			continue
		case len(control.values) > 1:
			c.ambiguousRevisionControls = append(c.ambiguousRevisionControls, control)
			continue
		}
		raw := bytes.TrimSpace(control.values[0])
		isNull := string(raw) == jsonNullLiteral
		switch control.name {
		case mcpRevisionModeKey:
			// Present even when null: an explicit non-value must be refused, not
			// read back as "not configured".
			c.revisionModePresent = true
			if isNull {
				continue
			}
			if err := json.Unmarshal(raw, &c.RevisionMode); err != nil {
				return err
			}
		case mcpNextRevisionHeadersKey:
			if isNull {
				continue // one null keeps this existing field's absent semantics
			}
			if err := json.Unmarshal(raw, &c.NextRevisionHeaders); err != nil {
				return err
			}
			c.nextRevisionHeadersPresent = true
		}
	}
	return nil
}

// mcpRevisionControlAmbiguity refuses a document that supplied either revision
// control more than once, naming the control, how many times, and the spellings
// it used. It is a COMPOSITION refusal rather than a JSON parse error on purpose:
// a duplicated MCP control takes the MCP surface down deny-closed and decides
// nothing about the other blocks the same operator document provisions.
func mcpRevisionControlAmbiguity(cfg *MCPConfig) error {
	if len(cfg.ambiguousRevisionControls) == 0 {
		return nil
	}
	stated := make([]string, 0, len(cfg.ambiguousRevisionControls))
	for _, control := range cfg.ambiguousRevisionControls {
		quoted := make([]string, 0, len(control.spellings))
		for _, spelling := range control.spellings {
			quoted = append(quoted, strconv.Quote(spelling))
		}
		stated = append(stated, fmt.Sprintf("%s is supplied %d times (as %s)",
			control.name, len(control.spellings), strings.Join(quoted, ", ")))
	}
	return fmt.Errorf(
		"mcp gateway config: %s; JSON member names are matched without distinguishing case, so which value the operator meant is ambiguous — supply each control exactly once",
		strings.Join(stated, " and "))
}

// The explicit gateway revision modes. They MUST be the same three strings
// connectors/mcp implements (rsconfig.go:402-404, unexported there); a divergence
// fails closed rather than silently, because the value this file validates is
// handed to the connector, whose own resolver rejects anything it does not know.
const (
	RevisionModeLegacy   = "legacy"
	RevisionModeDual     = "dual"
	RevisionModeRCStrict = "rc-strict"
)

// mcpGatewayRevisionModes lists the accepted values in the order every refusal
// names them.
var mcpGatewayRevisionModes = []string{
	RevisionModeLegacy, RevisionModeDual, RevisionModeRCStrict,
}

// mcpRevisionModeHeaderPosture reports whether a mode runs the 2026-07-28 L7
// header gate. It is the ONLY thing that makes the two operator fields
// comparable: next_revision_headers has always meant "that gate is on".
func mcpRevisionModeHeaderPosture(mode string) (headersOn, known bool) {
	switch mode {
	case RevisionModeLegacy:
		return false, true
	case RevisionModeDual, RevisionModeRCStrict:
		return true, true
	default:
		return false, false
	}
}

// ResolveRevisionMode validates the operator's revision pair and returns
// the EXPLICIT mode to hand to connectors/mcp, or "" when the operator stated
// none.
//
// "" is not a default invented here: it is precisely the value that leaves
// resolveResourceServerRevisionMode (connectors/mcp/rsconfig.go:438-452) resolving
// from DisableNextRevisionHeaders exactly as it did before this field existed.
// There is no second resolver in this file and no second protocol implementation —
// only the validation the composition owes an operator BEFORE its surface serves,
// which the connector cannot perform because the contradictory pair is a gateway
// config shape the connector never sees.
func ResolveRevisionMode(cfg *MCPConfig) (string, error) {
	// R1: a control the document stated twice is refused BEFORE anything reads
	// its value — there is no value to read that would not be a guess.
	if err := mcpRevisionControlAmbiguity(cfg); err != nil {
		return "", err
	}
	accepted := strings.Join(mcpGatewayRevisionModes, ", ")
	if !cfg.revisionModePresent && cfg.RevisionMode == "" {
		return "", nil // absent: the connector resolves from next_revision_headers, unchanged
	}
	mode := strings.TrimSpace(cfg.RevisionMode)
	if mode == "" {
		return "", fmt.Errorf(
			"mcp gateway config: revision_mode is present but empty; omit the field to keep the next_revision_headers resolution, or set one of: %s",
			accepted)
	}
	headersOn, known := mcpRevisionModeHeaderPosture(mode)
	if !known {
		return "", fmt.Errorf("mcp gateway config: unknown revision_mode %q (accepted: %s)", mode, accepted)
	}
	if cfg.nextRevisionHeadersPresent && cfg.NextRevisionHeaders != headersOn {
		return "", fmt.Errorf(
			"mcp gateway config: revision_mode %q and next_revision_headers %t contradict each other; %q requires next_revision_headers %t, so set it to %t or omit it",
			mode, cfg.NextRevisionHeaders, mode, headersOn, headersOn)
	}
	return mode, nil
}

// EffectiveConfig is the secret-free read-back of what this gateway was
// CONFIGURED with, emitted once per mounted Resource Server. Every field is a fact
// the composition can state about itself: the revision mode the Resource Server
// resolved, which operator field selected it, and whether each durable seam was
// WIRED.
//
// None of it is a readiness, conformance or interoperability claim. A wired seam
// has not been exercised, an absent seam is a configuration fact and not a
// failure, and no counterparty has been contacted to produce any of these values.
type EffectiveConfig struct {
	RevisionMode         string
	RevisionModeSource   string
	Upstream             string
	SubscriptionUpstream string
	SubscriptionLedger   string
	DurableTaskStore     string
}

// RevisionModeSource names the operator field that selected the mode.
func RevisionModeSource(cfg *MCPConfig, explicit string) string {
	switch {
	case explicit != "":
		return "revision_mode"
	case cfg.nextRevisionHeadersPresent || cfg.NextRevisionHeaders:
		return "next_revision_headers"
	default:
		return "default"
	}
}

// SeamState renders a seam as CONFIGURED or ABSENT — never as ready, healthy
// or available, none of which this process has measured.
func SeamState(wired bool) string {
	if wired {
		return "configured"
	}
	return "absent"
}

// UpstreamKind reduces the upstream descriptor to its KIND. The descriptor
// carries the configured URL and a URL can carry userinfo credentials, so the
// read-back names the kind and never the address.
func UpstreamKind(descriptor string) string {
	switch {
	case descriptor == "":
		return "absent"
	case strings.HasPrefix(descriptor, "in-process:"):
		return descriptor // this form is a fixed label, with no address in it
	case strings.HasPrefix(descriptor, "https-forward:"):
		return "https-forward"
	default:
		return "configured"
	}
}

// LogEffectiveConfig emits the read-back on the existing startup log
// path, once, at Info. The message says what the record is and what it is not,
// because a line listing wired seams is exactly the shape a reader mistakes for a
// health check.
func LogEffectiveConfig(log *slog.Logger, e EffectiveConfig) {
	if log == nil {
		return
	}
	log.Info("mcp gateway: effective configuration read-back (CONFIGURED posture only — not a readiness, conformance or interoperability claim; no seam below has been exercised)",
		"revision_mode", e.RevisionMode,
		"revision_mode_source", e.RevisionModeSource,
		"upstream", e.Upstream,
		"subscription_upstream", e.SubscriptionUpstream,
		"subscription_ledger", e.SubscriptionLedger,
		"durable_task_store", e.DurableTaskStore,
	)
}

// RetrievalConfig enables the in-process governed retrieval MCP surface.
type RetrievalConfig struct {
	Enabled bool   `json:"enabled"`
	Scope   string `json:"scope"` // OAuth scope for retrieval tools (default "knowledge:retrieval:read")
}

// DurableTasksConfig is the JSON-facing local route for MCP durable tasks.
// Tenant comes from the enclosing MCP Resource Server configuration so the
// authentication, evidence, work, and protocol-binding namespaces stay identical.
type DurableTasksConfig struct {
	WorkspaceID                  string   `json:"workspace_id"`
	BindingSpecID                string   `json:"binding_spec_id"`
	BindingSpecGeneration        int64    `json:"binding_spec_generation"`
	OwnerKind                    string   `json:"owner_kind"`
	OwnerRef                     string   `json:"owner_ref"`
	ProtocolRuleRefs             []string `json:"protocol_rule_refs"`
	ProtocolPermissionProfileRef string   `json:"protocol_permission_profile_ref"`
	InterruptChannelID           string   `json:"interrupt_channel_id"`
	InterruptSenderUserID        string   `json:"interrupt_sender_user_id"`
	InterruptRecipientUserID     string   `json:"interrupt_recipient_user_id"`
}

// DurableSubscriptionsConfig fixes the local workspace of every relayed
// stream. Tenant comes from the enclosing Resource Server and peer authority
// comes from upstream_url; neither can be supplied by a listen request.
type DurableSubscriptionsConfig struct {
	WorkspaceID string `json:"workspace_id"`
}

// IssuerTrust is one trusted token issuer for the MCP PEP: the EXACT iss value
// (the lookup key — compared byte-for-byte, no normalization) plus that issuer's own
// trust anchors and the RS's own credential at that issuer's introspection endpoint.
type IssuerTrust struct {
	Issuer            string          `json:"issuer"`
	IssuerJWKS        json.RawMessage `json:"issuer_jwks"`
	JWKSURL           string          `json:"jwks_url"`
	IntrospectionURL  string          `json:"introspection_url"`
	IntrospectionAuth string          `json:"introspection_auth"` // secret: per-issuer RS credential
}

// A2APushConfig provisions the inbound A2A push-notification receiver.
type A2APushConfig struct {
	Audience       string               `json:"audience"`
	IssuerJWKS     json.RawMessage      `json:"issuer_jwks"`
	JWKSURL        string               `json:"jwks_url"`
	AllowedIssuers []string             `json:"allowed_issuers"`
	Routes         []A2APushRouteConfig `json:"routes"`
}

type A2APushRouteConfig struct {
	PeerAuthority            string `json:"peer_authority"`
	Tenant                   string `json:"tenant"`
	WorkspaceID              string `json:"workspace_id"`
	InterruptChannelID       string `json:"interrupt_channel_id"`
	InterruptSenderUserID    string `json:"interrupt_sender_user_id"`
	InterruptRecipientUserID string `json:"interrupt_recipient_user_id"`
}

// A2AInboundConfig provisions the authenticated A2A SendMessage application
// endpoint. Routes are operator-owned local authority: a peer-supplied tenant,
// owner or WorkItem reference is never accepted as a routing decision.
type A2AInboundConfig struct {
	Audience                 string                  `json:"audience"`
	IssuerJWKS               json.RawMessage         `json:"issuer_jwks"`
	JWKSURL                  string                  `json:"jwks_url"`
	AllowedIssuers           []string                `json:"allowed_issuers"`
	InterfaceTenant          string                  `json:"interface_tenant"`
	RequireClientAttestation bool                    `json:"require_client_attestation"`
	AttesterJWKS             json.RawMessage         `json:"attester_jwks"`
	Routes                   []A2AInboundRouteConfig `json:"routes"`
}

type A2AInboundRouteConfig struct {
	PeerAuthority                string   `json:"peer_authority"`
	Tenant                       string   `json:"tenant"`
	WorkspaceID                  string   `json:"workspace_id"`
	BindingSpecID                string   `json:"binding_spec_id"`
	BindingSpecGeneration        int64    `json:"binding_spec_generation"`
	ChannelID                    string   `json:"channel_id"`
	SenderUserID                 string   `json:"sender_user_id"`
	RecipientUserID              string   `json:"recipient_user_id"`
	OwnerKind                    string   `json:"owner_kind"`
	OwnerRef                     string   `json:"owner_ref"`
	WorkKind                     string   `json:"work_kind"`
	Priority                     string   `json:"priority"`
	ProtocolRuleRefs             []string `json:"protocol_rule_refs"`
	ProtocolPermissionProfileRef string   `json:"protocol_permission_profile_ref"`
}

// DefaultListen is the loopback-default bind (secure default): an
// operator that must receive remote MCP/A2A traffic fronts it with their ingress.
const DefaultListen = "127.0.0.1:8446"
