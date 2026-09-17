// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const sessionsCommunicationFamily = "sessions-communication-v1"

// authCapabilitiesFamily is the SELF capability projection. It shares this file's
// machinery — one language-neutral catalog and the four emitters — because the whole
// point of that machinery is that a typed family does not get its own emitters. Only the
// name mapping below is family-specific.
const authCapabilitiesFamily = "auth-capabilities-v1"

func (o Operation) sessionsCommunicationTyped() bool {
	return typedSDKFamilies[o.SDKFamily]
}

func documentHasSessionsCommunication(doc *Document) bool {
	return doc.sessionsCommunicationSchemas != nil && len(doc.sessionsCommunicationSchemas.Types) > 0
}

func (o Operation) sessionsCommunicationBodyType() string {
	switch o.Method + " " + o.Path {
	case "POST /v1/auth/capabilities":
		return "AuthCapabilityQuestions"
	case "POST /v1/m/sessions/channels":
		return "SessionsCommunicationChannelCreateBody"
	case "PATCH /v1/m/sessions/channels":
		return "SessionsCommunicationChannelUpdateBody"
	case "POST /v1/m/sessions/channels/{id}/grants":
		return "SessionsCommunicationChannelGrantInput"
	case "POST /v1/m/sessions/messages/send":
		return "SessionsCommunicationMessageSendBody"
	case "PUT /v1/m/sessions/inbox/cursors/personal/{recipient}":
		return "SessionsCommunicationCursorAdvanceBody"
	case "POST /v1/m/sessions/handoffs":
		return "SessionsCommunicationHandoffOfferBody"
	case "POST /v1/m/sessions/handoffs/{id}/responses":
		return "SessionsCommunicationHandoffResponseBody"
	default:
		return ""
	}
}

func (o Operation) sessionsCommunicationResultType() string {
	switch o.Method + " " + o.Path {
	case "POST /v1/auth/capabilities":
		return "AuthCapabilityResults"
	case "POST /v1/m/sessions/channels", "PATCH /v1/m/sessions/channels",
		"POST /v1/m/sessions/channels/{id}/grants",
		"POST /v1/m/sessions/channels/{id}/grants/{grant_id}/revoke":
		return "SessionsCommunicationChannelMutationResult"
	case "GET /v1/m/sessions/channels":
		return "SessionsCommunicationChannelCatalogPage"
	case "GET /v1/m/sessions/channels/administration":
		return "SessionsCommunicationChannelAdministrationPage"
	case "GET /v1/m/sessions/channels/{id}/grants":
		return "SessionsCommunicationChannelGrantAdministrationPage"
	case "GET /v1/m/sessions/channels/{id}":
		return "SessionsCommunicationChannel"
	case "POST /v1/m/sessions/messages/send":
		return "SessionsCommunicationPublishResult"
	case "GET /v1/m/sessions/messages/{id}", "GET /v1/m/sessions/deliveries/{id}":
		return "SessionsCommunicationReadResult"
	case "GET /v1/m/sessions/inbox":
		return "SessionsCommunicationInboxPage"
	case "GET /v1/m/sessions/inbox/handoffs":
		return "SessionsCommunicationIncomingHandoffPage"
	case "GET /v1/m/sessions/deliveries/{id}/handoff":
		return "SessionsCommunicationIncomingHandoffReadResult"
	case "GET /v1/m/sessions/inbox/cursors/personal/{recipient}":
		return "SessionsCommunicationCursorTokenResult"
	case "PUT /v1/m/sessions/inbox/cursors/personal/{recipient}":
		return "SessionsCommunicationCursorAdvanceResult"
	case "POST /v1/m/sessions/deliveries/{id}/ack":
		return "SessionsCommunicationAckResult"
	case "POST /v1/m/sessions/handoffs":
		return "SessionsCommunicationHandoffOfferResult"
	case "POST /v1/m/sessions/handoffs/{id}/responses":
		return "SessionsCommunicationHandoffResponseResult"
	default:
		return ""
	}
}

