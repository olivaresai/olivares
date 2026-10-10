// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "github.com/olivaresai/olivares/core/api/oas"

// addOSAccountContract is the canonical producer for generated SDKs. The
// ceremony's ID is a single-use attempt, not a credential or an authority grant.
func addOSAccountContract(paths, schemas map[string]any) {
	ref := func(name string) map[string]any { return oas.Obj("$ref", "#/components/schemas/"+name) }
	str := func() map[string]any { return oas.Obj("type", "string") }
	id := func() map[string]any { return oas.Obj("type", "string", "format", "uuid") }
	object := func(fields map[string]any, required ...string) map[string]any {
		return oas.Obj("type", "object", "additionalProperties", false, "properties", fields, "required", required)
	}
	schemas["OSAccountBeginInput"] = object(oas.Obj("tenant", id(), "user_id", id(), "account", oas.Obj("type", "string", "minLength", 1, "maxLength", 256)), "tenant", "user_id", "account")
	schemas["OSAccountCeremony"] = object(oas.Obj("ceremony_id", id()), "ceremony_id")
	schemas["OSAccountCompleteInput"] = object(oas.Obj("ceremony_id", id(), "password", oas.Obj("type", "string", "format", "byte", "contentEncoding", "base64", "minLength", 4, "maxLength", 5464, "writeOnly", true, "description", "Base64-encoded UTF-8 native password, bounded to 4096 decoded bytes; discarded after this request.")), "ceremony_id", "password")
	schemas["OSAccountMapping"] = object(oas.Obj("tenant", id(), "user_id", id(), "uid", oas.Obj("type", "integer", "minimum", 1, "maximum", 4294967295), "account", str(), "digest", str()), "tenant", "user_id", "uid", "account", "digest")
	schemas["OSAccountRevoked"] = object(oas.Obj("ok", oas.Obj("type", "boolean", "const", true)), "ok")
	add := func(path, method, operation, summary, input, output, permission string, parameters ...any) {
		responses := oas.Obj("200", oas.JSONRespSchema("OK", ref(output)))
		for _, status := range []string{"400", "401", "403", "404", "409", "429", "503"} {
			responses[status] = oas.JSONRespSchema("Request refused", ref("Error"))
		}
		op := oas.Obj("operationId", operation, "summary", summary, "description", summary, "tags", []any{"auth"}, "security", oaBearer(), "x-stability", "stable", "responses", responses)
		if permission != "" {
			op["x-required-permission"] = permission
		}
		if input != "" {
			op["requestBody"] = oas.Obj("required", true, "content", oas.Obj("application/json", oas.Obj("schema", ref(input))))
		}
		if len(parameters) > 0 {
			op["parameters"] = parameters
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = oas.Obj()
			paths[path] = item
		}
		item[method] = op
	}
	add("/v1/auth/os-account-bindings", "post", "beginOSAccountBinding", "Start an OS-account binding under the current administrator's user authorization and configured step-up; direct TLS required.", "OSAccountBeginInput", "OSAccountCeremony", "user:write")
	add("/v1/auth/os-account-bindings/complete", "post", "completeOSAccountBinding", "Complete from the subject's own session with fresh native PAM authentication and Account validation; direct TLS required. Returns public mapping metadata only.", "OSAccountCompleteInput", "OSAccountMapping", "")
	target := oas.Obj("name", "id", "in", "path", "required", true, "schema", id())
	add("/v1/auth/os-account-bindings/{id}", "get", "getOSAccountBinding", "Read administrative mapping metadata and its audit digest; direct TLS and current administrative step-up required.", "", "OSAccountMapping", "user:write", target, oaTenantParam())
	add("/v1/auth/os-account-bindings/{id}", "delete", "revokeOSAccountBinding", "Revoke current authority while permanently retaining the immutable account/UID/subject reservation; direct TLS and current administrative step-up required.", "", "OSAccountRevoked", "user:write", target, oaTenantParam())
}
