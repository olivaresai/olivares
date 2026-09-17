// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Document is the parsed slice of the OpenAPI snapshot the generator consumes:
// the published operations plus the stability metadata core/api stamps on them
// (x-stability, deprecated, x-deprecated-at/x-sunset-at/x-migration-guide).
type Document struct {
	APIVersion      string // info.version, e.g. "v1"
	StabilityPolicy string // info.x-stability-policy
	SpecHash        string // sha256 hex of the snapshot bytes
	Operations      []Operation

	// Built exclusively from the canonical schemas on marked operations. All four
	// emitters consume this one model so their DTOs cannot drift apart.
	sessionsCommunicationSchemas *sessionsCommunicationSchemaCatalog

	// componentSchemas is #/components/schemas, retained so a typed family may name
	// its contract by $ref instead of repeating it inside the operation.
	componentSchemas map[string]json.RawMessage
}

// typedSDKFamilies is the closed set of x-olivares-sdk-family values that receive
// generated DTOs. It is a set rather than one constant so a second family can be added
// without loosening the fail-closed check on unknown values.
var typedSDKFamilies = map[string]bool{
	sessionsCommunicationFamily: true,
	authCapabilitiesFamily:      true,
}

// componentSchemaPrefix is the only $ref shape this generator resolves.
const componentSchemaPrefix = "#/components/schemas/"

// maxSchemaRefDepth bounds $ref following. A document whose refs form a cycle is a
// broken document, and the generator says so instead of recursing until it dies.
const maxSchemaRefDepth = 16

// maxSchemaNestingDepth bounds how deep the catalog may walk a schema's OBJECT
// structure, independently of how many $ref hops each level takes.
//
// ⛔ IT IS A SECOND BOUND ON A SECOND AXIS, and the two are not interchangeable. The ref
// trail below stops a graph that comes BACK to a component it is already inside; this
// stops a walk that simply goes too deep. A document could in principle nest inline
// objects far enough to exhaust the stack without ever repeating a component name, and a
// generator that only counted hops would still die there.
const maxSchemaNestingDepth = 64

// schemaRefTrail is the set of components the catalog is CURRENTLY INSIDE, plus the
// nesting depth of the walk. One value is threaded through the whole traversal.
//
// ⛔ IT IS A STACK, NOT A VISITED SET, AND THAT IS THE WHOLE DESIGN. A visited set would
// reject the second, perfectly ordinary reference to a component that two fields happen
// to share — the published contract does exactly that, and rejecting it would break
// generation of a correct document. What is not allowed is re-entering a component while
// still expanding it, because that is a cycle and nothing else.
type schemaRefTrail struct {
	active map[string]bool
	order  []string
	depth  int
}

func newSchemaRefTrail() *schemaRefTrail {
	return &schemaRefTrail{active: map[string]bool{}}
}

// enter marks a component as being expanded and reports the error when it already was.
func (t *schemaRefTrail) enter(name string) error {
	if t.active[name] {
		return fmt.Errorf(
			"schema component %q references itself through %s: recursive schemas are not supported",
			name, strings.Join(append(append([]string(nil), t.order...), name), " -> "),
		)
	}
	t.active[name] = true
	t.order = append(t.order, name)
	return nil
}

func (t *schemaRefTrail) leave(name string) {
	delete(t.active, name)
	for index := len(t.order) - 1; index >= 0; index-- {
		if t.order[index] == name {
			t.order = append(t.order[:index], t.order[index+1:]...)
			return
		}
	}
}

// descend increments the nesting depth, refusing a walk past the bound.
func (t *schemaRefTrail) descend(location string) error {
	if t.depth >= maxSchemaNestingDepth {
		return fmt.Errorf("%s: schema nesting exceeds %d levels", location, maxSchemaNestingDepth)
	}
	t.depth++
	return nil
}

func (t *schemaRefTrail) ascend() { t.depth-- }

