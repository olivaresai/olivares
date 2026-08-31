// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import "strconv"

// cardnorm.go implements the A2A v1.0 pre-canonicalization normalization the signed
// Agent Card procedure requires (spec §8.4.1 rule 1 + §5.7, formalized in v1.0 —
// v0.3.0 had the signatures field with NO normative procedure). Before JCS (RFC
// 8785) is applied, the card JSON MUST reflect protobuf field-presence semantics:
//
//   - REQUIRED fields (google.api.field_behavior=REQUIRED) are always kept, even at
//     their default value (the spec's worked example keeps `"description":""` and
//     `"skills":[]`).
//   - explicit-presence fields (proto3 `optional` scalars and every message-typed
//     field) are kept whenever present — their presence in the received JSON IS the
//     "explicitly set" signal (e.g. `"streaming":false` stays).
//   - implicit-presence fields (plain scalars, repeated, map) at their default value
//     ("", false, 0, [], {}) MUST be omitted from the signed content.
//   - unknown properties are kept verbatim: they cannot be classified against the
//     schema, and a signer that included them signed them as sent (forward compat,
//     §5.7 "SHOULD ignore unrecognized fields").
//
// The verification procedure (§8.4.3 step 3: "Remove properties with default values
// from the received Agent Card") applies the same transform on the verifier side, so
// the schema below is transcribed from the canonical proto (a2aproject/A2A
// specification/a2a.proto, tag v1.0.1 — byte-identical to v1.0.0) for the AgentCard
// message tree. google.protobuf.Struct fields (extension params, signature header)
// are opaque set values: kept verbatim, contents untouched.
//
// The transform is semantics-preserving (a default-valued implicit-presence field is
// indistinguishable from an absent one in proto3), so verifying against either the
// normalized or the literal form binds the same card content — verify.go tries the
// normalized form first (the v1.0 normative procedure) and falls back to the literal
// form for signers predating the formalization (the behavior). Fail-safe: a
// signature that verifies under neither form stays UNTRUSTED.

// fieldRule classifies one known field of a card message for normalization.
type fieldRule struct {
	required bool       // field_behavior=REQUIRED: always kept
	explicit bool       // proto3 `optional` scalar or message-typed: presence == set
	msg      *msgSchema // schema of a message-typed value (recurse), nil for scalars/Struct
	elem     *msgSchema // schema of repeated-message elements or map<string,message> values
}

// msgSchema is the known-field map of one proto message.
type msgSchema struct {
	fields map[string]fieldRule
}

// Schemas transcribed from a2a.proto v1.0.1 (AgentCard message tree). Declared as
// vars wired in init() because the tree is cyclic-free but cross-referencing.
var (
	agentCardSchema          = &msgSchema{}
	agentInterfaceSchema     = &msgSchema{}
	agentProviderSchema      = &msgSchema{}
	agentCapabilitiesSchema  = &msgSchema{}
	agentExtensionSchema     = &msgSchema{}
	agentSkillSchema         = &msgSchema{}
	securityRequirementSch   = &msgSchema{}
	stringListSchema         = &msgSchema{}
	securitySchemeSchema     = &msgSchema{}
	apiKeySchemeSchema       = &msgSchema{}
	httpAuthSchemeSchema     = &msgSchema{}
	oauth2SchemeSchema       = &msgSchema{}
	openIDConnectSchema      = &msgSchema{}
	mtlsSchemeSchema         = &msgSchema{}
	oauthFlowsSchema         = &msgSchema{}
	authzCodeFlowSchema      = &msgSchema{}
	clientCredsFlowSchema    = &msgSchema{}
	deviceCodeFlowSchema     = &msgSchema{}
	implicitFlowSchema       = &msgSchema{}
	passwordFlowSchema       = &msgSchema{}
	agentCardSignatureSchema = &msgSchema{}
)