func (o Operation) sessionsCommunicationInputType() string {
	return o.goName() + "Input"
}

func (o Operation) sessionsCommunicationHasInput() bool {
	return o.HasBody || len(o.Parameters) > 0
}

func typedFieldName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return !isASCIIAlnum(r) })
	initialisms := map[string]string{
		"acl": "ACL", "api": "API", "etag": "ETag", "id": "ID", "json": "JSON",
		"ms": "MS", "sid": "SID", "url": "URL", "uuid": "UUID",
	}
	var out strings.Builder
	for _, part := range parts {
		lower := strings.ToLower(part)
		if initialism, ok := initialisms[lower]; ok {
			out.WriteString(initialism)
			continue
		}
		out.WriteString(strings.ToUpper(lower[:1]))
		out.WriteString(lower[1:])
	}
	return out.String()
}

// sessionsCommunicationSchemaCatalog is the single language-neutral model for
// the marked family. Every DTO below is derived from the canonical OpenAPI
// request/success schemas; emitters do not carry a second handwritten field list.
type sessionsCommunicationSchemaCatalog struct {
	Types  []*sessionsCommunicationObject
	byName map[string]*sessionsCommunicationObject
	// doc resolves nested $ref into #/components/schemas while the catalog walks a
	// schema, so a shared type is written once and consumed by both the published
	// components section and these DTOs.
	doc *Document
	// trail is the components this walk is CURRENTLY INSIDE, plus its nesting depth.
	// It spans the whole traversal rather than one dereference, because the recursion
	// that can run away lives here — in the walk over properties and items — and not in
	// a single top-level ref chain.
	trail *schemaRefTrail
}

type sessionsCommunicationObject struct {
	Name   string
	Fields []sessionsCommunicationField
}

type sessionsCommunicationField struct {
	Name     string
	Required bool
	Type     sessionsCommunicationType
}

type sessionsCommunicationType struct {
	Kind     string
	Format   string
	Object   string
	Items    *sessionsCommunicationType
	Nullable bool
}

type rawSessionsCommunicationSchema struct {
	Type       string                     `json:"type"`
	Format     string                     `json:"format"`
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	Items      json.RawMessage            `json:"items"`
	AnyOf      []json.RawMessage          `json:"anyOf"`
	// AdditionalProperties distinguishes a closed object from a declared string MAP.
	// It is json.RawMessage because OpenAPI overloads the key: `false` closes an
	// object, and a schema opens a map of that value type.
	AdditionalProperties json.RawMessage `json:"additionalProperties"`
}

// stringValueMap reports whether a schema is a declared `map[string]string`: an object
// with NO properties whose additionalProperties is a string schema.
//
// ⛔ IT EXISTS BECAUSE THE RATIFIED WIRE CONTRACT HAS ONE. The capability selectors are
// maps by design — `selectors.path.id` names a route's own path parameter, so the key set
// belongs to the operation and not to the DTO — and the type model had no way to say so:
// addObject demands properties, so such a schema died as "not a concrete object". The
// alternative was changing the wire shape, which is not this generator's to change.
func (s rawSessionsCommunicationSchema) stringValueMap() bool {
	if s.Type != "object" || len(s.Properties) != 0 || len(s.AdditionalProperties) == 0 {
		return false
	}
	var value struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(s.AdditionalProperties, &value) == nil && value.Type == "string"
}

