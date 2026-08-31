// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestModuleMutationRequestBodyDispositionsAreCoherent(t *testing.T) {
	t.Parallel()

	checked := false
	for key := range operationDescriptions {
		method, path, ok := strings.Cut(key, " ")
		if !ok || !strings.HasPrefix(path, "/v1/m/") {
			continue
		}
		route, ok := moduleRouteFromOperationKey(method, path)
		if !ok || !moduleRouteIsMutation(route) {
			continue
		}
		checked = true
		op := moduleOperation(route)
		if problem := moduleRequestBodyDispositionProblem(op); problem != "" {
			t.Errorf("%s: %s", key, problem)
		}
		want := string(moduleRequestBodyDispositionFor(route))
		if got := op[moduleRequestBodyDispositionExtension]; got != want {
			t.Errorf("%s: %s = %#v, want %q", key, moduleRequestBodyDispositionExtension, got, want)
		}
	}
	if !checked {
		t.Fatal("operation catalog exposes no module mutations")
	}
}

func TestModuleNonMutationsDoNotPublishRequestBodyDisposition(t *testing.T) {
	t.Parallel()

	for key := range operationDescriptions {
		method, path, ok := strings.Cut(key, " ")
		if !ok || !strings.HasPrefix(path, "/v1/m/") {
			continue
		}
		route, ok := moduleRouteFromOperationKey(method, path)
		if !ok || moduleRouteIsMutation(route) {
			continue
		}
		if got := moduleOperation(route)[moduleRequestBodyDispositionExtension]; got != nil {
			t.Errorf("%s: non-mutation publishes %s=%#v", key, moduleRequestBodyDispositionExtension, got)
		}
	}
}

func TestModuleRequestBodyDispositionSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		route moduleRoute
		want  moduleRequestBodyDisposition
	}{
		{
			name:  "raw workspace file",
			route: moduleRoute{ns: "sessions", method: http.MethodPut, pattern: "/workspaces/{ref}/files/raw"},
			want:  moduleRequestBodySchemaPublished,
		},
		{
			name:  "legacy protocol binding",
			route: moduleRoute{ns: "sessions", method: http.MethodPost, pattern: "/protocol-bindings/{id}/reconcile"},
			want:  moduleRequestBodySchemaPublished,
		},
		{
			name:  "legacy run input",
			route: moduleRoute{ns: "sessions", method: http.MethodPost, pattern: "/runs/{ref}/input"},
			want:  moduleRequestBodySchemaPublished,
		},
		{
			name:  "legacy work mutation",
			route: moduleRoute{ns: "sessions", method: http.MethodPost, pattern: "/work-items"},
			want:  moduleRequestBodySchemaPublished,
		},
		{
			name:  "models command-like post",
			route: moduleRoute{ns: "models", method: http.MethodPost, pattern: "/routing-policies/{id}/resolve"},
			want:  moduleRequestBodyBodyless,
		},
		{
			name:  "models delete",
			route: moduleRoute{ns: "models", method: http.MethodDelete, pattern: "/routing-policies/{id}"},
			want:  moduleRequestBodyBodyless,
		},
		{
			name:  "finops delete",
			route: moduleRoute{ns: "finops", method: http.MethodDelete, pattern: "/budgets/{id}"},
			want:  moduleRequestBodyBodyless,
		},
		{
			name:  "compliance opaque JSON",
			route: moduleRoute{ns: "compliance", method: http.MethodPost, pattern: "/aims/pack"},
			want:  moduleRequestBodyOpaque,
		},
		{
			name:  "knowledge opaque NDJSON",
			route: moduleRoute{ns: "knowledge", method: http.MethodPost, pattern: "/memory/import"},
			want:  moduleRequestBodyOpaque,
		},
		{
			name:  "unknown mutation",
			route: moduleRoute{ns: "unknown", method: http.MethodPatch, pattern: "/future"},
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

	minimalBody := oaObj(
		"content", oaObj("application/json", oaObj("schema", oaObj())),
	)
	tests := []struct {
		name string
		op   map[string]any
		want string
	}{
		{
			name: "missing extension",
			op:   oaObj(),
			want: "mutation has no x-olivares-request-body-disposition",
		},
		{
			name: "schema disposition without body",
			op:   oaObj(moduleRequestBodyDispositionExtension, string(moduleRequestBodySchemaPublished)),
			want: "schema-published operation must declare requestBody",
		},
		{
			name: "opaque disposition without body",
			op:   oaObj(moduleRequestBodyDispositionExtension, string(moduleRequestBodyOpaque)),
			want: "opaque-body operation must declare requestBody",
		},
		{
			name: "bodyless disposition with body",
			op: oaObj(
				moduleRequestBodyDispositionExtension, string(moduleRequestBodyBodyless),
				"requestBody", minimalBody,
			),
			want: "bodyless operation must not declare requestBody",
		},
		{
			name: "unclassified disposition with body",
			op: oaObj(
				moduleRequestBodyDispositionExtension, string(moduleRequestBodyUnclassified),
				"requestBody", minimalBody,
			),
			want: "unclassified operation must not declare requestBody",
		},
		{
			name: "unknown disposition",
			op:   oaObj(moduleRequestBodyDispositionExtension, "future-kind"),
			want: "mutation has unsupported x-olivares-request-body-disposition value \"future-kind\"",
		},
		{
			name: "invented opaque properties",
			op: oaObj(
				moduleRequestBodyDispositionExtension, string(moduleRequestBodyOpaque),
				"requestBody", oaObj(
					"content", oaObj(
						"application/json", oaObj(
							"schema", oaObj("type", "object", "properties", oaObj("invented", oaObj())),
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

func moduleRouteFromOperationKey(method, path string) (moduleRoute, bool) {
	relative := strings.TrimPrefix(path, "/v1/m/")
	if relative == path || relative == "" {
		return moduleRoute{}, false
	}
	namespace, pattern, found := strings.Cut(relative, "/")
	if !found {
		pattern = "/"
	} else {
		pattern = "/" + pattern
	}
	return moduleRoute{ns: namespace, method: method, pattern: pattern}, namespace != ""
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
