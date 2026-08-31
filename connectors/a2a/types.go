// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
)

// AgentCard is the governance-relevant subset of an A2A v1.0 Agent Card. The full
// card is also kept as a generic map (for JCS canonicalization during signature
// verification — see verify.go); this typed view is for field extraction.
//
// Field set verified against the canonical proto (a2aproject/A2A specification/
// a2a.proto, tag v1.0.1 — the spec's single normative data model, §1.4): name,
// description, supportedInterfaces, provider, version, documentationUrl,
// capabilities, securitySchemes, securityRequirements, defaultInputModes/
// OutputModes, skills, signatures, iconUrl. The v0.x top-level url /
// protocolVersion / preferredTransport fields NO LONGER EXIST in v1.0 (replaced by
// supportedInterfaces[]); URL/ProtocolVersion are kept here ONLY as a lenient-parse
// fallback for pre-1.0 cards and are never emitted.
type AgentCard struct {
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	Version             string                    `json:"version"`
	SupportedInterfaces []AgentInterface          `json:"supportedInterfaces"`
	Provider            *agentProvider            `json:"provider"`
	Capabilities        *AgentCapabilities        `json:"capabilities"`
	SecuritySchemes     map[string]securityScheme `json:"securitySchemes"`
	Skills              []agentSkill              `json:"skills"`
	Signatures          []AgentCardSignature      `json:"signatures"`

	// URL / ProtocolVersion are the REMOVED v0.x top-level fields, read only as a
	// fallback for a peer still serving a pre-1.0 card (lenient parse, §5.7 spirit).
	// A v1.0 card resolves its endpoint via SupportedInterfaces (jsonrpcInterface).
	URL             string `json:"url"`
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentInterface is one A2A v1.0 supported interface (spec §4.4.6): the URL where a
// given protocol binding is served. The list is preference-ordered (first entry
// preferred, §8.3.1); protocolBinding is an open string whose core values are
// exactly "JSONRPC", "GRPC" and "HTTP+JSON"; tenant is an opaque routing id the
// client MUST echo in the `tenant` field of every request to that interface;
// protocolVersion is the Major.Minor protocol version served there.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	Tenant          string `json:"tenant"`
	ProtocolVersion string `json:"protocolVersion"`
}

// bindingJSONRPC is the core protocolBinding identifier for the JSON-RPC 2.0
// binding this client speaks (spec §4.4.6).
const bindingJSONRPC = "JSONRPC"

// jsonrpcInterface selects the agent's JSON-RPC service interface per the client
// rules of §8.3.2: walk supportedInterfaces in declared (preference) order and pick
// the FIRST entry whose binding is JSONRPC and whose protocolVersion is a major
// version this client speaks (1.x; an empty version is tolerated as lenient parse).
// ok=false when the card declares interfaces but none we can speak — the caller
// must NOT fall back to another binding's URL (acting outside the declared surface).
func (c AgentCard) jsonrpcInterface() (AgentInterface, bool) {
	for _, it := range c.SupportedInterfaces {
		if strings.TrimSpace(it.ProtocolBinding) != bindingJSONRPC {
			continue
		}
		v := strings.TrimSpace(it.ProtocolVersion)
		if v == "" || v == "1" || strings.HasPrefix(v, "1.") {
			if strings.TrimSpace(it.URL) != "" {
				return it, true
			}
		}
	}
	return AgentInterface{}, false
}

// AgentCapabilities is the A2A v1.0 AgentCapabilities object: the OPTIONAL protocol
// features an agent advertises in its (signed) card. The connector reads it to bind a
// delegation to the agent's CRYPTOGRAPHICALLY-DECLARED capability surface (capability.go):
// you may not stream to an agent whose signed card does not advertise streaming, nor
// configure push on one that does not advertise pushNotifications, nor request the
// extended card unless extendedAgentCard is set (the v1.0 rename of the v0.x top-level
// supportsAuthenticatedExtendedCard). Field set verified against a2a.proto v1.0.1
// (streaming, push_notifications, extensions, extended_agent_card; the v0.x
// stateTransitionHistory was removed). Extensions is the list of protocol extensions
// the agent declares (URI + required flag); only the URI + required flag are
// inventoried (minimal data — never an extension's params).
type AgentCapabilities struct {
	Streaming         bool             `json:"streaming"`
	PushNotifications bool             `json:"pushNotifications"`
	ExtendedAgentCard bool             `json:"extendedAgentCard"`
	Extensions        []agentExtension `json:"extensions"`
}