func buildSessionsCommunicationSchemas(doc *Document) (*sessionsCommunicationSchemaCatalog, error) {
	catalog := &sessionsCommunicationSchemaCatalog{
		byName: make(map[string]*sessionsCommunicationObject), doc: doc,
		trail: newSchemaRefTrail(),
	}
	for _, op := range doc.Operations {
		if !op.sessionsCommunicationTyped() {
			continue
		}
		if op.HasBody {
			name := op.sessionsCommunicationBodyType()
			if name == "" {
				return nil, fmt.Errorf("%s %s: marked request has no SDK body type", op.Method, op.Path)
			}
			if err := catalog.addObject(name, op.RequestSchema, op.Method+" "+op.Path+" request"); err != nil {
				return nil, err
			}
		}
		name := op.sessionsCommunicationResultType()
		if name == "" {
			return nil, fmt.Errorf("%s %s: marked success response has no SDK result type", op.Method, op.Path)
		}
		if err := catalog.addObject(name, op.ResponseSchema, op.Method+" "+op.Path+" response"); err != nil {
			return nil, err
		}
	}
	sort.Slice(catalog.Types, func(i, j int) bool { return catalog.Types[i].Name < catalog.Types[j].Name })
	return catalog, nil
}

func (c *sessionsCommunicationSchemaCatalog) addObject(name string, raw json.RawMessage, location string) error {
	resolved, followed, refErr := c.doc.resolveSchemaTrail(raw, c.trail)
	if refErr != nil {
		return fmt.Errorf("%s: %s: %w", location, name, refErr)
	}
	raw = resolved
	// The components this schema was reached through stay OPEN for exactly as long as
	// its children are being walked below, which is the window a cycle has to be caught
	// in. Closing them on the way out is what keeps an ordinary second reference to the
	// same component — two fields sharing a type — perfectly legal.
	for _, component := range followed {
		if err := c.trail.enter(component); err != nil {
			return fmt.Errorf("%s: %s: %w", location, name, err)
		}
	}
	defer func() {
		for _, component := range followed {
			c.trail.leave(component)
		}
	}()
	if err := c.trail.descend(location); err != nil {
		return fmt.Errorf("%s: %s: %w", location, name, err)
	}
	defer c.trail.ascend()
	var schema rawSessionsCommunicationSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("%s: parse %s: %w", location, name, err)
	}
	if schema.Type != "object" || len(schema.Properties) == 0 {
		return fmt.Errorf("%s: %s must be a concrete object", location, name)
	}
	required := make(map[string]bool, len(schema.Required))
	for _, field := range schema.Required {
		if _, ok := schema.Properties[field]; !ok {
			return fmt.Errorf("%s: %s requires unknown property %q", location, name, field)
		}
		required[field] = true
	}
	propertyNames := make([]string, 0, len(schema.Properties))
	for field := range schema.Properties {
		propertyNames = append(propertyNames, field)
	}
	sort.Strings(propertyNames)
	object := &sessionsCommunicationObject{Name: name}
	for _, field := range propertyNames {
		typ, err := c.parseType(name, field, schema.Properties[field], location+"."+field)
		if err != nil {
			return err
		}
		object.Fields = append(object.Fields, sessionsCommunicationField{
			Name: field, Required: required[field], Type: typ,
		})
	}
	if existing, ok := c.byName[name]; ok {
		if existing.signature() != object.signature() {
			return fmt.Errorf("%s: schema for reused type %s differs from its first marked definition", location, name)
		}
		return nil
	}
	c.byName[name] = object
	c.Types = append(c.Types, object)
	return nil
}

