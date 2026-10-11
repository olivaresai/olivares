// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api/oas"
	"github.com/olivaresai/olivares/core/auth"
)

// bodyKindsModule documents one operation of each body kind, so the coherence
// checks run over documents the module system really builds.
type bodyKindsModule struct{}

func (bodyKindsModule) APINamespace() string           { return "bodykinds" }
func (bodyKindsModule) Permissions() []auth.Permission { return nil }
func (bodyKindsModule) APIRoutes(reg RouteRegistrar) {
	reg.Handle(http.MethodPost, "/json", "", nil)
	reg.Handle(http.MethodPost, "/opaque", "", nil)
	reg.Handle(http.MethodPost, "/none", "", nil)
	reg.Handle(http.MethodPost, "/undocumented", "", nil)
	reg.Handle(http.MethodGet, "/read", "", nil)
}

func (bodyKindsModule) OperationDocumentation(method, pattern string) (ModuleOperationDocumentation, bool) {
	jsonBody := func(schema map[string]any) map[string]any {
		return oas.Obj("required", true, "content", oas.Obj("application/json", oas.Obj("schema", schema)))
	}
	switch method + " " + pattern {
	case "POST /json":
		return ModuleOperationDocumentation{
			BodyKind:    ModuleOperationJSONBody,
			RequestBody: jsonBody(oas.Obj("type", "object", "properties", oas.Obj("name", oas.Obj("type", "string")))),
		}, true
	case "POST /opaque":
		return ModuleOperationDocumentation{
			BodyKind:    ModuleOperationOpaqueBody,
			RequestBody: jsonBody(oas.Obj("type", "object")),
		}, true
	case "POST /none":
		return ModuleOperationDocumentation{BodyKind: ModuleOperationBodyless}, true
	}
	return ModuleOperationDocumentation{}, false
}

func bodyKindsOperations(t *testing.T) map[string]map[string]any {
	t.Helper()
	ops := map[string]map[string]any{}
	paths := ModuleOpenAPIDocument([]Module{bodyKindsModule{}})["paths"].(map[string]any)
	for path, item := range paths {
		for method, op := range item.(map[string]any) {
			ops[strings.ToUpper(method)+" "+path] = op.(map[string]any)
		}
	}
	return ops
}

func TestModuleMutationRequestBodyDispositionsAreCoherent(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"POST /v1/m/bodykinds/json":         "schema-published",
		"POST /v1/m/bodykinds/opaque":       "opaque-body",
		"POST /v1/m/bodykinds/none":         "bodyless",
		"POST /v1/m/bodykinds/undocumented": "unclassified",
	}
	ops := bodyKindsOperations(t)
	for key, disposition := range want {
		op, ok := ops[key]
		if !ok {
			t.Fatalf("%s is not in the module document", key)
		}
		if problem := moduleRequestBodyDispositionProblem(op); problem != "" {
			t.Errorf("%s: %s", key, problem)
		}
		if got := op[moduleRequestBodyDispositionExtension]; got != disposition {
			t.Errorf("%s: %s = %#v, want %q", key, moduleRequestBodyDispositionExtension, got, disposition)
		}
	}
}

func TestModuleNonMutationsDoNotPublishRequestBodyDisposition(t *testing.T) {
	t.Parallel()

	op, ok := bodyKindsOperations(t)["GET /v1/m/bodykinds/read"]
	if !ok {
		t.Fatal("GET /v1/m/bodykinds/read is not in the module document")
	}
	if got := op[moduleRequestBodyDispositionExtension]; got != nil {
		t.Errorf("non-mutation publishes %s=%#v", moduleRequestBodyDispositionExtension, got)
	}
}

// TestModuleRequestBodyDispositionSentinels pins the core half of the
// classification: a mutation no module documents stays unclassified. Each
// module pins its own kinds in its own package.
func TestModuleRequestBodyDispositionSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		route moduleRoute
		want  moduleRequestBodyDisposition
	}{
		{
			name:  "unknown mutation",
			route: moduleRoute{ns: "unknown", method: http.MethodPatch, pattern: "/future"},
			want:  moduleRequestBodyUnclassified,
		},
		{
			name: "documented opaque body",
			route: moduleRoute{ns: "unknown", method: http.MethodPost, pattern: "/import", documentation: &ModuleOperationDocumentation{
				BodyKind: ModuleOperationOpaqueBody, RequestBody: oas.Obj("content", oas.Obj("application/x-ndjson", oas.Obj("schema", oas.Obj()))),
			}},
			want: moduleRequestBodyOpaque,
		},
		{
			name:  "opaque kind without its body",
			route: moduleRoute{ns: "unknown", method: http.MethodPost, pattern: "/import", documentation: &ModuleOperationDocumentation{BodyKind: ModuleOperationOpaqueBody}},
			want:  moduleRequestBodyUnclassified,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := moduleRequestBodyDispositionFor(test.route); got != test.want {
				t.Fatalf("disposition = %q, want %q", got, test.want)
			}
		})
	}
}