// agentExtension is one declared A2A protocol extension (its URI + whether the agent
// requires a caller to honor it). Only the URI/required flag are read.
type agentExtension struct {
	URI         string `json:"uri"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

// declaresSkill reports whether the card's signed skill set declares a skill matching
// ref (by id, the canonical identifier used in messages, OR by name — both come from
// the signed card, so either is a cryptographically-claimed capability). A blank ref or
// a card with no skills never matches. The match is trim-normalized and case-sensitive
// (skill ids are case-sensitive references).
func (c AgentCard) declaresSkill(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	for _, s := range c.Skills {
		if strings.TrimSpace(s.ID) == ref || strings.TrimSpace(s.Name) == ref {
			return true
		}
	}
	return false
}

// skillRefs returns the declared skill identifiers (id preferred, name fallback) for
// catalog/discovery reporting — minimal data, sorted+deduped, no descriptions.
func (c AgentCard) skillRefs() []string {
	if len(c.Skills) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range c.Skills {
		ref := strings.TrimSpace(s.ID)
		if ref == "" {
			ref = strings.TrimSpace(s.Name)
		}
		if ref != "" {
			seen[ref] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// capabilityLabels returns the sorted set of advertised capability labels of the signed
// card (streaming / push / an extensions count), for the discovery finding. Absent
// capabilities yield an empty slice (an agent that advertises none).
func (c AgentCard) capabilityLabels() []string {
	if c.Capabilities == nil {
		return nil
	}
	var out []string
	if c.Capabilities.Streaming {
		out = append(out, "streaming")
	}
	if c.Capabilities.PushNotifications {
		out = append(out, "push")
	}
	if c.Capabilities.ExtendedAgentCard {
		out = append(out, "extended-card")
	}
	for _, e := range c.Capabilities.Extensions {
		// The extension URI comes from the REMOTE agent's card (UNTRUSTED) and flows into
		// a "safe to display" finding title (capabilityFinding) — scrub secret shapes so a
		// hostile card cannot smuggle a token-shaped value into a displayed/persisted title.
		if uri := strings.TrimSpace(redact.Clean(e.URI)); uri != "" {
			label := "ext:" + uri
			if e.Required {
				label += "(required)"
			}
			out = append(out, label)
		}
	}
	sort.Strings(out)
	return out
}

// agentProvider identifies the organization operating the agent.
type agentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url"`
}

// securityScheme is one declared auth scheme. In A2A v1.0 SecurityScheme is a proto
// ONEOF discriminated by JSON member name (a2a.proto v1.0.1: apiKeySecurityScheme /
// httpAuthSecurityScheme / oauth2SecurityScheme / openIdConnectSecurityScheme /
// mtlsSecurityScheme) — NOT by the v0.x OpenAPI-style `type` string, which is kept
// only as a lenient-parse fallback for pre-1.0 cards. The connector inventories
// WHICH schemes a peer requires and their OAuth hygiene posture (flows declared,
// PKCE), never credentials.
type securityScheme struct {
	APIKey        *apiKeyScheme        `json:"apiKeySecurityScheme"`
	HTTPAuth      *httpAuthScheme      `json:"httpAuthSecurityScheme"`
	OAuth2        *oauth2Scheme        `json:"oauth2SecurityScheme"`
	OpenIDConnect *openIDConnectScheme `json:"openIdConnectSecurityScheme"`
	MutualTLS     *mtlsScheme          `json:"mtlsSecurityScheme"`

	// Type is the REMOVED v0.x OpenAPI-style discriminator (lenient-parse fallback).
	Type string `json:"type"`
}

// kindLabel returns the canonical scheme-kind label for inventory findings: the v1.0
// oneof member when present, the legacy v0.x type otherwise, "" when neither.
func (s securityScheme) kindLabel() string {
	switch {
	case s.APIKey != nil:
		return "apiKey"
	case s.HTTPAuth != nil:
		return "http"
	case s.OAuth2 != nil:
		return "oauth2"
	case s.OpenIDConnect != nil:
		return "openIdConnect"
	case s.MutualTLS != nil:
		return "mutualTLS"
	default:
		return strings.TrimSpace(s.Type)
	}
}

// apiKeyScheme / httpAuthScheme / openIDConnectScheme / mtlsScheme carry no fields
// the connector needs beyond presence (the oneof member IS the kind); they exist as
// distinct types so a future field read stays typed.
type (
	apiKeyScheme        struct{}
	httpAuthScheme      struct{}
	openIDConnectScheme struct{}
	mtlsScheme          struct{}
)

// oauth2Scheme is the v1.0 OAuth2SecurityScheme: the declared flows (a proto oneof,
// discriminated by member presence) plus the RFC 8414 metadata URL. Read for the
// OAuth-hygiene inventory: implicit/password are DEPRECATED in v1.0 ("Use
// Authorization Code + PKCE instead" / "... or Device Code", a2a.proto), and an
// authorization-code flow advertises whether it requires PKCE (RFC 7636).
type oauth2Scheme struct {
	Flows             *oauthFlows `json:"flows"`
	OAuth2MetadataURL string      `json:"oauth2MetadataUrl"`
}