func (c *sessionsCommunicationSchemaCatalog) parseType(
	parent, field string,
	raw json.RawMessage,
	location string,
) (sessionsCommunicationType, error) {
	resolved, followed, err := c.doc.resolveSchemaTrail(raw, c.trail)
	if err != nil {
		return sessionsCommunicationType{}, fmt.Errorf("%s: %w", location, err)
	}
	raw = resolved
	for _, component := range followed {
		if enterErr := c.trail.enter(component); enterErr != nil {
			return sessionsCommunicationType{}, fmt.Errorf("%s: %w", location, enterErr)
		}
	}
	defer func() {
		for _, component := range followed {
			c.trail.leave(component)
		}
	}()
	if descendErr := c.trail.descend(location); descendErr != nil {
		return sessionsCommunicationType{}, descendErr
	}
	defer c.trail.ascend()
	var schema rawSessionsCommunicationSchema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return sessionsCommunicationType{}, fmt.Errorf("%s: parse schema: %w", location, err)
	}
	if len(schema.AnyOf) > 0 {
		if len(schema.AnyOf) != 2 {
			return sessionsCommunicationType{}, fmt.Errorf("%s: only a value/null anyOf is supported", location)
		}
		var value json.RawMessage
		nullable := false
		for _, candidate := range schema.AnyOf {
			var part rawSessionsCommunicationSchema
			if err := json.Unmarshal(candidate, &part); err != nil {
				return sessionsCommunicationType{}, fmt.Errorf("%s: parse anyOf: %w", location, err)
			}
			if part.Type == "null" {
				nullable = true
				continue
			}
			if value != nil {
				return sessionsCommunicationType{}, fmt.Errorf("%s: anyOf has multiple non-null alternatives", location)
			}
			value = candidate
		}
		if !nullable || value == nil {
			return sessionsCommunicationType{}, fmt.Errorf("%s: anyOf must contain exactly one null alternative", location)
		}
		typ, err := c.parseType(parent, field, value, location)
		typ.Nullable = true
		return typ, err
	}
	typ := sessionsCommunicationType{Kind: schema.Type, Format: schema.Format}
	switch schema.Type {
	case "string", "integer", "boolean":
		return typ, nil
	case "object":
		if schema.stringValueMap() {
			typ.Kind = "map"
			return typ, nil
		}
		name := sessionsCommunicationNestedType(parent, field)
		if err := c.addObject(name, raw, location); err != nil {
			return sessionsCommunicationType{}, err
		}
		typ.Object = name
		return typ, nil
	case "array":
		if len(schema.Items) == 0 {
			return sessionsCommunicationType{}, fmt.Errorf("%s: array items are required", location)
		}
		item, err := c.parseType(parent, field, schema.Items, location+"[]")
		if err != nil {
			return sessionsCommunicationType{}, err
		}
		if item.Kind != "object" {
			return sessionsCommunicationType{}, fmt.Errorf("%s: arrays of %s are not supported by every SDK emitter", location, item.Kind)
		}
		typ.Items = &item
		return typ, nil
	default:
		return sessionsCommunicationType{}, fmt.Errorf("%s: unsupported schema type %q", location, schema.Type)
	}
}