func TestModuleRequestBodyDispositionMutantsReportExactMessages(t *testing.T) {
	t.Parallel()

	minimalBody := oas.Obj(
		"content", oas.Obj("application/json", oas.Obj("schema", oas.Obj())),
	)
	tests := []struct {
		name string
		op   map[string]any
		want string
	}{
		{
			name: "missing extension",
			op:   oas.Obj(),
			want: "mutation has no x-olivares-request-body-disposition",
		},
		{
			name: "schema disposition without body",
			op:   oas.Obj(moduleRequestBodyDispositionExtension, string(moduleRequestBodySchemaPublished)),
			want: "schema-published operation must declare requestBody",
		},
		{
			name: "opaque disposition without body",
			op:   oas.Obj(moduleRequestBodyDispositionExtension, string(moduleRequestBodyOpaque)),
			want: "opaque-body operation must declare requestBody",
		},
		{
			name: "bodyless disposition with body",
			op: oas.Obj(
				moduleRequestBodyDispositionExtension, string(moduleRequestBodyBodyless),
				"requestBody", minimalBody,
			),
			want: "bodyless operation must not declare requestBody",
		},
		{
			name: "unclassified disposition with body",
			op: oas.Obj(
				moduleRequestBodyDispositionExtension, string(moduleRequestBodyUnclassified),
				"requestBody", minimalBody,
			),
			want: "unclassified operation must not declare requestBody",
		},
		{
			name: "unknown disposition",
			op:   oas.Obj(moduleRequestBodyDispositionExtension, "future-kind"),
			want: "mutation has unsupported x-olivares-request-body-disposition value \"future-kind\"",
		},
		{
			name: "invented opaque properties",
			op: oas.Obj(
				moduleRequestBodyDispositionExtension, string(moduleRequestBodyOpaque),
				"requestBody", oas.Obj(
					"content", oas.Obj(
						"application/json", oas.Obj(
							"schema", oas.Obj("type", "object", "properties", oas.Obj("invented", oas.Obj())),
						),
					),
				),
			),
			want: "opaque-body operation requestBody content \"application/json\" must not publish schema properties",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := moduleRequestBodyDispositionProblem(test.op); got != test.want {
				t.Fatalf("mutant message = %q, want %q", got, test.want)
			}
		})
	}
}

func moduleRequestBodyDispositionProblem(op map[string]any) string {
	rawDisposition, ok := op[moduleRequestBodyDispositionExtension]
	if !ok {
		return "mutation has no x-olivares-request-body-disposition"
	}
	disposition, ok := rawDisposition.(string)
	if !ok {
		return fmt.Sprintf("mutation has non-string x-olivares-request-body-disposition value %#v", rawDisposition)
	}
	rawBody, hasBody := op["requestBody"]
	switch moduleRequestBodyDisposition(disposition) {
	case moduleRequestBodySchemaPublished, moduleRequestBodyOpaque:
		if !hasBody {
			return disposition + " operation must declare requestBody"
		}
		body, ok := rawBody.(map[string]any)
		if !ok {
			return disposition + " operation requestBody must be an object"
		}
		content, ok := body["content"].(map[string]any)
		if !ok || len(content) == 0 {
			return disposition + " operation requestBody.content must be a non-empty object"
		}
		for mediaType, rawMedia := range content {
			media, ok := rawMedia.(map[string]any)
			if !ok {
				return fmt.Sprintf("%s operation requestBody content %q must be an object", disposition, mediaType)
			}
			schema, ok := media["schema"].(map[string]any)
			if !ok {
				return fmt.Sprintf("%s operation requestBody content %q must carry an object schema", disposition, mediaType)
			}
			if disposition == string(moduleRequestBodySchemaPublished) && len(schema) == 0 {
				return fmt.Sprintf("%s operation requestBody content %q must carry a non-empty schema", disposition, mediaType)
			}
			if disposition == string(moduleRequestBodyOpaque) {
				if _, invented := schema["properties"]; invented {
					return fmt.Sprintf("%s operation requestBody content %q must not publish schema properties", disposition, mediaType)
				}
			}
		}
		return ""
	case moduleRequestBodyBodyless, moduleRequestBodyUnclassified:
		if hasBody {
			return disposition + " operation must not declare requestBody"
		}
		return ""
	default:
		return fmt.Sprintf("mutation has unsupported %s value %q", moduleRequestBodyDispositionExtension, disposition)
	}
}