// oauthFlows is the v1.0 OAuthFlows oneof; exactly one member is set.
type oauthFlows struct {
	AuthorizationCode *authorizationCodeFlow `json:"authorizationCode"`
	ClientCredentials *json.RawMessage       `json:"clientCredentials"`
	Implicit          *json.RawMessage       `json:"implicit"` // deprecated in v1.0
	Password          *json.RawMessage       `json:"password"` // deprecated in v1.0
	DeviceCode        *json.RawMessage       `json:"deviceCode"`
}

// authorizationCodeFlow carries the hygiene-relevant PKCE flag (RFC 7636).
type authorizationCodeFlow struct {
	PKCERequired bool `json:"pkceRequired"`
}

// agentSkill is one skill an agent exposes (id/name only; descriptions are not
// inventoried as edges — minimal data).
type agentSkill struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AgentCardSignature is one JWS signature over the card (RFC 7515), in the A2A
// AgentCardSignature shape: a DETACHED JWS — protected header + signature, no
// inline payload (the payload is the JCS-canonical card minus the signatures
// field, reconstructed at verification time, verify.go). header is the optional
// JWS unprotected header.
type AgentCardSignature struct {
	Protected string         `json:"protected"`
	Signature string         `json:"signature"`
	Header    map[string]any `json:"header"`
}

// securitySchemeTypes is the set of canonical scheme-kind labels (the five v1.0
// oneof members; the same vocabulary the v0.x `type` field used), for honest
// reporting (an unrecognized legacy type is surfaced as "other", never dropped).
var securitySchemeTypes = map[string]struct{}{
	"apiKey": {}, "http": {}, "oauth2": {}, "openIdConnect": {}, "mutualTLS": {},
}

// A2A v1.0 Message.role wire values (ProtoJSON enum serialization, spec §5.5 /
// ADR-001): the v0.x "user"/"agent" strings became ROLE_USER/ROLE_AGENT.
const (
	roleUser  = "ROLE_USER"
	roleAgent = "ROLE_AGENT"
)

// TaskState is an A2A v1.0 task lifecycle state. v1.0 serializes the enum as
// ProtoJSON SCREAMING_SNAKE_CASE strings (a BREAKING change from the v0.x
// kebab-case forms) — verified against the spec + whats-new-v1. The connector
// matches on these canonical values.
type TaskState string

const (
	TaskStateUnspecified  TaskState = "TASK_STATE_UNSPECIFIED"
	TaskStateSubmitted    TaskState = "TASK_STATE_SUBMITTED"
	TaskStateWorking      TaskState = "TASK_STATE_WORKING"
	TaskStateInputReq     TaskState = "TASK_STATE_INPUT_REQUIRED"
	TaskStateAuthRequired TaskState = "TASK_STATE_AUTH_REQUIRED"
	TaskStateCompleted    TaskState = "TASK_STATE_COMPLETED"
	TaskStateCanceled     TaskState = "TASK_STATE_CANCELED"
	TaskStateFailed       TaskState = "TASK_STATE_FAILED"
	TaskStateRejected     TaskState = "TASK_STATE_REJECTED"
)

// knownTaskStates is the v1.0 enum set. A value outside it is surfaced as an
// unrecognized state rather than dropped (honest observation).
var knownTaskStates = map[TaskState]struct{}{
	TaskStateUnspecified: {}, TaskStateSubmitted: {}, TaskStateWorking: {},
	TaskStateInputReq: {}, TaskStateAuthRequired: {}, TaskStateCompleted: {},
	TaskStateCanceled: {}, TaskStateFailed: {}, TaskStateRejected: {},
}

// known reports whether s is a recognized v1.0 task state.
func (s TaskState) known() bool {
	_, ok := knownTaskStates[s]
	return ok
}

// notable reports whether a task state warrants its own lifecycle finding (an
// input/auth request or a non-success terminal state); the happy path
// (submitted/working/completed) is normal and emits only the edge.
func (s TaskState) notable() bool {
	switch s {
	case TaskStateFailed, TaskStateRejected, TaskStateAuthRequired, TaskStateInputReq:
		return true
	default:
		return false
	}
}

// rawCard decodes card bytes into BOTH the typed AgentCard and the generic map the
// JCS canonicalizer needs (numbers preserved via the decoder's UseNumber in
// decodeGeneric, discover.go). raw is nil when the bytes are not a JSON object.
type rawCard struct {
	card AgentCard
	raw  map[string]any
}

// decodeCardTyped unmarshals the typed view (lenient: unknown fields ignored).
func decodeCardTyped(data []byte) (AgentCard, error) {
	var c AgentCard
	err := json.Unmarshal(data, &c)
	return c, err
}