func sessionsCommunicationNestedType(parent, field string) string {
	// The catalog page carries Channel items (not inbox read results) and each
	// item carries the caller's access bits; both are named by their parent so
	// the same field name in another page keeps its own established type.
	switch parent + "." + field {
	case "SessionsCommunicationChannelCatalogPage.items":
		return "SessionsCommunicationChannelCatalogItem"
	case "SessionsCommunicationChannelCatalogItem.my_access":
		return "SessionsCommunicationChannelAccess"
	// The incoming-handoff surface reuses two field names whose established
	// types are a DIFFERENT shape: "items" is an inbox read result and "handoff"
	// is the offer CONTENT. Naming these by parent keeps both established types
	// intact and gives the offer identity a name of its own. "content" is named
	// by parent for the same reason, and deliberately resolves to the SAME
	// handoff-content type the offer body already publishes.
	case "SessionsCommunicationIncomingHandoffPage.items":
		return "SessionsCommunicationIncomingHandoffSummary"
	case "SessionsCommunicationIncomingHandoffSummary.handoff",
		"SessionsCommunicationIncomingHandoffReadResult.handoff":
		return "SessionsCommunicationIncomingHandoffOffer"
	case "SessionsCommunicationIncomingHandoffReadResult.content":
		return "SessionsCommunicationHandoffContent"
	// The two administrative pages carry their OWN item types. Without these
	// rows the generic `items` fallback below would name them
	// SessionsCommunicationReadResult and then fail closed on the signature
	// mismatch — the exact collision the read catalog's rows exist to avoid.
	case "SessionsCommunicationChannelAdministrationPage.items":
		return "SessionsCommunicationChannelAdministrationItem"
	case "SessionsCommunicationChannelGrantAdministrationPage.items":
		return "SessionsCommunicationChannelGrantAdministrationItem"
	}
	// The capability DTOs name their nested types by parent, which keeps `questions`
	// and `results` from colliding with the K3 `items` fallback below.
	switch parent + "." + field {
	case "AuthCapabilityQuestions.questions":
		return "AuthCapabilityQuestion"
	case "AuthCapabilityQuestion.selectors":
		return "AuthCapabilitySelectors"
	case "AuthCapabilityResults.results":
		return "AuthCapabilityResult"
	}
	switch field {
	case "subject", "sender", "recipient", "granted_by", "revoked_by":
		return "SessionsCommunicationRef"
	case "reference", "references", "artifact_refs":
		return "SessionsCommunicationContentReference"
	case "blocks":
		return "SessionsCommunicationContentBlock"
	case "content":
		return "SessionsCommunicationMessageContent"
	case "initial_grants":
		return "SessionsCommunicationChannelGrantInput"
	case "channel":
		return "SessionsCommunicationChannel"
	case "grant", "grants":
		return "SessionsCommunicationChannelGrant"
	case "fulfillment":
		return "SessionsCommunicationFulfillment"
	case "message":
		return "SessionsCommunicationMessageView"
	case "delivery":
		return "SessionsCommunicationDeliveryView"
	case "items":
		return "SessionsCommunicationReadResult"
	case "projection":
		return "SessionsCommunicationCursorProjection"
	case "handoff":
		return "SessionsCommunicationHandoffContent"
	case "reason", "terminal_reason":
		return "SessionsCommunicationReason"
	case "carrier":
		return "SessionsCommunicationIncomingHandoffCarrier"
	case "work_item":
		return "SessionsCommunicationIncomingHandoffWorkItem"
	case "from", "to":
		return "SessionsCommunicationRef"
	default:
		return parent + typedFieldName(field)
	}
}

func (o *sessionsCommunicationObject) signature() string {
	var b strings.Builder
	for _, field := range o.Fields {
		fmt.Fprintf(&b, "%s:%t:%s;", field.Name, field.Required, field.Type.signature())
	}
	return b.String()
}

func (t sessionsCommunicationType) signature() string {
	item := ""
	if t.Items != nil {
		item = t.Items.signature()
	}
	return fmt.Sprintf("%s/%s/%s/%s/%t", t.Kind, t.Format, t.Object, item, t.Nullable)
}

func emitSessionsCommunicationGoTypes(b *strings.Builder, catalog *sessionsCommunicationSchemaCatalog) {
	for _, object := range catalog.Types {
		fmt.Fprintf(b, "type %s struct {\n", object.Name)
		for _, field := range object.Fields {
			jsonTag := field.Name
			if !field.Required {
				jsonTag += ",omitempty"
			}
			fmt.Fprintf(b, "\t%s %s `json:%q`\n", typedFieldName(field.Name),
				goSessionsCommunicationType(field.Type, field.Required), jsonTag)
		}
		b.WriteString("}\n\n")
	}
}

func goSessionsCommunicationType(typ sessionsCommunicationType, required bool) string {
	var value string
	switch typ.Kind {
	case "string":
		value = "string"
	case "integer":
		value = "int64"
	case "boolean":
		value = "bool"
	case "object":
		value = typ.Object
	case "map":
		// A declared string map is already a reference type; a pointer to it would
		// add a second way to spell "absent" beside the empty map.
		return "map[string]string"
	case "array":
		value = "[]" + goSessionsCommunicationType(*typ.Items, true)
	}
	if typ.Kind != "array" && (!required || typ.Nullable) {
		return "*" + value
	}
	return value
}