func init() {
	agentCardSchema.fields = map[string]fieldRule{
		"name":                {required: true},
		"description":         {required: true},
		"supportedInterfaces": {required: true, elem: agentInterfaceSchema},
		"provider":            {explicit: true, msg: agentProviderSchema},
		"version":             {required: true},
		"documentationUrl":    {explicit: true},
		"capabilities":        {required: true, msg: agentCapabilitiesSchema},
		"securitySchemes":     {elem: securitySchemeSchema}, // map<string, SecurityScheme>
		"securityRequirements": {
			elem: securityRequirementSch,
		},
		"defaultInputModes":  {required: true},
		"defaultOutputModes": {required: true},
		"skills":             {required: true, elem: agentSkillSchema},
		"signatures":         {elem: agentCardSignatureSchema},
		"iconUrl":            {explicit: true},
	}
	agentInterfaceSchema.fields = map[string]fieldRule{
		"url":             {required: true},
		"protocolBinding": {required: true},
		"tenant":          {},
		"protocolVersion": {required: true},
	}
	agentProviderSchema.fields = map[string]fieldRule{
		"url":          {required: true},
		"organization": {required: true},
	}
	agentCapabilitiesSchema.fields = map[string]fieldRule{
		"streaming":         {explicit: true},
		"pushNotifications": {explicit: true},
		"extensions":        {elem: agentExtensionSchema},
		"extendedAgentCard": {explicit: true},
	}
	agentExtensionSchema.fields = map[string]fieldRule{
		"uri":         {},
		"description": {},
		"required":    {},
		"params":      {explicit: true}, // google.protobuf.Struct: opaque, kept verbatim
	}
	agentSkillSchema.fields = map[string]fieldRule{
		"id":                   {required: true},
		"name":                 {required: true},
		"description":          {required: true},
		"tags":                 {required: true},
		"examples":             {},
		"inputModes":           {},
		"outputModes":          {},
		"securityRequirements": {elem: securityRequirementSch},
	}
	securityRequirementSch.fields = map[string]fieldRule{
		"schemes": {elem: stringListSchema}, // map<string, StringList>
	}
	stringListSchema.fields = map[string]fieldRule{
		"list": {},
	}
	securitySchemeSchema.fields = map[string]fieldRule{ // proto oneof: members are message-typed
		"apiKeySecurityScheme":        {explicit: true, msg: apiKeySchemeSchema},
		"httpAuthSecurityScheme":      {explicit: true, msg: httpAuthSchemeSchema},
		"oauth2SecurityScheme":        {explicit: true, msg: oauth2SchemeSchema},
		"openIdConnectSecurityScheme": {explicit: true, msg: openIDConnectSchema},
		"mtlsSecurityScheme":          {explicit: true, msg: mtlsSchemeSchema},
	}
	apiKeySchemeSchema.fields = map[string]fieldRule{
		"description": {},
		"location":    {required: true},
		"name":        {required: true},
	}
	httpAuthSchemeSchema.fields = map[string]fieldRule{
		"description":  {},
		"scheme":       {required: true},
		"bearerFormat": {},
	}
	oauth2SchemeSchema.fields = map[string]fieldRule{
		"description":       {},
		"flows":             {required: true, msg: oauthFlowsSchema},
		"oauth2MetadataUrl": {},
	}
	openIDConnectSchema.fields = map[string]fieldRule{
		"description":      {},
		"openIdConnectUrl": {required: true},
	}
	mtlsSchemeSchema.fields = map[string]fieldRule{
		"description": {},
	}
	oauthFlowsSchema.fields = map[string]fieldRule{ // proto oneof: members are message-typed
		"authorizationCode": {explicit: true, msg: authzCodeFlowSchema},
		"clientCredentials": {explicit: true, msg: clientCredsFlowSchema},
		"implicit":          {explicit: true, msg: implicitFlowSchema},
		"password":          {explicit: true, msg: passwordFlowSchema},
		"deviceCode":        {explicit: true, msg: deviceCodeFlowSchema},
	}
	authzCodeFlowSchema.fields = map[string]fieldRule{
		"authorizationUrl": {required: true},
		"tokenUrl":         {required: true},
		"refreshUrl":       {},
		"scopes":           {required: true}, // map<string,string>
		"pkceRequired":     {},
	}
	clientCredsFlowSchema.fields = map[string]fieldRule{
		"tokenUrl":   {required: true},
		"refreshUrl": {},
		"scopes":     {required: true},
	}
	deviceCodeFlowSchema.fields = map[string]fieldRule{
		"deviceAuthorizationUrl": {required: true},
		"tokenUrl":               {required: true},
		"refreshUrl":             {},
		"scopes":                 {required: true},
	}
	implicitFlowSchema.fields = map[string]fieldRule{ // deprecated flow: no REQUIRED fields
		"authorizationUrl": {},
		"refreshUrl":       {},
		"scopes":           {},
	}
	passwordFlowSchema.fields = map[string]fieldRule{ // deprecated flow: no REQUIRED fields
		"tokenUrl":   {},
		"refreshUrl": {},
		"scopes":     {},
	}
	agentCardSignatureSchema.fields = map[string]fieldRule{
		"protected": {required: true},
		"signature": {required: true},
		"header":    {explicit: true}, // google.protobuf.Struct: opaque
	}
}

// normalizeCard returns a copy of the generic card map with proto-default-valued,
// implicit-presence properties removed per §8.4.1/§5.7 (see file header). The input
// map is never mutated.
func normalizeCard(raw map[string]any) map[string]any {
	return normalizeObject(raw, agentCardSchema)
}

// normalizeObject normalizes one JSON object against a message schema. Unknown
// properties are kept verbatim.
func normalizeObject(obj map[string]any, schema *msgSchema) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		rule, known := schema.fields[k]
		if !known {
			out[k] = v
			continue
		}
		keep := rule.required || rule.explicit || !isProtoDefault(v)
		if !keep {
			continue
		}
		out[k] = normalizeValue(v, rule)
	}
	return out
}

// normalizeValue recurses into a kept value: message values normalize against their
// schema; repeated-message / map<string,message> values normalize each element
// (array order preserved — JCS never reorders arrays); everything else (scalars and
// opaque Struct content) passes through verbatim.
func normalizeValue(v any, rule fieldRule) any {
	if rule.msg != nil {
		if obj, ok := v.(map[string]any); ok {
			return normalizeObject(obj, rule.msg)
		}
		return v
	}
	if rule.elem == nil {
		return v
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			if obj, ok := e.(map[string]any); ok {
				out[i] = normalizeObject(obj, rule.elem)
			} else {
				out[i] = e
			}
		}
		return out
	case map[string]any: // map<string, message>
		out := make(map[string]any, len(x))
		for k, e := range x {
			if obj, ok := e.(map[string]any); ok {
				out[k] = normalizeObject(obj, rule.elem)
			} else {
				out[k] = e
			}
		}
		return out
	default:
		return v
	}
}

// isProtoDefault reports whether a decoded JSON value is the proto3 default for an
// implicit-presence field: "" (string), false (bool), 0 (number), [] (repeated),
// {} (map/message), or null. Numbers arrive as json.Number (decodeGeneric uses
// UseNumber); the float64 case covers a non-UseNumber decode defensively.
func isProtoDefault(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case bool:
		return !x
	case jsonNumberLike:
		f, err := strconv.ParseFloat(x.String(), 64)
		return err == nil && f == 0
	case float64:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}
