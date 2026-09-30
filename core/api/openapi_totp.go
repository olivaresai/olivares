// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

// addTOTPContract publishes the actual native second-factor wire protocol.
// LoginResponse remains a completed session; password login also permits the
// disjoint pending challenge, which carries no session credential.
func addTOTPContract(paths, schemas map[string]any) {
	ref := func(name string) map[string]any { return oaObj("$ref", "#/components/schemas/"+name) }
	str := func() map[string]any { return oaObj("type", "string") }
	object := func(properties map[string]any, required ...string) map[string]any {
		out := oaObj("type", "object", "properties", properties)
		if len(required) > 0 {
			names := make([]any, len(required))
			for i, n := range required {
				names[i] = n
			}
			out["required"] = names
		}
		return out
	}
	response := func(name string) map[string]any { return oaJSONRespSchema("OK", ref(name)) }
	codes := oaObj("type", "array", "items", str(), "description", "Single-use recovery codes, revealed only in this response.")
	schemas["TOTPLoginChallenge"] = object(oaObj(
		"mfa_required", oaObj("type", "boolean", "const", true), "mfa_token", str(),
		"enrolment_required", oaObj("type", "boolean"), "pending_expires_in_s", oaObj("type", "integer")),
		"mfa_required", "mfa_token", "enrolment_required", "pending_expires_in_s")
	schemas["TOTPEnrolInput"] = object(oaObj("mfa_token", str()))
	schemas["TOTPActivateInput"] = object(oaObj("mfa_token", str(), "code", str()), "code")
	schemas["TOTPChallengeInput"] = object(oaObj("mfa_token", str(), "code", str(), "recovery_code", str()), "mfa_token")
	schemas["TOTPChallengeInput"].(map[string]any)["anyOf"] = []any{oaObj("required", []any{"code"}), oaObj("required", []any{"recovery_code"})}
	schemas["TOTPEnrolment"] = object(oaObj("secret", str(), "uri", str(), "algorithm", str(),
		"digits", oaObj("type", "integer"), "period", oaObj("type", "integer"), "qr_png_base64", oaObj("type", "string", "contentEncoding", "base64")),
		"secret", "uri", "algorithm", "digits", "period", "qr_png_base64")
	schemas["TOTPActivationResponse"] = object(oaObj("recovery_codes", codes, "token", str(), "session_id", oaObj("type", "string", "format", "uuid"),
		"expires_at", oaObj("type", "string", "format", "date-time")), "recovery_codes")
	schemas["TOTPStatus"] = object(oaObj("enrolled", oaObj("type", "boolean"), "algorithm", str(), "digits", oaObj("type", "integer"),
		"period", oaObj("type", "integer"), "seed_hint", str(), "activated_at", oaObj("type", "string", "format", "date-time"),
		"recovery_codes_remaining", oaObj("type", "integer")), "enrolled", "recovery_codes_remaining")
	schemas["TOTPPolicy"] = object(oaObj("require_for_admins", oaObj("type", "boolean")), "require_for_admins")
	schemas["TOTPOK"] = object(oaObj("ok", oaObj("type", "boolean", "const", true)), "ok")
	add := func(path, method, id, summary, output, input string, security []any, params ...any) {
		responses := oaObj("200", response(output))
		for _, status := range []string{"400", "401", "403", "404", "409", "429", "503"} {
			responses[status] = oaJSONRespSchema("Request refused", ref("Error"))
		}
		op := oaObj("operationId", id, "summary", summary, "tags", []any{"auth"}, "x-stability", "stable", "security", security, "responses", responses)
		if input != "" {
			op["requestBody"] = oaObj("required", true, "content", oaObj("application/json", oaObj("schema", ref(input))))
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = oaObj()
			paths[path] = item
		}
		item[method] = op
	}
	dual := []any{oaObj("bearerAuth", []any{}), oaObj()}
	add("/v1/auth/totp/enrol", "post", "enrolTOTP", "Start a TOTP enrolment with a session or a pending login", "TOTPEnrolment", "TOTPEnrolInput", dual)
	add("/v1/auth/totp/activate", "post", "activateTOTP", "Prove possession and reveal recovery codes once", "TOTPActivationResponse", "TOTPActivateInput", dual)
	add("/v1/auth/totp/challenge", "post", "completeTOTPLogin", "Complete a pending login with a code or recovery code", "LoginResponse", "TOTPChallengeInput", []any{oaObj()})
	add("/v1/auth/totp/status", "get", "getTOTPStatus", "Read the calling account's factor status", "TOTPStatus", "", oaBearer())
	add("/v1/auth/totp", "delete", "removeTOTP", "Remove the calling account's factor with AAL3", "TOTPOK", "", oaBearer())
	add("/v1/auth/totp/policy", "get", "getTOTPPolicy", "Read the administrator factor policy", "TOTPPolicy", "", oaBearer())
	add("/v1/auth/totp/policy", "put", "setTOTPPolicy", "Set the administrator factor policy with AAL3", "TOTPPolicy", "TOTPPolicy", oaBearer())
	id := oaObj("name", "id", "in", "path", "required", true, "schema", oaObj("type", "string", "format", "uuid"))
	add("/v1/users/{id}/totp", "get", "getUserTOTPStatus", "Read a member's factor status in the selected tenant", "TOTPStatus", "", oaBearer(), id, oaTenantParam())
	add("/v1/users/{id}/totp/reset", "post", "resetUserTOTP", "Reset a tenant-governed member's factor with AAL3", "TOTPOK", "", oaBearer(), id, oaTenantParam())
	login := paths["/v1/auth/login"].(map[string]any)["post"].(map[string]any)
	login["responses"].(map[string]any)["200"] = oaJSONRespSchema("Completed session or pending second-factor challenge", oaObj("oneOf", []any{ref("LoginResponse"), ref("TOTPLoginChallenge")}))
}