func emitSessionsCommunicationTypeScriptTypes(b *strings.Builder, catalog *sessionsCommunicationSchemaCatalog) {
	for _, object := range catalog.Types {
		fmt.Fprintf(b, "export interface %s {\n", object.Name)
		for _, field := range object.Fields {
			optional := ""
			if !field.Required {
				optional = "?"
			}
			fmt.Fprintf(b, "  %s%s: %s;\n", field.Name, optional,
				typeScriptSessionsCommunicationType(field.Type))
		}
		b.WriteString("}\n\n")
	}
}

func typeScriptSessionsCommunicationType(typ sessionsCommunicationType) string {
	var value string
	switch typ.Kind {
	case "string":
		value = "string"
	case "integer":
		value = "number"
	case "boolean":
		value = "boolean"
	case "object":
		value = typ.Object
	case "map":
		value = "Record<string, string>"
	case "array":
		item := typeScriptSessionsCommunicationType(*typ.Items)
		if typ.Items.Nullable {
			item = "(" + item + ")"
		}
		value = item + "[]"
	}
	if typ.Nullable {
		value += " | null"
	}
	return value
}

func emitSessionsCommunicationPythonTypes(b *strings.Builder, catalog *sessionsCommunicationSchemaCatalog) {
	for _, object := range catalog.Types {
		var required, optional []sessionsCommunicationField
		for _, field := range object.Fields {
			if field.Required {
				required = append(required, field)
			} else {
				optional = append(optional, field)
			}
		}
		// A published JSON field may be a Python keyword ("from" on the incoming
		// handoff offer) or otherwise not an identifier. The class syntax cannot
		// express it at all — it is a SyntaxError, not a lint — so those objects
		// are emitted with the functional TypedDict form, which takes the field
		// names as dict keys. Everything else keeps the class form byte for byte.
		if !pythonSessionsCommunicationClassSyntaxFits(object.Fields) {
			emitPythonSessionsCommunicationFunctionalType(b, object.Name, required, optional)
			continue
		}
		if len(optional) == 0 {
			fmt.Fprintf(b, "\nclass %s(TypedDict):\n", object.Name)
			emitPythonSessionsCommunicationFields(b, required)
			continue
		}
		if len(required) > 0 {
			fmt.Fprintf(b, "\nclass _%sRequired(TypedDict):\n", object.Name)
			emitPythonSessionsCommunicationFields(b, required)
			fmt.Fprintf(b, "\nclass %s(_%sRequired, total=False):\n", object.Name, object.Name)
			emitPythonSessionsCommunicationFields(b, optional)
			continue
		}
		fmt.Fprintf(b, "\nclass %s(TypedDict, total=False):\n", object.Name)
		emitPythonSessionsCommunicationFields(b, optional)
	}
	b.WriteString("\n")
}

// pythonSessionsCommunicationClassSyntaxFits reports whether every field name can
// be a Python class attribute. Keywords and non-identifiers cannot.
func pythonSessionsCommunicationClassSyntaxFits(fields []sessionsCommunicationField) bool {
	for _, field := range fields {
		if !validPythonIdentifier(field.Name) || pythonKeywords[field.Name] {
			return false
		}
	}
	return true
}

// pythonKeywords is the reserved-word set of the versions this SDK supports
// (pyproject requires-python >= 3.10), plus the soft keywords that are still
// legal attribute names and are therefore deliberately absent.
var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true,
	"assert": true, "async": true, "await": true, "break": true, "class": true,
	"continue": true, "def": true, "del": true, "elif": true, "else": true,
	"except": true, "finally": true, "for": true, "from": true, "global": true,
	"if": true, "import": true, "in": true, "is": true, "lambda": true,
	"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
	"return": true, "try": true, "while": true, "with": true, "yield": true,
}

func validPythonIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		if r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			continue
		}
		if index > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// emitPythonSessionsCommunicationFunctionalType writes one object with the