// resolveSchema follows a top-level $ref into #/components/schemas, so a typed family's
// operation may point at the SAME schema the published components section carries.
//
// It resolves only the top level; nested $ref inside properties and items is resolved by
// the catalog as it walks them, because that is where the surrounding type context —
// which object a field belongs to — is known.
//
// ⛔ IT FAILS CLOSED ON EVERY UNRESOLVABLE CASE. An unknown component, a foreign
// document, or a ref chain deeper than the bound is an error, never a silent fallback to
// the untyped emitters: a DTO that quietly disappears from four SDKs with exit 0 is the
// failure mode this whole generator is written to refuse.
func (d *Document) resolveSchema(raw json.RawMessage) (json.RawMessage, error) {
	resolved, _, err := d.resolveSchemaTrail(raw, newSchemaRefTrail())
	return resolved, err
}

// resolveSchemaTrail dereferences a schema and reports WHICH components it passed
// through, so the caller can hold them open for as long as it walks their children.
//
// ⛔ IT REPORTS THE NAMES INSTEAD OF MARKING THEM ITSELF, and that asymmetry is the fix
// for the defect this exists to close. A component must count as "being expanded" for
// the whole time its CHILDREN are being walked, and that walk happens in the caller,
// after this function has returned. Marking and unmarking here would open and close the
// window before the recursion that needs it — which is precisely how a property
// pointing back at its own ancestor slipped past the old hop counter and ran until the
// stack was gone.
func (d *Document) resolveSchemaTrail(
	raw json.RawMessage, trail *schemaRefTrail,
) (json.RawMessage, []string, error) {
	var followed []string
	for depth := 0; depth <= maxSchemaRefDepth; depth++ {
		if len(bytes.TrimSpace(raw)) == 0 {
			return raw, followed, nil
		}
		var probe struct {
			Ref string `json:"$ref"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil || probe.Ref == "" {
			return raw, followed, nil
		}
		if !strings.HasPrefix(probe.Ref, componentSchemaPrefix) {
			return nil, nil, fmt.Errorf("unsupported schema reference %q", probe.Ref)
		}
		name := strings.TrimPrefix(probe.Ref, componentSchemaPrefix)
		target, ok := d.componentSchemas[name]
		if !ok {
			return nil, nil, fmt.Errorf("schema reference %q names no component", probe.Ref)
		}
		// A component reached while it is still being expanded — by its own property,
		// by a chain, or mutually through another component — is a cycle.
		if trail.active[name] {
			return nil, nil, fmt.Errorf(
				"schema component %q references itself through %s: recursive schemas are not supported",
				name, strings.Join(append(append([]string(nil), trail.order...), name), " -> "),
			)
		}
		for _, seen := range followed {
			if seen == name {
				return nil, nil, fmt.Errorf(
					"schema reference %q revisits %q within one chain: recursive schemas are not supported",
					probe.Ref, name,
				)
			}
		}
		followed = append(followed, name)
		raw = target
	}
	return nil, nil, fmt.Errorf("schema reference chain exceeds %d hops", maxSchemaRefDepth)
}

// Operation is one published method+path.
type Operation struct {
	Method                 string // upper-case
	Path                   string // spec path, e.g. /v1/agents/{id} — also the dedup route key
	Summary                string
	Stability              string
	Deprecated             bool
	DeprecatedAt           string
	SunsetAt               string
	MigrationGuide         string
	PathParams             []string // in order of appearance
	HasBody                bool     // the generated operation carries a request body
	BodyRequired           bool     // requestBody.required; false is the OpenAPI default
	RequestBodyDisposition string   // handler-derived beta classification; empty for legacy stable
	RequestContentType     string   // the one declared media type, when the contract has exactly one
	RawBody                bool     // 200 is NOT application/json → raw-returning operation
	RawReqBody             bool     // opaque or non-JSON requestBody → raw bytes are sent
	SDKFamily              string
	Parameters             []OperationParameter
	RequestSchema          json.RawMessage
	ResponseSchema         json.RawMessage
}

type OperationParameter struct {
	Name     string
	In       string
	Required bool
	Type     string
}

// bodyRequiredInSignature preserves the stable SDK's historically optional
// body parameter while making classified beta request bodies exact.
func (o Operation) bodyRequiredInSignature() bool {
	return o.HasBody && o.RequestBodyDisposition != "" && o.BodyRequired
}

// methodOrder fixes the emission order inside one path (determinism).
var methodOrder = []string{"get", "post", "put", "patch", "delete"}

// pathItemFields are the OpenAPI path-item keys that are NOT operations; any
// other non-methodOrder key is an operation the generator does not understand
// and must refuse (it would otherwise vanish from the SDKs with exit 0).
var pathItemFields = map[string]bool{
	"$ref": true, "summary": true, "description": true, "servers": true, "parameters": true,
}

type rawOp struct {
	Summary                 string          `json:"summary"`
	Deprecated              bool            `json:"deprecated"`
	XStability              string          `json:"x-stability"`
	XDeprecatedAt           string          `json:"x-deprecated-at"`
	XSunsetAt               string          `json:"x-sunset-at"`
	XMigrationGuide         string          `json:"x-migration-guide"`
	XRequestBodyDisposition json.RawMessage `json:"x-olivares-request-body-disposition"`
	XSDKFamily              string          `json:"x-olivares-sdk-family"`
	Parameters              []struct {
		Name     string `json:"name"`
		In       string `json:"in"`
		Required bool   `json:"required"`
		Schema   struct {
			Type string `json:"type"`
		} `json:"schema"`
	} `json:"parameters"`
	Responses map[string]struct {
		Content map[string]struct {
			Schema json.RawMessage `json:"schema"`
		} `json:"content"`
	} `json:"responses"`
	RequestBody rawRequestBody `json:"requestBody"`
}

type rawRequestBody struct {
	Present  bool
	Required bool                       `json:"required"`
	Content  map[string]json.RawMessage `json:"content"`
}

func (b *rawRequestBody) UnmarshalJSON(raw []byte) error {
	b.Present = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	type wireRequestBody struct {
		Required bool                       `json:"required"`
		Content  map[string]json.RawMessage `json:"content"`
	}
	var wire wireRequestBody
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	b.Required = wire.Required
	b.Content = wire.Content
	return nil
}

// isRawBody reports whether the 200 response declares a non-JSON body.
func (o rawOp) isRawBody() bool {
	ok200, ok := o.Responses["200"]
	if !ok || len(ok200.Content) == 0 {
		return false
	}
	_, hasJSON := ok200.Content["application/json"]
	return !hasJSON
}

func (o rawOp) successSchema() json.RawMessage {
	for _, status := range []string{"200", "201"} {
		if response, ok := o.Responses[status]; ok {
			if media, ok := response.Content["application/json"]; ok {
				return append(json.RawMessage(nil), media.Schema...)
			}
		}
	}
	return nil
}

func (o rawOp) jsonRequestSchema() json.RawMessage {
	media, ok := o.RequestBody.Content["application/json"]
	if !ok {
		return nil
	}
	var content struct {
		Schema json.RawMessage `json:"schema"`
	}
	if json.Unmarshal(media, &content) != nil {
		return nil
	}
	return append(json.RawMessage(nil), content.Schema...)
}

func validateTypedFamilySchema(method, path, label string, raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%s %s: %s schema is required for the typed SDK family", method, path, label)
	}
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil || schema.Type != "object" || len(schema.Properties) == 0 {
		return fmt.Errorf("%s %s: %s schema must be a concrete object with properties", method, path, label)
	}
	return nil
}

// isRawReqBody reports whether the requestBody declares a non-JSON content type
// (e.g. application/octet-stream) — the operation sends raw bytes, not JSON.
func (o rawOp) isRawReqBody() bool {
	if len(o.RequestBody.Content) == 0 {
		return false
	}
	_, hasJSON := o.RequestBody.Content["application/json"]
	return !hasJSON
}

// hasReqBody reports whether the operation explicitly declares request
// content. JSON operations retain the generator's historical method-based body
// inference, but raw-response operations must use this declaration so a POST
// with no request body can be represented without silently discarding data.
func (o rawOp) hasReqBody() bool {
	return len(o.RequestBody.Content) != 0
}

func (o rawOp) singleRequestContentType() string {
	if len(o.RequestBody.Content) != 1 {
		return ""
	}
	for contentType := range o.RequestBody.Content {
		return contentType
	}
	return ""
}

func validateRequestContentType(method, path, contentType string) error {
	if contentType == "" {
		return fmt.Errorf("%s %s: requestBody media type must not be empty", method, path)
	}
	parsed, params, err := mime.ParseMediaType(contentType)
	if err != nil || parsed != contentType || len(params) != 0 || strings.Contains(contentType, "*") {
		return fmt.Errorf(
			"%s %s: requestBody media type %q must be canonical and contain no parameters",
			method, path, contentType,
		)
	}
	return nil
}

func (o rawOp) requestBodyDisposition(method, path string) (string, bool, error) {
	raw := bytes.TrimSpace(o.XRequestBodyDisposition)
	if len(raw) == 0 {
		return "", false, nil
	}
	if bytes.Equal(raw, []byte("null")) {
		return "", true, fmt.Errorf(
			"%s %s: x-olivares-request-body-disposition must be a non-empty string, got null",
			method, path,
		)
	}
	var disposition string
	if err := json.Unmarshal(raw, &disposition); err != nil {
		return "", true, fmt.Errorf(
			"%s %s: x-olivares-request-body-disposition must be a string",
			method, path,
		)
	}
	if disposition == "" {
		return "", true, fmt.Errorf(
			"%s %s: x-olivares-request-body-disposition must be a non-empty string, got %q",
			method, path, disposition,
		)
	}
	return disposition, true, nil
}

// sdkRequestBody preserves the historical verb inference for contracts that
// do not publish a disposition (notably the stable document), while making the
// beta module contract exact once its handler-derived classification is present.
// An unclassified or contradictory operation cannot yield a trustworthy SDK
// signature, so generation fails closed instead of guessing from the HTTP verb.
func (o rawOp) sdkRequestBody(
	method, path string,
	requireDisposition bool,
) (bool, string, string, error) {
	legacy := method == "POST" || method == "PUT" || method == "PATCH"
	disposition, present, err := o.requestBodyDisposition(method, path)
	if err != nil {
		return false, "", "", err
	}
	if requireDisposition && isMutationMethod(method) && !present {
		return false, "", "", fmt.Errorf(
			"%s %s: x-olivares-request-body-disposition is required in the beta document",
			method, path,
		)
	}
	contentType := o.singleRequestContentType()
	switch disposition {
	case "":
		return legacy, "", contentType, nil
	case "schema-published", "opaque-body":
		if len(o.RequestBody.Content) == 0 {
			return false, "", "", fmt.Errorf(
				"%s %s: x-olivares-request-body-disposition=%q requires requestBody content",
				method, path, disposition,
			)
		}
		if len(o.RequestBody.Content) != 1 {
			return false, "", "", fmt.Errorf(
				"%s %s: x-olivares-request-body-disposition=%q requires exactly one requestBody media type, got %d",
				method, path, disposition, len(o.RequestBody.Content),
			)
		}
		if err := validateRequestContentType(method, path, contentType); err != nil {
			return false, "", "", err
		}
		return true, disposition, contentType, nil
	case "bodyless":
		if o.RequestBody.Present {
			return false, "", "", fmt.Errorf(
				"%s %s: x-olivares-request-body-disposition=%q forbids requestBody",
				method, path, disposition,
			)
		}
		return false, disposition, "", nil
	case "unclassified":
		return false, "", "", fmt.Errorf(
			"%s %s: x-olivares-request-body-disposition=%q cannot produce an exact SDK signature",
			method, path, disposition,
		)
	default:
		return false, "", "", fmt.Errorf(
			"%s %s: unknown x-olivares-request-body-disposition=%q",
			method, path, disposition,
		)
	}
}

func isMutationMethod(method string) bool {
	return method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE"
}

// safeSegment matches the literal path segments the emitters can embed in a Go
// string, a Python string and a TS template literal without escaping.
var safeSegment = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// validateCommentText rejects any free-text field that would break (or escape)
// the comment / docstring context it is interpolated into. The Go emitter uses
// // line comments and is gofmt-gated, but the Java/TS block comments (/** */)
// and the Python docstring (""" """) fail OPEN — a stray */ or """ would close
// the comment early and turn the rest into live code with exit 0. The producer
// is in-repo (core/api), so failing closed beats escaping. Applied to EVERY
// free-text field that reaches generated code, not just the summary.
func validateCommentText(method, path, what, s string) error {
	for _, bad := range []string{`"""`, "*/", "`", "${", "\\", "\n", "\r"} {
		if strings.Contains(s, bad) {
			return fmt.Errorf("%s %s: %s contains %q, which would corrupt generated comments/docstrings — rephrase it in core/api", method, path, what, bad)
		}
	}
	for _, r := range s {
		if r < 0x20 {
			return fmt.Errorf("%s %s: %s contains a control character", method, path, what)
		}
	}
	return nil
}

// isWholeSegmentParam reports whether seg is exactly one `{param}` — braces at both
// ends, one of each, and something between them. Every other use of a brace inside a
// segment (`v{x}`, `{a}{b}`, `{}`) is templating the emitters cannot express.
//
// It is a named predicate rather than a negated five-term conjunction at the call site
// (staticcheck QF1001). De Morgan's rewrite of it is a five-term disjunction whose
// terms nobody can check by eye, and this predicate is what keeps a path the
// generator would mis-emit out of a published client.
func isWholeSegmentParam(seg string) bool {
	return strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") &&
		strings.Count(seg, "{") == 1 && strings.Count(seg, "}") == 1 && len(seg) > 2
}

// validatePath enforces the templating the emitters can express: whole-segment
// {param} only, safe charset on literal segments.
func validatePath(p string) error {
	if !strings.HasPrefix(p, "/") {
		return fmt.Errorf("path %q: must start with /", p)
	}
	for _, seg := range strings.Split(p, "/")[1:] {
		if seg == "" {
			continue
		}
		if strings.ContainsAny(seg, "{}") {
			if !isWholeSegmentParam(seg) {
				return fmt.Errorf("path %q: segment %q uses path templating the generator cannot express (only whole-segment {param})", p, seg)
			}
			continue
		}
		if !safeSegment.MatchString(seg) {
			return fmt.Errorf("path %q: segment %q contains characters outside the safe set [A-Za-z0-9._~-]", p, seg)
		}
	}
	return nil
}

func load(path string) (*Document, error) {
	return loadWithPolicy(path, false)
}

func loadWithPolicy(path string, requireRequestBodyDispositions bool) (*Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	var spec struct {
		OpenAPI string `json:"openapi"`
		Info    struct {
			Version          string `json:"version"`
			XStabilityPolicy string `json:"x-stability-policy"`
		} `json:"info"`
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if spec.OpenAPI == "" || len(spec.Paths) == 0 || spec.Info.Version == "" {
		return nil, fmt.Errorf("%s: not an OpenAPI document with info.version and paths", path)
	}

	doc := &Document{
		APIVersion:       spec.Info.Version,
		StabilityPolicy:  spec.Info.XStabilityPolicy,
		SpecHash:         hex.EncodeToString(sum[:]),
		componentSchemas: spec.Components.Schemas,
	}
	paths := make([]string, 0, len(spec.Paths))
	for p := range spec.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	known := map[string]bool{}
	for _, m := range methodOrder {
		known[m] = true
	}
	seenNames := map[string]Operation{}
	for _, p := range paths {
		if err := validatePath(p); err != nil {
			return nil, err
		}
		// Fail closed on operations the emitters would silently drop: an
		// unknown key under a path item is either a new HTTP method (teach the
		// generator) or a typo — never something to skip with exit 0.
		for k := range spec.Paths[p] {
			if !known[k] && !pathItemFields[k] && !strings.HasPrefix(k, "x-") {
				return nil, fmt.Errorf("path %q: unsupported path-item key %q — the operation would be silently dropped from the generated SDKs", p, k)
			}
		}
		for _, m := range methodOrder {
			rawJSON, ok := spec.Paths[p][m]
			if !ok {
				continue
			}
			var op rawOp
			if err := json.Unmarshal(rawJSON, &op); err != nil {
				return nil, fmt.Errorf("parse %s %s: %w", m, p, err)
			}
			method := strings.ToUpper(m)
			hasBody, disposition, requestContentType, err := op.sdkRequestBody(
				method, p, requireRequestBodyDispositions,
			)
			if err != nil {
				return nil, err
			}
			// Validate EVERY free-text field that reaches a generated comment/
			// docstring — summary AND the stability metadata (the latter flows
			// into the @deprecated / Stability lines), or a stray */ in e.g.
			// x-migration-guide would corrupt Java/TS/Python output with exit 0.
			for _, f := range []struct{ what, val string }{
				{"summary", op.Summary},
				{"x-stability", op.XStability},
				{"x-deprecated-at", op.XDeprecatedAt},
				{"x-sunset-at", op.XSunsetAt},
				{"x-migration-guide", op.XMigrationGuide},
			} {
				if err := validateCommentText(method, p, f.what, f.val); err != nil {
					return nil, err
				}
			}
			o := Operation{
				Method:                 method,
				Path:                   p,
				Summary:                op.Summary,
				Stability:              op.XStability,
				Deprecated:             op.Deprecated,
				DeprecatedAt:           op.XDeprecatedAt,
				SunsetAt:               op.XSunsetAt,
				MigrationGuide:         op.XMigrationGuide,
				PathParams:             pathParams(p),
				HasBody:                hasBody,
				BodyRequired:           op.RequestBody.Required,
				RequestBodyDisposition: disposition,
				RequestContentType:     requestContentType,
				RawBody:                op.isRawBody(),
				RawReqBody:             disposition == "opaque-body" || op.isRawReqBody(),
				SDKFamily:              op.XSDKFamily,
				RequestSchema:          op.jsonRequestSchema(),
				ResponseSchema:         op.successSchema(),
			}
			for _, parameter := range op.Parameters {
				if parameter.In == "path" || parameter.Name == "X-Olivares-Tenant" {
					continue
				}
				o.Parameters = append(o.Parameters, OperationParameter{
					Name: parameter.Name, In: parameter.In,
					Required: parameter.Required, Type: parameter.Schema.Type,
				})
			}
			if o.SDKFamily != "" {
				if !typedSDKFamilies[o.SDKFamily] {
					return nil, fmt.Errorf("%s %s: unsupported x-olivares-sdk-family %q", o.Method, o.Path, o.SDKFamily)
				}
				// ⛔ A TYPED FAMILY'S OPERATION SCHEMAS ARE DEREFERENCED HERE, and only
				// here. The catalog below demands a concrete object with properties, so
				// an operation that names its contract by $ref — which is how a schema
				// SHARED with the published components is written — would otherwise be
				// rejected as "not a concrete object" and silently fall back to the
				// untyped emitters. Resolving is what lets one schema serve both the
				// components section and the typed SDKs instead of being written twice.
				var refErr error
				if o.RequestSchema, refErr = doc.resolveSchema(o.RequestSchema); refErr != nil {
					return nil, fmt.Errorf("%s %s request: %w", o.Method, o.Path, refErr)
				}
				if o.ResponseSchema, refErr = doc.resolveSchema(o.ResponseSchema); refErr != nil {
					return nil, fmt.Errorf("%s %s success response: %w", o.Method, o.Path, refErr)
				}
				if o.HasBody {
					if err := validateTypedFamilySchema(o.Method, o.Path, "request", o.RequestSchema); err != nil {
						return nil, err
					}
				}
				if err := validateTypedFamilySchema(o.Method, o.Path, "success response", o.ResponseSchema); err != nil {
					return nil, err
				}
			}
			// Raw-response emitters dispatch to doRaw, which intentionally has no
			// request-body slot. A POST with no declared request body is therefore
			// representable; an operation that declares one must still fail closed
			// because otherwise its bytes would be silently dropped.
			if o.RawBody {
				if op.hasReqBody() {
					return nil, fmt.Errorf("%s %s: an operation with both a request body and a non-JSON 200 response is not expressible by the SDK generators (the request body would be silently dropped) — split the route or declare a JSON response", o.Method, o.Path)
				}
				o.HasBody = false
			}
			// All three languages derive from the same token sequence, so one
			// canonical collision check covers them. In Python a duplicate def
			// would silently SHADOW the first operation — never emit one.
			if prev, dup := seenNames[o.pyName()]; dup {
				return nil, fmt.Errorf("operation name collision: %s %s and %s %s both derive %q — rename one route",
					prev.Method, prev.Path, o.Method, o.Path, o.pyName())
			}
			seenNames[o.pyName()] = o
			doc.Operations = append(doc.Operations, o)
		}
	}
	catalog, err := buildSessionsCommunicationSchemas(doc)
	if err != nil {
		return nil, err
	}
	doc.sessionsCommunicationSchemas = catalog
	return doc, nil
}

// pathParams extracts {param} names in order of appearance.
func pathParams(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			out = append(out, seg[1:len(seg)-1])
		}
	}
	return out
}

// --- naming -------------------------------------------------------------------
//
// Operation names are DERIVED (the spec carries no operationIds) with a fixed,
// documented rule, so they are stable as long as the path is — renaming only
// happens when the route itself changes, which the stability policy already
// governs. The API major stays in the name (GetV1Agents): /v2 operations could
// then coexist in a future SDK major without collisions.

// isASCIIAlnum reports whether r is an ASCII letter or digit — the only runes that
// stay INSIDE a word when a path segment is split into words. Everything else is a
// separator, non-ASCII letters included: an identifier emitted into four languages
// has to be ASCII, and a rune that is a letter in Unicode but not in ASCII would
// otherwise be carried into a Go/Java/TS/Python symbol name.
//
// Named, positive predicate: the call site needs the negation, and expressing it as
// `!(a || b || c)` inline is the shape staticcheck QF1001 flags and the shape where
// one inverted comparison silently changes every generated method name.
func isASCIIAlnum(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9'
}

// words splits a path segment or param into lower-case words ("server-info" →
// [server info], "openapi.json" → [openapi json]).
func words(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !isASCIIAlnum(r) }) {
		out = append(out, strings.ToLower(w))
	}
	return out
}

// goInitialisms are upper-cased whole in Go identifiers (Go style).
var goInitialisms = map[string]string{
	"id": "ID", "url": "URL", "api": "API", "json": "JSON",
	"uuid": "UUID", "urn": "URN", "scim": "SCIM", "sso": "SSO",
}

func goWord(w string) string {
	if g, ok := goInitialisms[w]; ok {
		return g
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

func camelWord(w string) string { return strings.ToUpper(w[:1]) + w[1:] }

// nameParts renders the path as naming units: plain segments and ByParam units.
func nameParts(path string, word func(string) string) []string {
	var parts []string
	for _, seg := range strings.Split(strings.Trim(path, "/"), "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			parts = append(parts, "By")
			for _, w := range words(seg[1 : len(seg)-1]) {
				parts = append(parts, word(w))
			}
			continue
		}
		for _, w := range words(seg) {
			parts = append(parts, word(w))
		}
	}
	return parts
}

// goName: GET /v1/agents/{id} → GetV1AgentsByID.
func (o Operation) goName() string {
	return goWord(strings.ToLower(o.Method)) + strings.Join(nameParts(o.Path, goWord), "")
}

// tsName: GET /v1/agents/{id} → getV1AgentsById (JS casing, no all-cap initialisms).
func (o Operation) tsName() string {
	return strings.ToLower(o.Method) + strings.Join(nameParts(o.Path, camelWord), "")
}

// javaName: GET /v1/agents/{id} → getV1AgentsById. Java methods are
// lowerCamelCase with no all-cap initialisms — identical to the JS/TS casing,
// so the derived name matches tsName exactly.
func (o Operation) javaName() string {
	return strings.ToLower(o.Method) + strings.Join(nameParts(o.Path, camelWord), "")
}

// pyName: GET /v1/agents/{id} → get_v1_agents_by_id.
func (o Operation) pyName() string {
	var parts []string
	parts = append(parts, strings.ToLower(o.Method))
	for _, seg := range strings.Split(strings.Trim(o.Path, "/"), "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			parts = append(parts, "by")
			parts = append(parts, words(seg[1:len(seg)-1])...)
			continue
		}
		parts = append(parts, words(seg)...)
	}
	return strings.Join(parts, "_")
}

// reservedIdents is the FULL union of Go keywords, Python 3 keywords (+ the
// match/case/type soft keywords), JS/TS reserved words including strict-mode
// ones, and Java keywords + the reserved literals (true/false/null). words()
// lowercases, so only lowercase forms can occur. Suffixing is harmless; an
// unlisted reserved word emits invalid code with exit 0 in Python/TS/Java
// (only Go fails closed via format.Source), so err wide.
var reservedIdents = map[string]bool{
	// Go
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
	// Python
	"and": true, "as": true, "assert": true, "async": true, "await": true,
	"class": true, "def": true, "del": true, "elif": true, "except": true,
	"finally": true, "from": true, "global": true, "in": true, "is": true,
	"lambda": true, "match": true, "nonlocal": true, "not": true, "or": true,
	"pass": true, "raise": true, "try": true, "while": true, "with": true,
	"yield": true,
	// JS/TS (incl. strict-mode + contextual)
	"catch": true, "debugger": true, "delete": true, "do": true, "enum": true,
	"export": true, "extends": true, "function": true, "implements": true,
	"instanceof": true, "let": true, "new": true, "of": true,
	"private": true, "protected": true, "public": true, "static": true,
	"super": true, "this": true, "throw": true, "typeof": true, "void": true,
	// Java keywords + reserved literals not already covered above. A path-param
	// named e.g. "long" must not emit a bare keyword as a Java identifier.
	"abstract": true, "boolean": true, "byte": true, "char": true,
	"double": true, "final": true, "float": true, "int": true, "long": true,
	"native": true, "short": true, "strictfp": true, "synchronized": true,
	"throws": true, "transient": true, "volatile": true,
	"true": true, "false": true, "null": true,
}

// paramIdent sanitizes a path-param name into a safe identifier for all three
// languages (lower-case word join; a leading digit or reserved word gets a
// trailing underscore).
func paramIdent(name string) string {
	w := words(name)
	id := strings.Join(w, "_")
	if id == "" {
		id = "param"
	}
	if id[0] >= '0' && id[0] <= '9' {
		id = "p" + id
	}
	if reservedIdents[id] {
		return id + "_"
	}
	return id
}

// docDeprecation renders the shared human sentence for a deprecated op.
func (o Operation) docDeprecation() string {
	s := "deprecated since " + o.DeprecatedAt
	if o.SunsetAt != "" {
		s += ", sunset " + o.SunsetAt
	}
	if o.MigrationGuide != "" {
		s += "; migration guide: " + o.MigrationGuide
	}
	return s + "."
}