// functional TypedDict form. Annotations are QUOTED forward references: the
// functional form evaluates its dict eagerly, and the catalog is emitted in name
// order, so an unquoted reference to a type declared later would fail at import.
// Required and optional fields keep their totality by inheriting two bases, which
// is the only shape that expresses both when a name cannot be an attribute.
func emitPythonSessionsCommunicationFunctionalType(
	b *strings.Builder,
	name string,
	required, optional []sessionsCommunicationField,
) {
	entries := func(fields []sessionsCommunicationField) string {
		var out strings.Builder
		for _, field := range fields {
			fmt.Fprintf(&out, "        %q: %q,\n", field.Name,
				pythonSessionsCommunicationType(field.Type))
		}
		return out.String()
	}
	switch {
	case len(optional) == 0:
		fmt.Fprintf(b, "\n%s = TypedDict(\n    %q,\n    {\n%s    },\n)\n",
			name, name, entries(required))
	case len(required) == 0:
		fmt.Fprintf(b, "\n%s = TypedDict(\n    %q,\n    {\n%s    },\n    total=False,\n)\n",
			name, name, entries(optional))
	default:
		fmt.Fprintf(b, "\n_%sRequired = TypedDict(\n    %q,\n    {\n%s    },\n)\n",
			name, "_"+name+"Required", entries(required))
		fmt.Fprintf(b, "\n_%sOptional = TypedDict(\n    %q,\n    {\n%s    },\n    total=False,\n)\n",
			name, "_"+name+"Optional", entries(optional))
		fmt.Fprintf(b, "\nclass %s(_%sRequired, _%sOptional):\n    pass\n",
			name, name, name)
	}
}

func emitPythonSessionsCommunicationFields(b *strings.Builder, fields []sessionsCommunicationField) {
	if len(fields) == 0 {
		b.WriteString("    pass\n")
		return
	}
	for _, field := range fields {
		fmt.Fprintf(b, "    %s: %s\n", field.Name, pythonSessionsCommunicationType(field.Type))
	}
}

func pythonSessionsCommunicationType(typ sessionsCommunicationType) string {
	var value string
	switch typ.Kind {
	case "string":
		value = "str"
	case "integer":
		value = "int"
	case "boolean":
		value = "bool"
	case "object":
		value = typ.Object
	case "map":
		value = "dict[str, str]"
	case "array":
		value = "list[" + pythonSessionsCommunicationType(*typ.Items) + "]"
	}
	if typ.Nullable {
		value += " | None"
	}
	return value
}

func emitSessionsCommunicationJavaTypes(b *strings.Builder, catalog *sessionsCommunicationSchemaCatalog) {
	b.WriteString(`

    private static String text(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof String ? (String) item : null;
    }

    private static long number(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof Number ? ((Number) item).longValue() : 0L;
    }

    private static Long optionalNumber(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof Number ? ((Number) item).longValue() : null;
    }

    private static boolean bool(Map<String, Object> value, String key) {
        return Boolean.TRUE.equals(value.get(key));
    }

    private static Boolean optionalBool(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof Boolean ? (Boolean) item : null;
    }

    @SuppressWarnings("unchecked")
    private static Map<String, Object> object(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof Map ? (Map<String, Object>) item : Map.of();
    }

    @SuppressWarnings("unchecked")
    private static List<Map<String, Object>> objects(Map<String, Object> value, String key) {
        Object item = value.get(key);
        return item instanceof List ? (List<Map<String, Object>>) item : List.of();
    }

    // A DECLARED string map: the JSON object's keys belong to the operation, not to the
    // DTO. Non-string values are dropped rather than coerced, so a malformed member
    // cannot enter the typed map as something it is not.
    private static Map<String, String> stringMap(Map<String, Object> value, String key) {
        Object item = value.get(key);
        if (!(item instanceof Map<?, ?> raw)) {
            return Map.of();
        }
        Map<String, String> out = new LinkedHashMap<>();
        for (Map.Entry<?, ?> entry : raw.entrySet()) {
            if (entry.getKey() instanceof String name && entry.getValue() instanceof String text) {
                out.put(name, text);
            }
        }
        return Map.copyOf(out);
    }
`)
	for _, object := range catalog.Types {
		emitSessionsCommunicationJavaObject(b, object)
	}
}

func emitSessionsCommunicationJavaObject(b *strings.Builder, object *sessionsCommunicationObject) {
	fmt.Fprintf(b, "\n    public record %s(\n", object.Name)
	for index, field := range object.Fields {
		comma := ","
		if index == len(object.Fields)-1 {
			comma = ""
		}
		required := "optional"
		if field.Required {
			required = "required"
		}
		fmt.Fprintf(b, "            /* %s */ %s %s%s\n", required,
			javaSessionsCommunicationType(field.Type, field.Required), field.Name, comma)
	}
	b.WriteString("    ) {\n")
	var nonNull []string
	for _, field := range object.Fields {
		if field.Required && !field.Type.Nullable && javaSessionsCommunicationReference(field.Type) {
			nonNull = append(nonNull, field.Name)
		}
	}
	if len(nonNull) > 0 {
		fmt.Fprintf(b, "        public %s {\n", object.Name)
		for _, field := range nonNull {
			fmt.Fprintf(b, "            Objects.requireNonNull(%s, %q);\n", field, field)
		}
		b.WriteString("        }\n\n")
	}
	fmt.Fprintf(b, "        static %s from(Map<String, Object> value) {\n", object.Name)
	fmt.Fprintf(b, "            return new %s(\n", object.Name)
	for index, field := range object.Fields {
		comma := ","
		if index == len(object.Fields)-1 {
			comma = ""
		}
		fmt.Fprintf(b, "                    %s%s\n", javaSessionsCommunicationRead(field), comma)
	}
	b.WriteString("            );\n        }\n    }\n")
}

func javaSessionsCommunicationReference(typ sessionsCommunicationType) bool {
	return typ.Kind == "string" || typ.Kind == "object" || typ.Kind == "array"
}

func javaSessionsCommunicationType(typ sessionsCommunicationType, required bool) string {
	switch typ.Kind {
	case "string":
		return "String"
	case "integer":
		if required && !typ.Nullable {
			return "long"
		}
		return "Long"
	case "boolean":
		if required && !typ.Nullable {
			return "boolean"
		}
		return "Boolean"
	case "object":
		return typ.Object
	case "map":
		return "Map<String, String>"
	case "array":
		return "List<" + javaSessionsCommunicationType(*typ.Items, true) + ">"
	default:
		return "Object"
	}
}

func javaSessionsCommunicationRead(field sessionsCommunicationField) string {
	name := fmt.Sprintf("%q", field.Name)
	switch field.Type.Kind {
	case "string":
		return "Client.text(value, " + name + ")"
	case "integer":
		if field.Required && !field.Type.Nullable {
			return "Client.number(value, " + name + ")"
		}
		return "Client.optionalNumber(value, " + name + ")"
	case "boolean":
		if field.Required && !field.Type.Nullable {
			return "Client.bool(value, " + name + ")"
		}
		return "Client.optionalBool(value, " + name + ")"
	case "object":
		conversion := field.Type.Object + ".from(Client.object(value, " + name + "))"
		if !field.Required || field.Type.Nullable {
			return "value.get(" + name + ") instanceof Map<?, ?> ? " + conversion + " : null"
		}
		return conversion
	case "map":
		conversion := "Client.stringMap(value, " + name + ")"
		if !field.Required || field.Type.Nullable {
			return "value.get(" + name + ") instanceof Map<?, ?> ? " + conversion + " : null"
		}
		return conversion
	case "array":
		conversion := "Client.objects(value, " + name + ").stream().map(" +
			field.Type.Items.Object + "::from).toList()"
		if !field.Required || field.Type.Nullable {
			return "value.get(" + name + ") instanceof List<?> ? " + conversion + " : null"
		}
		return conversion
	default:
		return "null"
	}
}
